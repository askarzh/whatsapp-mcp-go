package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
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
	Kind      string `json:"kind"`
	URL       string `json:"url,omitempty"`
	Code      string `json:"code,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
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

func (l *loginFlow) challenge(sess *loginSession) *loginChallenge {
	c := &loginChallenge{
		Prompt:    "Open the link on any screen that is not the phone and scan it in WhatsApp › Linked devices. Or reply with the phone number to get a pairing code instead.",
		InputType: "text",
	}
	if l.state.PairingQRPNG() != nil {
		c.Display = &loginDisplay{Kind: "qr_link",
			URL:       fmt.Sprintf("%s/bridge/v1/qr/%s", l.publicURL, sess.token),
			ExpiresAt: fmtTime(sess.created.Add(qrLinkTTL))}
	}
	return c
}

// Step is one round of the challenge-response. The bridge dictates the step,
// Mindet relays it; nothing about WhatsApp leaks into Mindet.
func (l *loginFlow) Step(sessionID *string, response *string) loginReply {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state.LoggedIn() {
		id := ""
		if sessionID != nil {
			id = *sessionID
		}
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
			return loginReply{SessionID: id, Status: "failed", Detail: "unknown login session"}
		}
	}
	if response != nil && *response != "" {
		phone := digitsOnly(*response)
		if len(phone) < 8 {
			return loginReply{SessionID: id, Status: "in_progress", Challenge: l.challenge(sess),
				Detail: "that does not look like a phone number"}
		}
		code, err := l.pairer.PairPhone(context.Background(), phone)
		if err != nil {
			return loginReply{SessionID: id, Status: "failed", Detail: "pairing code unavailable: " + err.Error()}
		}
		return loginReply{SessionID: id, Status: "in_progress", Challenge: &loginChallenge{
			Prompt:    "In WhatsApp on the phone: Linked devices › Link a device › Link with phone number instead, then type this code.",
			InputType: "none",
			Display:   &loginDisplay{Kind: "pairing_code", Code: code, ExpiresAt: fmtTime(l.now().Add(3 * time.Minute))},
		}}
	}
	return loginReply{SessionID: id, Status: "in_progress", Challenge: l.challenge(sess)}
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

func (l *loginFlow) QRStatus(w http.ResponseWriter, r *http.Request) {
	expired := l.sessionByToken(r.PathValue("token")) == nil
	writeJSON(w, 200, map[string]any{"paired": l.state.LoggedIn(), "expired": expired})
}
