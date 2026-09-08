package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"whatsapp-bridge/config"
	"whatsapp-bridge/wastate"
)

const testToken = "0123456789abcdef0123456789abcdef"

var (
	contractDepsMu    sync.Mutex
	contractDepsBySrv = map[*httptest.Server]*contractDeps{}
)

// contractDepsOf returns the deps object backing a server created by
// newContractServer, so a test (or a later task) can mutate fields like
// Media/Sender after the mux already closed over the pointer.
func contractDepsOf(srv *httptest.Server) *contractDeps {
	contractDepsMu.Lock()
	defer contractDepsMu.Unlock()
	return contractDepsBySrv[srv]
}

// setMedia and setSender let a test swap in a fake after the mux already
// closed over the *contractDeps pointer.
func setMedia(srv *httptest.Server, m mediaFetcher) {
	contractDepsOf(srv).Media = m
}

func setSender(srv *httptest.Server, s messageSender) {
	contractDepsOf(srv).Sender = s
}

// setup runs against the store before the mux is built, so a test that needs
// whatsmeow's tables in place for the contacts-availability probe (run once,
// at mux construction) can create them in time.
func newContractServer(t *testing.T, setup ...func(*MessageStore)) (*httptest.Server, *MessageStore, *wastate.State) {
	t.Helper()
	s := newTestMessageStore(t)
	for _, fn := range setup {
		fn(s)
	}
	st := wastate.New()
	st.SetConnected(true)
	st.SetLoggedIn(true)
	root := t.TempDir()
	cfg := &config.Config{MindetBridgeToken: testToken, MediaSharedRoot: root, MediaDownloadDir: root + "/whatsapp"}
	deps := &contractDeps{
		Store: s, State: st, Cfg: cfg, OwnerJID: func() string { return "77000000009@s.whatsapp.net" },
		Login: newLoginFlow(st, &fakePairer{}, "https://wa.example"),
	}
	// Mirrors main.go's wiring: the qr/{token} pages sit on the outer mux,
	// unauthenticated, alongside the bearer-gated /bridge/v1/ mux.
	outer := http.NewServeMux()
	outer.Handle("/bridge/v1/", http.StripPrefix("/bridge/v1", newContractMux(deps)))
	outer.HandleFunc("GET /bridge/v1/qr/{token}", deps.Login.QRPage)
	outer.HandleFunc("GET /bridge/v1/qr/{token}/png", deps.Login.QRPNG)
	outer.HandleFunc("GET /bridge/v1/qr/{token}/status", deps.Login.QRStatus)
	srv := httptest.NewServer(outer)
	contractDepsMu.Lock()
	contractDepsBySrv[srv] = deps
	contractDepsMu.Unlock()
	t.Cleanup(func() {
		srv.Close()
		contractDepsMu.Lock()
		delete(contractDepsBySrv, srv)
		contractDepsMu.Unlock()
	})
	return srv, s, st
}

func get(t *testing.T, url, token string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp, body
}

func post(t *testing.T, url, token, body string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

// mustWriteT is mustWrite without the *testing.T — usable from a fake that
// has no test handle of its own.
func mustWriteT(path string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		panic(err)
	}
}

func TestBearerGate(t *testing.T) {
	srv, _, _ := newContractServer(t)
	if r, _ := get(t, srv.URL+"/bridge/v1/health", ""); r.StatusCode != 401 {
		t.Fatalf("no bearer: %d", r.StatusCode)
	}
	if r, _ := get(t, srv.URL+"/bridge/v1/health", "wrong"); r.StatusCode != 401 {
		t.Fatalf("wrong bearer: %d", r.StatusCode)
	}
	if r, _ := get(t, srv.URL+"/bridge/v1/health", testToken); r.StatusCode != 200 {
		t.Fatalf("right bearer: %d", r.StatusCode)
	}
}

