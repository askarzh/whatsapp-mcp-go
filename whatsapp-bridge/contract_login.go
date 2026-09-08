package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"whatsapp-bridge/wastate"
)

// The login challenge of spec §3.7a for WhatsApp: a link to a page that shows
// the live QR (the code expires every ~20 s, so the page refreshes it), or a
// pairing code for a phone number when the phone is the only screen.

type phonePairer interface {
	PairPhone(ctx context.Context, phone string) (string, error)
}

type loginDisplay struct {
	Kind string `json:"kind"`
	URL  string `json:"url,omitempty"`
	Code string `json:"code,omitempty"`
	// ImagePNG is always nil for WhatsApp: the PNG is served from
	// /bridge/v1/qr/{token}/png, never inlined. The key stays present (and
	// null) so the emitted shape matches spec §15's display object exactly.
	ImagePNG  *string `json:"image_png_base64"`
	ExpiresAt string  `json:"expires_at,omitempty"`
}

type loginChallenge struct {
	Prompt    string        `json:"prompt"`
	InputType string        `json:"input_type"`
	Display   *loginDisplay `json:"display"`
}

type loginReply struct {
	SessionID string          `json:"session_id"`
	Status    string          `json:"status"`
	Challenge *loginChallenge `json:"challenge,omitempty"`
	Detail    string          `json:"detail,omitempty"`
}

type loginSession struct {
	token   string
	created time.Time
}

const qrLinkTTL = 10 * time.Minute

type loginFlow struct {
	state     *wastate.State
	pairer    phonePairer
	publicURL string
	now       func() time.Time
	mu        sync.Mutex
	sessions  map[string]*loginSession
}

