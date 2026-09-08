package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func newContractServer(t *testing.T) (*httptest.Server, *MessageStore, *wastate.State) {
	t.Helper()
	s := newTestMessageStore(t)
	st := wastate.New()
	st.SetConnected(true)
	st.SetLoggedIn(true)
	cfg := &config.Config{MindetBridgeToken: testToken, MediaSharedRoot: t.TempDir(), MediaDownloadDir: t.TempDir() + "/whatsapp"}
	deps := &contractDeps{
		Store: s, State: st, Cfg: cfg, OwnerJID: func() string { return "77000000009@s.whatsapp.net" },
	}
	srv := httptest.NewServer(http.StripPrefix("/bridge/v1", newContractMux(deps)))
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

func TestChatsAndContacts(t *testing.T) {
	srv, s, _ := newContractServer(t)
	mustExec(t, s.db, `INSERT INTO chats (jid, name) VALUES
		('77000000001@s.whatsapp.net', 'engineer'), ('120363000000000000@g.us', 'tender team'), ('1@newsletter', 'news')`)
	_, c := get(t, srv.URL+"/bridge/v1/chats", testToken)
	kinds := map[string]string{}
	for _, x := range c["chats"].([]any) {
		ch := x.(map[string]any)
		kinds[ch["native_id"].(string)] = ch["kind"].(string)
	}
	if kinds["77000000001@s.whatsapp.net"] != "direct" || kinds["120363000000000000@g.us"] != "group" || kinds["1@newsletter"] != "feed" {
		t.Fatalf("%v", kinds)
	}
	mustExec(t, s.db, `CREATE TABLE IF NOT EXISTS whatsmeow_contacts (our_jid TEXT, their_jid TEXT, first_name TEXT, full_name TEXT, push_name TEXT, business_name TEXT)`)
	mustExec(t, s.db, `CREATE TABLE IF NOT EXISTS whatsmeow_lid_map (lid TEXT, pn TEXT)`)
	mustExec(t, s.db, `INSERT INTO whatsmeow_contacts VALUES ('me', '77000000001@s.whatsapp.net', 'eng', 'engineer example', 'eng', '')`)
	mustExec(t, s.db, `INSERT INTO whatsmeow_lid_map VALUES ('1839000000000000', '77000000001')`) // bare user parts, as whatsmeow stores them
	_, ct := get(t, srv.URL+"/bridge/v1/contacts", testToken)
	list := ct["contacts"].([]any)
	if len(list) != 1 {
		t.Fatalf("%+v", ct)
	}
	one := list[0].(map[string]any)
	if one["key"] != "e164:+77000000001" || one["name"] != "engineer example" {
		t.Fatalf("%+v", one)
	}
	if al := one["aliases"].([]any); len(al) != 1 || al[0].(map[string]any)["value"] != "1839000000000000@lid" {
		t.Fatalf("aliases: %+v", one["aliases"])
	}
}
