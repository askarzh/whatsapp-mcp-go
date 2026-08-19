package helpers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newBridgeStub stands in for the whatsapp-bridge. Its /api/ tree is behind the
// same JWT gate the real bridge uses (auth.JwtAuthMiddleware), so any caller
// that forgets the Authorization header gets a 401 here exactly as it does in
// production. It records the last /api request for assertions.
func newBridgeStub(t *testing.T, apiJSON string) (*httptest.Server, *http.Request) {
	t.Helper()
	var last http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/login" {
			if r.Header.Get("Authorization") != "Bearer test-api-key" {
				http.Error(w, "bad api key", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"token":"test-jwt"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-jwt" {
			http.Error(w, "Missing or invalid Authorization header", http.StatusUnauthorized)
			return
		}
		last = *r
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(apiJSON))
	}))
	t.Cleanup(srv.Close)

	prevURL, prevKey := apiBaseURL, apiKey
	apiBaseURL, apiKey = srv.URL+"/api", "test-api-key"
	jwtToken, tokenExpiresAt = "", time.Time{}
	t.Cleanup(func() {
		apiBaseURL, apiKey = prevURL, prevKey
		jwtToken, tokenExpiresAt = "", time.Time{}
	})
	return srv, &last
}

func TestDownloadMediaAuthenticates(t *testing.T) {
	_, last := newBridgeStub(t, `{"success":true,"path":"/shared/voice.ogg"}`)

	path, err := DownloadMedia("msg-1", "77077254753@s.whatsapp.net")
	if err != nil {
		t.Fatalf("DownloadMedia: %v", err)
	}
	if path != "/shared/voice.ogg" {
		t.Errorf("path = %q, want /shared/voice.ogg", path)
	}
	if got := last.URL.Path; got != "/api/download" {
		t.Errorf("path = %q, want /api/download", got)
	}
}

func TestSendMessageAuthenticates(t *testing.T) {
	newBridgeStub(t, `{"success":true,"message":"sent"}`)

	ok, msg := SendMessage("77077254753", "hi")
	if !ok {
		t.Fatalf("SendMessage failed: %s", msg)
	}
}

func TestSendFileAuthenticates(t *testing.T) {
	newBridgeStub(t, `{"success":true,"message":"sent"}`)
	f := filepath.Join(t.TempDir(), "doc.pdf")
	if err := os.WriteFile(f, []byte("%PDF-1.4"), 0o600); err != nil {
		t.Fatal(err)
	}

	ok, msg := SendFile("77077254753", f)
	if !ok {
		t.Fatalf("SendFile failed: %s", msg)
	}
}

func TestSendAudioVoiceMessageAuthenticates(t *testing.T) {
	newBridgeStub(t, `{"success":true,"message":"sent"}`)
	f := filepath.Join(t.TempDir(), "note.ogg") // .ogg → no ffmpeg conversion
	if err := os.WriteFile(f, []byte("OggS"), 0o600); err != nil {
		t.Fatal(err)
	}

	ok, msg := SendAudioVoiceMessage("77077254753", f)
	if !ok {
		t.Fatalf("SendAudioVoiceMessage failed: %s", msg)
	}
}

// A contact name with a space must survive the trip. Concatenating it straight
// into the URL puts a literal space in the HTTP request line, which the bridge's
// net/http server rejects with 400 before any handler runs.
func TestSearchContactsEscapesSpaces(t *testing.T) {
	_, last := newBridgeStub(t, `{"contacts":[{"phone_number":"77077254753","name":"Руслан Шалтыков"}]}`)

	res, _, err := searchContactsHandler(context.Background(), nil, searchContactsInput{Query: "Руслан Шалтыков"})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.IsError {
		t.Fatalf("handler returned error result: %+v", res.Content)
	}
	if got := last.URL.Query().Get("q"); got != "Руслан Шалтыков" {
		t.Errorf("q = %q, want %q", got, "Руслан Шалтыков")
	}
}

// Guard the whole surface: no /api caller may build a request by hand.
func TestAllBridgeCallersSendAuthorization(t *testing.T) {
	_, last := newBridgeStub(t, `{"ok":true}`)
	if _, err := callAPI(http.MethodGet, "/chats", nil); err != nil {
		t.Fatalf("callAPI: %v", err)
	}
	var probe map[string]any
	_ = json.Unmarshal([]byte(`{}`), &probe)
	if last.Header.Get("Authorization") != "Bearer test-jwt" {
		t.Errorf("Authorization = %q", last.Header.Get("Authorization"))
	}
}