// The QR link's token is the secret; a bearer is neither required nor
// checked. This route must never answer 401.
func TestQRRouteBypassesTheBearerGate(t *testing.T) {
	srv, _, st := newContractServer(t)
	st.SetLoggedIn(false)
	st.SetPairingQRPNG([]byte("png")) // a QR loop is running, so there is something to offer
	_, body := post(t, srv.URL+"/bridge/v1/login", testToken, `{}`)
	challenge, _ := body["challenge"].(map[string]any)
	if challenge == nil {
		t.Fatalf("expected a challenge with no bearer needed for qr routes yet: %+v", body)
	}
	if r, _ := get(t, srv.URL+"/bridge/v1/qr/not-a-real-token", ""); r.StatusCode == http.StatusUnauthorized {
		t.Fatalf("qr page must not require a bearer, got %d", r.StatusCode)
	}
	if r, _ := get(t, srv.URL+"/bridge/v1/qr/not-a-real-token/png", ""); r.StatusCode == http.StatusUnauthorized {
		t.Fatalf("qr png must not require a bearer, got %d", r.StatusCode)
	}
	r, status := get(t, srv.URL+"/bridge/v1/qr/not-a-real-token/status", "")
	if r.StatusCode == http.StatusUnauthorized {
		t.Fatalf("qr status must not require a bearer, got %d", r.StatusCode)
	}
	// An unknown/expired token never reports "paired" regardless of the
	// WhatsApp session's own login state.
	if status["paired"] != false || status["expired"] != true {
		t.Fatalf("unknown token status: %+v", status)
	}

	// Only exact registered patterns bypass the gate; anything else — no
	// token, a lookalike prefix, or extra path beyond /png — still needs the
	// bearer and falls to the gated /bridge/v1/ catch-all.
	for _, path := range []string{
		"/bridge/v1/qr",
		"/bridge/v1/qrx",
		"/bridge/v1/qr/not-a-real-token/png/extra",
	} {
		if r, _ := get(t, srv.URL+path, ""); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s without a bearer must be 401, got %d", path, r.StatusCode)
		}
	}
}

func TestHealthShape(t *testing.T) {
	srv, _, st := newContractServer(t)
	_, h := get(t, srv.URL+"/bridge/v1/health", testToken)
	if h["contract"] != float64(1) || h["source"] != "whatsapp" || h["connected"] != true || h["auth"] != "ok" {
		t.Fatalf("%+v", h)
	}
	caps := h["capabilities"].([]any)
	if len(caps) < 5 || caps[0] != "messages" {
		t.Fatalf("capabilities: %v", caps)
	}
	if since, _ := h["since"].(string); !strings.HasSuffix(since, "+00:00") {
		t.Fatalf("since must be aware: %v", h["since"])
	}
	st.SetLoggedIn(false)
	_, h = get(t, srv.URL+"/bridge/v1/health", testToken)
	if h["auth"] != "needs_login" {
		t.Fatalf("auth after logout: %v", h["auth"])
	}
	// No session and no QR available (a QR channel timeout, or a process
	// that never started pairing) means the daemon has nothing to offer:
	// the owner needs to restart the bridge to get a fresh QR channel.
	if detail, _ := h["detail"].(string); !strings.Contains(detail, "restart") {
		t.Fatalf("detail with no session and no QR must say restart: %v", h["detail"])
	}
}

func TestMessagesPagingCursorAndUntil(t *testing.T) {
	srv, s, _ := newContractServer(t)
	// empty store: a page with no since still carries a cursor, and echoes it back
	_, p := get(t, srv.URL+"/bridge/v1/messages", testToken)
	next, _ := p["next"].(string)
	if next == "" || len(p["messages"].([]any)) != 0 {
		t.Fatalf("empty page: %+v", p)
	}
	_, again := get(t, srv.URL+"/bridge/v1/messages?since="+next, testToken)
	if again["next"] != next {
		t.Fatalf("terminal page must echo the cursor: %v vs %v", again["next"], next)
	}
	mustExec(t, s.db, `INSERT INTO chats (jid, name) VALUES ('77000000001@s.whatsapp.net', 'x')`)
	for _, id := range []string{"a", "b", "c"} {
		_ = s.StoreMessage(id, "77000000001@s.whatsapp.net", "77000000001", id, time.Now().UTC(), false, "", "", "", nil, nil, nil, 0)
	}
	_, p1 := get(t, srv.URL+"/bridge/v1/messages?limit=2", testToken)
	if len(p1["messages"].([]any)) != 2 {
		t.Fatalf("limit: %+v", p1)
	}
	_, p2 := get(t, srv.URL+"/bridge/v1/messages?limit=2&since="+p1["next"].(string), testToken)
	msgs := p2["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["native_id"] != "c" {
		t.Fatalf("second page: %+v", p2)
	}
	m := msgs[0].(map[string]any)
	if m["author"].(map[string]any)["key"] != "e164:+77000000001" || !strings.HasSuffix(m["sent_at"].(string), "+00:00") {
		t.Fatalf("message shape: %+v", m)
	}
	if r, _ := get(t, srv.URL+"/bridge/v1/messages?until=2026-09-07T06:00:00", testToken); r.StatusCode != 400 {
		t.Fatalf("naive until must be 400, got %d", r.StatusCode)
	}
	if r, _ := get(t, srv.URL+"/bridge/v1/messages?since=not%20a%20cursor", testToken); r.StatusCode != 400 {
		t.Fatalf("malformed cursor must be 400, got %d", r.StatusCode)
	}
	_, old := get(t, srv.URL+"/bridge/v1/messages?until=2000-01-01T00:00:00%2B00:00", testToken)
	if len(old["messages"].([]any)) != 0 || old["next"] == "" {
		t.Fatalf("ancient until: %+v", old)
	}
	if r, _ := get(t, srv.URL+"/bridge/v1/messages?limit=5000", testToken); r.StatusCode != 400 {
		t.Fatalf("limit over the maximum must be 400, got %d", r.StatusCode)
	}
}

