package main

import "testing"

// whatsmeow's DownloadMediaWithPath builds the CDN URL as
//
//	https://<host><directPath>&hash=...&mms-type=...&__wa-mms=
//
// appending with '&', so directPath MUST still carry the signed query WhatsApp
// issued (?ccb/oh/oe/_nc_sid). Dropping it leaves a URL with no query at all —
// no oh/oe signature — and the CDN answers 403.
func TestExtractDirectPathKeepsSignedQuery(t *testing.T) {
	const url = "https://mmg.whatsapp.net/v/t62.7117-24/778402454_1543466654222208_1181020700691390197_n.enc" +
		"?ccb=11-4&oh=01_Q5Aa5QGhp15n7heSUS8gPPCS7QCB_ehfkoc_m58dst6BaR9NXw&oe=6AAB71FA&_nc_sid=5e03e0&mms3=true"
	const want = "/v/t62.7117-24/778402454_1543466654222208_1181020700691390197_n.enc" +
		"?ccb=11-4&oh=01_Q5Aa5QGhp15n7heSUS8gPPCS7QCB_ehfkoc_m58dst6BaR9NXw&oe=6AAB71FA&_nc_sid=5e03e0&mms3=true"

	if got := extractDirectPathFromURL(url); got != want {
		t.Errorf("extractDirectPathFromURL()\n got = %q\nwant = %q", got, want)
	}
}

func TestExtractDirectPathStartsWithSlash(t *testing.T) {
	// whatsmeow rejects a path that does not start with "/".
	if got := extractDirectPathFromURL("https://mmg.whatsapp.net/v/x.enc?a=1"); got[0] != '/' {
		t.Errorf("got %q, want leading slash", got)
	}
}
