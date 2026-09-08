package main

import (
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"whatsapp-bridge/config"
	"whatsapp-bridge/wastate"
)

const contractVersion = 1

var (
	errMediaGone    = errors.New("media gone upstream")
	errMediaUnknown = errors.New("unknown message")
)

type mediaFetcher interface {
	Fetch(messageID, chatJID string) (absPath string, err error)
}

type messageSender interface {
	Send(chat, text, mediaAbsPath string) (nativeID string, sentAt time.Time, err error)
}

type loginFlow struct{} // Task 5

type contractDeps struct {
	Store    *MessageStore
	State    *wastate.State
	Cfg      *config.Config
	OwnerJID func() string
	Media    mediaFetcher
	Sender   messageSender
	Login    *loginFlow

	// contactsAvailable is probed once, at mux construction: on some
	// deployments (SQLite, whatsmeow's tables in a separate database file
	// from the messages one) whatsmeow_contacts simply isn't reachable from
	// this store, and /contacts has to say so rather than 500.
	contactsAvailable bool
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// One static bearer for Mindet (spec §6). The JWT the MCP server uses is a
// different door for a different caller; neither opens the other.
func bearerGate(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			writeErr(w, http.StatusUnauthorized, "bearer required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func newContractMux(d *contractDeps) http.Handler {
	d.contactsAvailable = d.Store.HasContacts()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", d.health)
	mux.HandleFunc("GET /messages", d.messages)
	mux.HandleFunc("GET /chats", d.chats)
	mux.HandleFunc("GET /contacts", d.contacts)
	mux.HandleFunc("GET /media/{id}", d.media)
	mux.HandleFunc("POST /send", d.send)
	// Task 5 adds POST /login and the unauthenticated /qr/{token} pages on
	// the outer mux.
	return bearerGate(d.Cfg.MindetBridgeToken, mux)
}

func (d *contractDeps) capabilities() []string {
	caps := []string{"messages", "chats"}
	if d.contactsAvailable {
		caps = append(caps, "contacts")
	}
	return append(caps, "media", "send", "login")
}

func (d *contractDeps) health(w http.ResponseWriter, r *http.Request) {
	auth := "ok"
	detail := "whatsmeow session ok"
	if !d.State.LoggedIn() {
		auth = "needs_login"
		detail = "no WhatsApp session: run the login flow"
	}
	var since *string
	if !d.State.ConnectedSince().IsZero() {
		s := fmtTime(d.State.ConnectedSince())
		since = &s
	}
	var last *string
	if t, ok, err := d.Store.LastArrival(); err == nil && ok {
		s := fmtTime(t)
		last = &s
	}
	writeJSON(w, 200, map[string]any{
		"contract": contractVersion, "source": "whatsapp", "connected": d.State.Connected(),
		"since": since, "last_message_at": last, "auth": auth,
		"capabilities": d.capabilities(), "detail": detail,
	})
}

// parseAware accepts only a time with a zone (spec §7).
func parseAware(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, errors.New("time must be RFC 3339 with a zone")
	}
	return t, nil
}

func (d *contractDeps) messages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var after int64
	if s := q.Get("since"); s != "" {
		seq, err := decodeCursor(s)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		after = seq
	}
	var until time.Time
	if s := q.Get("until"); s != "" {
		t, err := parseAware(s)
		if err != nil {
			writeErr(w, 400, "until: "+err.Error())
			return
		}
		until = t
	}
	limit := 500
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 1000 {
			writeErr(w, 400, "limit must be 1..1000")
			return
		}
		limit = n
	}
	rows, err := d.Store.ListArrived(after, until, limit)
	if err != nil {
		slog.Error("contract messages", "err", err)
		writeErr(w, 500, "store error")
		return
	}
	out := make([]contractMessage, 0, len(rows))
	next := encodeCursor(after) // the terminal page echoes what it was given
	for _, m := range rows {
		out = append(out, toContractMessage(m, d.OwnerJID(), d.Cfg.MediaSharedRoot, d.Cfg.MediaDownloadDir))
		next = encodeCursor(m.Seq)
	}
	writeJSON(w, 200, map[string]any{"messages": out, "next": next})
}

type contractChat struct {
	NativeID    string  `json:"native_id"`
	Name        *string `json:"name"`
	Kind        string  `json:"kind"`
	MemberCount *int    `json:"member_count"`
}