// A store rebuilt from an empty volume, or restored from an older dump,
// restarts its arrival sequence. Mindet's held cursor then names a place in
// a store that no longer exists: answering it with an empty page would look
// exactly like "nothing new" and stall ingestion forever, so it is a 400 —
// the one answer Mindet re-bootstraps on.
func TestCursorFromAnotherStoreIsRejected(t *testing.T) {
	srv, s, _ := newContractServer(t)
	mustExec(t, s.db, `INSERT INTO chats (jid, name) VALUES ('77000000001@s.whatsapp.net', 'x')`)
	_ = s.StoreMessage("a", "77000000001@s.whatsapp.net", "77000000001", "a", time.Now().UTC(), false, "", "", "", nil, nil, nil, 0)

	foreign := encodeCursor("deadbeefdeadbeef", 1)
	r, body := get(t, srv.URL+"/bridge/v1/messages?since="+foreign, testToken)
	if r.StatusCode != 400 || !strings.Contains(fmt.Sprint(body["error"]), "different store") {
		t.Fatalf("foreign cursor: %d %+v", r.StatusCode, body)
	}
	// the pre-generation cursor form is just as stale
	if r, _ := get(t, srv.URL+"/bridge/v1/messages?since=djE6NTAwMDA", testToken); r.StatusCode != 400 {
		t.Fatalf("cursor with no generation must be 400, got %d", r.StatusCode)
	}
	// ours still works
	if r, _ := get(t, srv.URL+"/bridge/v1/messages?since="+encodeCursor(s.Generation(), 0), testToken); r.StatusCode != 200 {
		t.Fatalf("our own cursor: %d", r.StatusCode)
	}
	if s.Generation() == "" {
		t.Fatal("a store must have a generation")
	}
}