func newLoginFlow(state *wastate.State, pairer phonePairer, publicURL string) *loginFlow {
	return &loginFlow{state: state, pairer: pairer, publicURL: strings.TrimRight(publicURL, "/"),
		now: time.Now, sessions: map[string]*loginSession{}}
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// noSessionDetail is the truth when the QR loop is not running: whatsmeow
// opens it once, at startup, only when there is no session — so a logout at
// runtime leaves nothing to scan and no way to pair from chat. Saying that
// beats handing the owner a challenge with nothing in it, which is a prompt
// that leads nowhere.
const noSessionDetail = "this bridge lost its session and needs a restart before it can be paired"

// challenge is nil when there is no QR to offer: see noSessionDetail.
func (l *loginFlow) challenge(sess *loginSession) *loginChallenge {
	if l.state.PairingQRPNG() == nil {
		return nil
	}
	return &loginChallenge{
		Prompt:    "Open the link on any screen that is not the phone and scan it in WhatsApp › Linked devices. Or reply with the phone number to get a pairing code instead.",
		InputType: "text",
		Display: &loginDisplay{Kind: "qr_link",
			URL:       fmt.Sprintf("%s/bridge/v1/qr/%s", l.publicURL, sess.token),
			ExpiresAt: fmtTime(sess.created.Add(qrLinkTTL))},
	}
}

// evictExpiredLocked drops sessions whose QR link has expired. Called with
// l.mu held: a login flow that runs for weeks must not accumulate one map
// entry per attempt forever.
func (l *loginFlow) evictExpiredLocked() {
	now := l.now()
	for id, s := range l.sessions {
		if !now.Before(s.created.Add(qrLinkTTL)) {
			delete(l.sessions, id)
		}
	}
}

// Step is one round of the challenge-response. The bridge dictates the step,
// Mindet relays it; nothing about WhatsApp leaks into Mindet.
//
// The lock is held only to resolve/create the session and read what
// challenge() needs; it is released before PairPhone, a network call that
// can take tens of seconds, so a slow pairing attempt never blocks the QR
// page's /status and /png polls or a concurrent /login call.
func (l *loginFlow) Step(sessionID *string, response *string) loginReply {
	l.mu.Lock()
	l.evictExpiredLocked()
	if l.state.LoggedIn() {
		id := ""
		if sessionID != nil {
			id = *sessionID
		}
		l.mu.Unlock()
		return loginReply{SessionID: id, Status: "authenticated", Detail: "WhatsApp session is active"}
	}
	var sess *loginSession
	var id string
	if sessionID == nil || *sessionID == "" {
		id = randomToken()
		sess = &loginSession{token: randomToken(), created: l.now()}
		l.sessions[id] = sess
	} else {
		id = *sessionID
		sess = l.sessions[id]
		if sess == nil {
			l.mu.Unlock()
			return loginReply{SessionID: id, Status: "failed", Detail: "unknown login session"}
		}
	}
	if response != nil && *response != "" {
		phone := digitsOnly(*response)
		if len(phone) < 8 {
			c := l.challenge(sess)
			l.mu.Unlock()
			if c == nil {
				return loginReply{SessionID: id, Status: "failed", Detail: noSessionDetail}
			}
			return loginReply{SessionID: id, Status: "in_progress", Challenge: c,
				Detail: "that does not look like a phone number"}
		}
		l.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		code, err := l.pairer.PairPhone(ctx, phone)
		if err != nil {
			// PairPhone needs a connected client; after a runtime logout
			// there is none, and no amount of retrying from chat will make
			// one. The owner needs the same restart the QR path needs.
			slog.Warn("contract login: pairing code unavailable", "err", err)
			return loginReply{SessionID: id, Status: "failed", Detail: noSessionDetail}
		}
		return loginReply{SessionID: id, Status: "in_progress", Challenge: &loginChallenge{
			Prompt:    "In WhatsApp on the phone: Linked devices › Link a device › Link with phone number instead, then type this code.",
			InputType: "none",
			Display:   &loginDisplay{Kind: "pairing_code", Code: code, ExpiresAt: fmtTime(l.now().Add(3 * time.Minute))},
		}}
	}
	c := l.challenge(sess)
	l.mu.Unlock()
	if c == nil {
		return loginReply{SessionID: id, Status: "failed", Detail: noSessionDetail}
	}
	return loginReply{SessionID: id, Status: "in_progress", Challenge: c}
}

func (l *loginFlow) sessionByToken(token string) *loginSession {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.sessions {
		if s.token == token && l.now().Before(s.created.Add(qrLinkTTL)) {
			return s
		}
	}
	return nil
}

func (l *loginFlow) pngFor(token string) ([]byte, int) {
	if l.sessionByToken(token) == nil {
		return nil, http.StatusGone
	}
	png := l.state.PairingQRPNG()
	if png == nil {
		return nil, http.StatusNoContent
	}
	return png, http.StatusOK
}

const qrPageHTML = `<!doctype html><meta charset="utf-8"><title>Link WhatsApp</title>
<style>body{font-family:system-ui;text-align:center;padding:2rem}img{width:288px;height:288px}</style>
<h1>Scan in WhatsApp &rsaquo; Linked devices</h1><img id="qr" alt="QR"><p id="s">waiting for a code&hellip;</p>
<script>
async function tick(){const r=await fetch(location.pathname+'/status');const j=await r.json();
 if(j.paired){document.getElementById('s').textContent='Paired. You can close this page.';return;}
 if(j.expired){document.getElementById('s').textContent='This link has expired. Ask for a new one in chat.';return;}
 document.getElementById('qr').src=location.pathname+'/png?'+Date.now();
 document.getElementById('s').textContent='The code refreshes on its own.';setTimeout(tick,10000);}
tick();
</script>`

func (l *loginFlow) QRPage(w http.ResponseWriter, r *http.Request) {
	if l.sessionByToken(r.PathValue("token")) == nil {
		http.Error(w, "this link has expired", http.StatusGone)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(qrPageHTML))
}

func (l *loginFlow) QRPNG(w http.ResponseWriter, r *http.Request) {
	png, status := l.pngFor(r.PathValue("token"))
	if status != http.StatusOK {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

// QRStatus reports "paired" only for a token that is still live: an unknown
// or expired token was never a promise the bridge is now honoring, so it
// answers not-paired/expired regardless of the WhatsApp session's own state.
func (l *loginFlow) QRStatus(w http.ResponseWriter, r *http.Request) {
	if l.sessionByToken(r.PathValue("token")) == nil {
		writeJSON(w, 200, map[string]any{"paired": false, "expired": true})
		return
	}
	writeJSON(w, 200, map[string]any{"paired": l.state.LoggedIn(), "expired": false})
}
