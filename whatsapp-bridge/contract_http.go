package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"whatsapp-bridge/config"
	"whatsapp-bridge/wastate"
)

const contractVersion = 1

type mediaFetcher interface{}  // Task 4
type messageSender interface{} // Task 4
type loginFlow struct{}        // Task 5

type contractDeps struct {
	Store    *MessageStore
	State    *wastate.State
	Cfg      *config.Config
	OwnerJID func() string
	Media    mediaFetcher
	Sender   messageSender
	Login    *loginFlow
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
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", d.health)
	mux.HandleFunc("GET /messages", d.messages)
	mux.HandleFunc("GET /chats", d.chats)
	mux.HandleFunc("GET /contacts", d.contacts)
	// Task 4 adds: GET /media/{id}, POST /send. Task 5 adds POST /login and
	// the unauthenticated /qr/{token} pages on the outer mux.
	return bearerGate(d.Cfg.MindetBridgeToken, mux)
}

func (d *contractDeps) capabilities() []string {
	return []string{"messages", "chats", "contacts", "media", "send", "login"}
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
	list, err := d.Store.ListContactsForContract()
	if err != nil {
		writeErr(w, 500, fmt.Sprintf("store error: %v", err))
		return
	}
	writeJSON(w, 200, map[string]any{"contacts": list})
}