func TestChatsAndContacts(t *testing.T) {
	srv, _, _ := newContractServer(t, func(s *MessageStore) {
		mustExec(t, s.db, `INSERT INTO chats (jid, name) VALUES
			('77000000001@s.whatsapp.net', 'engineer'), ('120363000000000000@g.us', 'tender team'), ('1@newsletter', 'news')`)
		mustExec(t, s.db, `CREATE TABLE IF NOT EXISTS whatsmeow_contacts (our_jid TEXT, their_jid TEXT, first_name TEXT, full_name TEXT, push_name TEXT, business_name TEXT)`)
		mustExec(t, s.db, `CREATE TABLE IF NOT EXISTS whatsmeow_lid_map (lid TEXT, pn TEXT)`)
		mustExec(t, s.db, `INSERT INTO whatsmeow_contacts VALUES ('me', '77000000001@s.whatsapp.net', 'eng', 'engineer example', 'eng', '')`)
		mustExec(t, s.db, `INSERT INTO whatsmeow_lid_map VALUES ('1839000000000000', '77000000001')`) // bare user parts, as whatsmeow stores them
		// an @lid contact the lid map cannot resolve: gets its own row, key null
		mustExec(t, s.db, `INSERT INTO whatsmeow_contacts VALUES ('me', '9990000000000000@lid', '', 'unlinked lid', '', '')`)
		// an @lid contact the lid map DOES resolve: must not appear as its own
		// row — it is already the alias on the phone row above
		mustExec(t, s.db, `INSERT INTO whatsmeow_contacts VALUES ('me', '1839000000000000@lid', '', 'should be hidden', '', '')`)
	})

	_, c := get(t, srv.URL+"/bridge/v1/chats", testToken)
	kinds := map[string]string{}
	for _, x := range c["chats"].([]any) {
		ch := x.(map[string]any)
		kinds[ch["native_id"].(string)] = ch["kind"].(string)
	}
	if kinds["77000000001@s.whatsapp.net"] != "direct" || kinds["120363000000000000@g.us"] != "group" || kinds["1@newsletter"] != "feed" {
		t.Fatalf("%v", kinds)
	}

	_, ct := get(t, srv.URL+"/bridge/v1/contacts", testToken)
	list := ct["contacts"].([]any)
	if len(list) != 2 {
		t.Fatalf("%+v", ct)
	}
	byNative := map[string]map[string]any{}
	for _, x := range list {
		m := x.(map[string]any)
		byNative[m["native_id"].(string)] = m
	}

	one := byNative["77000000001@s.whatsapp.net"]
	if one == nil || one["key"] != "e164:+77000000001" || one["name"] != "engineer example" {
		t.Fatalf("phone row: %+v", one)
	}
	if al := one["aliases"].([]any); len(al) != 1 || al[0].(map[string]any)["value"] != "1839000000000000@lid" {
		t.Fatalf("aliases: %+v", one["aliases"])
	}

	unmapped := byNative["9990000000000000@lid"]
	if unmapped == nil || unmapped["key"] != nil || unmapped["name"] != "unlinked lid" {
		t.Fatalf("unmapped lid row: %+v", unmapped)
	}

	if _, seen := byNative["1839000000000000@lid"]; seen {
		t.Fatalf("mapped lid contact must not appear as its own row: %+v", ct)
	}
}

// Some deployments never get whatsmeow_contacts in the same database this
// bridge can query (SQLite: it lives in a different file). The contract must
// not 500 on that — it drops "contacts" from health and answers 404.
func TestContactsUnavailable(t *testing.T) {
	srv, _, _ := newContractServer(t) // no whatsmeow_contacts table
	_, h := get(t, srv.URL+"/bridge/v1/health", testToken)
	for _, c := range h["capabilities"].([]any) {
		if c == "contacts" {
			t.Fatalf("capabilities must not list contacts when the table is unreachable: %v", h["capabilities"])
		}
	}
	r, body := get(t, srv.URL+"/bridge/v1/contacts", testToken)
	if r.StatusCode != http.StatusNotFound || body["error"] != "contacts not available" {
		t.Fatalf("contacts without the table: %d %+v", r.StatusCode, body)
	}
}

type fakeMedia struct {
	calls int
	gone  bool
	dir   string
}

func (f *fakeMedia) Fetch(id, chat string) (string, error) {
	f.calls++
	if id == "unknown" {
		return "", errMediaUnknown
	}
	if f.gone {
		return "", errMediaGone
	}
	p := f.dir + "/" + chat + "/" + id + ".ogg"
	mustWriteT(p)
	return p, nil
}

// failNext, when set, makes the next Send fail instead of sending, then
// clears itself — so a test can fail one attempt and let a retry through.
type fakeSender struct {
	sent     []string
	failNext bool
	failErr  error
}

func (f *fakeSender) Send(chat, text, media string) (string, time.Time, error) {
	if f.failNext {
		f.failNext = false
		return "", time.Time{}, f.failErr
	}
	f.sent = append(f.sent, chat+":"+text)
	return fmt.Sprintf("SENT%d", len(f.sent)), time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC), nil
}

