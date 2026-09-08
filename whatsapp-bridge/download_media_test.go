package main

import (
	"errors"
	"net/http"
	"testing"

	"go.mau.fi/whatsmeow"
)

// TestClassifyDownloadErr pins which client.Download failures Mindet must
// treat as permanent (errMediaGone: a 410, never worth retrying) versus
// transient (left generic, retried as a 500). A CDN 403/404/410 and
// whatsmeow's own deterministic decrypt errors can never resolve on retry;
// everything else might.
func TestClassifyDownloadErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		gone bool
	}{
		{"cdn 403", whatsmeow.DownloadHTTPError{Response: &http.Response{StatusCode: 403}}, true},
		{"cdn 404", whatsmeow.DownloadHTTPError{Response: &http.Response{StatusCode: 404}}, true},
		{"cdn 410", whatsmeow.DownloadHTTPError{Response: &http.Response{StatusCode: 410}}, true},
		{"cdn 500 is not permanent", whatsmeow.DownloadHTTPError{Response: &http.Response{StatusCode: 500}}, false},
		{"no url present", whatsmeow.ErrNoURLPresent, true},
		{"invalid media sha256", whatsmeow.ErrInvalidMediaSHA256, true},
		{"invalid media hmac", whatsmeow.ErrInvalidMediaHMAC, true},
		{"too short file", whatsmeow.ErrTooShortFile, true},
		{"a network hiccup stays generic", errors.New("connection reset"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyDownloadErr(c.err)
			if errors.Is(got, errMediaGone) != c.gone {
				t.Fatalf("classifyDownloadErr(%v) = %v, want gone=%v", c.err, got, c.gone)
			}
			if errors.Is(got, errMediaUnknown) {
				t.Fatalf("classifyDownloadErr(%v) must never be errMediaUnknown", c.err)
			}
		})
	}
}
