package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"whatsapp-bridge/wastate"
)

type fakePairer struct {
	phone string
	fail  bool
}

func (f *fakePairer) PairPhone(ctx context.Context, phone string) (string, error) {
	f.phone = phone
	if f.fail {
		return "", errors.New("not connected")
	}
	return "ABCD-EFGH", nil
}

func TestLoginOffersAQRLinkThenAPairingCodeThenAuthenticates(t *testing.T) {
	st := wastate.New()
	st.SetPairingQRPNG([]byte("png"))
	st.SetPairingQRCode("2@qr")
	fp := &fakePairer{}
	l := newLoginFlow(st, fp, "https://wa.example")
	first := l.Step(nil, nil)
	if first.Status != "in_progress" || first.SessionID == "" || first.Challenge == nil ||
		first.Challenge.Display == nil || first.Challenge.Display.Kind != "qr_link" ||
		!strings.HasPrefix(first.Challenge.Display.URL, "https://wa.example/bridge/v1/qr/") ||
		first.Challenge.InputType != "text" {
		t.Fatalf("first step: %+v", first)
	}
	phone := "+7 700 000 00 01"
	second := l.Step(&first.SessionID, &phone)
	if second.Status != "in_progress" || second.Challenge.Display.Kind != "pairing_code" || second.Challenge.Display.Code != "ABCD-EFGH" || fp.phone != "77000000001" {
		t.Fatalf("second step: %+v (pairer got %q)", second, fp.phone)
	}
	st.SetLoggedIn(true)
	third := l.Step(&first.SessionID, nil)
	if third.Status != "authenticated" || third.Challenge != nil {
		t.Fatalf("third step: %+v", third)
	}
	if l.Step(nil, nil).Status != "authenticated" {
		t.Fatal("a fresh session while logged in is already authenticated")
	}
}

func TestLoginFailsCleanlyWhenPairingIsImpossible(t *testing.T) {
	st := wastate.New()
	l := newLoginFlow(st, &fakePairer{fail: true}, "https://wa.example")
	first := l.Step(nil, nil)
	if first.Status != "in_progress" || first.Challenge.Display != nil {
		t.Fatalf("with no QR yet the display is empty but the prompt still asks for a phone: %+v", first)
	}
	phone := "77000000001"
	r := l.Step(&first.SessionID, &phone)
	if r.Status != "failed" || r.Detail == "" {
		t.Fatalf("%+v", r)
	}
	unknown := "nope"
	if l.Step(&unknown, nil).Status != "failed" {
		t.Fatal("unknown session must fail")
	}
}

func TestQRLinkTokenExpiresAndServesPNG(t *testing.T) {
	st := wastate.New()
	st.SetPairingQRPNG([]byte("png-bytes"))
	l := newLoginFlow(st, &fakePairer{}, "https://wa.example")
	now := time.Now()
	l.now = func() time.Time { return now }
	first := l.Step(nil, nil)
	token := strings.TrimPrefix(first.Challenge.Display.URL, "https://wa.example/bridge/v1/qr/")
	if len(token) < 32 {
		t.Fatalf("token too short: %q", token)
	}
	if body, status := l.pngFor(token); status != 200 || string(body) != "png-bytes" {
		t.Fatalf("png: %d", status)
	}
	l.now = func() time.Time { return now.Add(11 * time.Minute) }
	if _, status := l.pngFor(token); status != 410 {
		t.Fatalf("expired token must be 410, got %d", status)
	}
}