func TestMediaIsIdempotentGoneIs410UnknownIs404AndIdIsPercentDecoded(t *testing.T) {
	srv, s, _ := newContractServer(t)
	fm := &fakeMedia{dir: contractDepsOf(srv).Cfg.MediaDownloadDir}
	setMedia(srv, fm)
	mustExec(t, s.db, `INSERT INTO chats (jid, name) VALUES ('c@s.whatsapp.net', 'x')`)
	_ = s.StoreMessage("v/1+", "c@s.whatsapp.net", "c", "", time.Now().UTC(), false, "audio", "v1.ogg", "u", []byte{1}, []byte{2}, []byte{3}, 4)
	r1, a := get(t, srv.URL+"/bridge/v1/media/v%2F1%2B?chat=c@s.whatsapp.net", testToken)
	r2, b := get(t, srv.URL+"/bridge/v1/media/v%2F1%2B?chat=c@s.whatsapp.net", testToken)
	if r1.StatusCode != 200 || r2.StatusCode != 200 || a["path"] != b["path"] || a["path"] != "whatsapp/c@s.whatsapp.net/v/1+.ogg" {
		t.Fatalf("%d %d %+v %+v", r1.StatusCode, r2.StatusCode, a, b)
	}
	if a["sha256"] != "02" || a["mime"] != "audio/ogg" {
		t.Fatalf("media info: %+v", a)
	}
	fm.gone = true
	if r, _ := get(t, srv.URL+"/bridge/v1/media/v%2F1%2B?chat=c@s.whatsapp.net", testToken); r.StatusCode != 410 {
		t.Fatalf("gone must be 410, got %d", r.StatusCode)
	}
	if r, _ := get(t, srv.URL+"/bridge/v1/media/unknown?chat=c@s.whatsapp.net", testToken); r.StatusCode != 404 {
		t.Fatalf("unknown must be 404, got %d", r.StatusCode)
	}
}

func TestSendIsIdempotentOnKey(t *testing.T) {
	srv, _, _ := newContractServer(t)
	fs := &fakeSender{}
	setSender(srv, fs)
	body := `{"idempotency_key":"k1","to":{"chat":"c@s.whatsapp.net"},"text":"hello"}`
	r1, a := post(t, srv.URL+"/bridge/v1/send", testToken, body)
	r2, b := post(t, srv.URL+"/bridge/v1/send", testToken, body)
	if r1.StatusCode != 200 || r2.StatusCode != 200 || a["native_id"] != b["native_id"] || len(fs.sent) != 1 {
		t.Fatalf("%d %d %+v %+v sent=%v", r1.StatusCode, r2.StatusCode, a, b, fs.sent)
	}
	if !strings.HasSuffix(a["sent_at"].(string), "+00:00") {
		t.Fatalf("sent_at: %v", a["sent_at"])
	}
	if r, _ := post(t, srv.URL+"/bridge/v1/send", testToken, `{"to":{"chat":"c"},"text":"x"}`); r.StatusCode != 400 {
		t.Fatalf("missing key must be 400, got %d", r.StatusCode)
	}
}

// A reservation with an empty native_id is a send still in flight — a second
// request with the same key must not reach the sender at all.
func TestSendInFlightAnswers409WithoutCallingSender(t *testing.T) {
	srv, s, _ := newContractServer(t)
	fs := &fakeSender{}
	setSender(srv, fs)
	// stamped now: a reservation this fresh is a send genuinely in flight,
	// not debris a restart or the TTL sweep would reclaim
	mustExec(t, s.db, `INSERT INTO sent_by_key (idempotency_key, native_id, sent_at_unix) VALUES ('k2', '', ?)`,
		time.Now().Unix())
	r, body := post(t, srv.URL+"/bridge/v1/send", testToken, `{"idempotency_key":"k2","to":{"chat":"c@s.whatsapp.net"},"text":"hi"}`)
	if r.StatusCode != 409 || body["error"] != "send in flight" || len(fs.sent) != 0 {
		t.Fatalf("in-flight: %d %+v sent=%v", r.StatusCode, body, fs.sent)
	}
}

// A failed send must release its reservation: the key was never actually
// used, so a retry has to be free to try again.
func TestFailedSendReleasesTheKeyForARetry(t *testing.T) {
	srv, _, _ := newContractServer(t)
	fs := &fakeSender{failNext: true, failErr: errors.New("upstream unreachable")}
	setSender(srv, fs)
	body := `{"idempotency_key":"k3","to":{"chat":"c@s.whatsapp.net"},"text":"hello"}`
	r1, _ := post(t, srv.URL+"/bridge/v1/send", testToken, body)
	if r1.StatusCode != 502 {
		t.Fatalf("first attempt should fail with 502, got %d", r1.StatusCode)
	}
	r2, a := post(t, srv.URL+"/bridge/v1/send", testToken, body)
	if r2.StatusCode != 200 || len(fs.sent) != 1 || a["native_id"] == nil {
		t.Fatalf("retry after a failed send should succeed: %d %+v sent=%v", r2.StatusCode, a, fs.sent)
	}
}