// chats and contacts are small for one account (a few hundred chats, a few
// thousand contacts), so the contract allows fetching "all of them"; `since`
// is accepted on both endpoints and ignored rather than rejected as unknown.
func (d *contractDeps) chats(w http.ResponseWriter, r *http.Request) {
	list, err := d.Store.ListChatsForContract()
	if err != nil {
		writeErr(w, 500, "store error")
		return
	}
	writeJSON(w, 200, map[string]any{"chats": list})
}

type contractContact struct {
	Key      *string `json:"key"`
	NativeID string  `json:"native_id"`
	Name     *string `json:"name"`
	Aliases  []rawID `json:"aliases"`
}

func (d *contractDeps) contacts(w http.ResponseWriter, r *http.Request) {
	if !d.contactsAvailable {
		writeErr(w, http.StatusNotFound, "contacts not available")
		return
	}
	list, err := d.Store.ListContactsForContract()
	if err != nil {
		slog.Error("contract contacts", "err", err)
		writeErr(w, 500, "store error")
		return
	}
	writeJSON(w, 200, map[string]any{"contacts": list})
}

// media reports where a message's file lives, relative to MediaSharedRoot —
// Mindet's daemon and this bridge both mount that root, so a relative path is
// all either side needs. Repeating the request is safe: Fetch downloads at
// most once and every later call finds the same file already on disk.
func (d *contractDeps) media(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	chat := r.URL.Query().Get("chat")
	if id == "" || chat == "" {
		writeErr(w, 400, "id and chat are required")
		return
	}
	m, err := d.Store.LookupMessage(id, chat)
	if err != nil {
		writeErr(w, 500, "store error")
		return
	}
	if m == nil {
		writeErr(w, 404, "unknown message")
		return
	}
	abs, err := d.Media.Fetch(id, chat)
	switch {
	case errors.Is(err, errMediaUnknown):
		writeErr(w, 404, "unknown message")
		return
	case errors.Is(err, errMediaGone):
		writeErr(w, 410, "upstream media expired")
		return
	case err != nil:
		slog.Warn("contract media", "id", id, "err", err)
		writeErr(w, 500, "download failed")
		return
	}
	rel, err := filepath.Rel(d.Cfg.MediaSharedRoot, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		writeErr(w, 500, "media is outside the shared root")
		return
	}
	var sha *string
	if len(m.FileSHA256) > 0 {
		h := hex.EncodeToString(m.FileSHA256)
		sha = &h
	}
	writeJSON(w, 200, map[string]any{"path": filepath.ToSlash(rel), "mime": mimeFor(m.MediaType, m.Filename), "sha256": sha})
}

type sendRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	To             struct {
		Chat string `json:"chat"`
	} `json:"to"`
	Text  string   `json:"text"`
	Files []string `json:"files"`
}

// send is idempotent on the caller's key (spec §3.6): the key is remembered
// before the reply is written, so a crash between the send and the reply
// still leaves one message, not two, and a retried request after a lost
// reply answers from memory instead of sending the boss the same text twice.
// One file per send in v1; a second file is a second send.
func (d *contractDeps) send(w http.ResponseWriter, r *http.Request) {
	var req sendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IdempotencyKey == "" || req.To.Chat == "" {
		writeErr(w, 400, "idempotency_key, to.chat required")
		return
	}
	if id, at, ok, err := d.Store.RecallSend(req.IdempotencyKey); err != nil {
		writeErr(w, 500, "store error")
		return
	} else if ok {
		writeJSON(w, 200, map[string]any{"native_id": id, "sent_at": fmtTime(at)})
		return
	}
	media := ""
	if len(req.Files) > 0 {
		media = filepath.Join(d.Cfg.MediaSharedRoot, filepath.FromSlash(req.Files[0]))
		if err := allowedMediaPath(d.Cfg.MediaDirs, media); err != nil {
			writeErr(w, 400, "file is outside the allowed directories: "+err.Error())
			return
		}
	}
	id, at, err := d.Sender.Send(req.To.Chat, req.Text, media)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	if err := d.Store.RememberSend(req.IdempotencyKey, id, at); err != nil {
		slog.Error("remember send", "err", err)
	}
	writeJSON(w, 200, map[string]any{"native_id": id, "sent_at": fmtTime(at)})
}
