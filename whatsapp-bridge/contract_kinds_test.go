package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

func mustWrite(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestChatKindFromJID(t *testing.T) {
	cases := map[string]string{
		"77000000001@s.whatsapp.net":    "direct",
		"1839000000000000@lid":          "direct",
		"120363000000000000@g.us":       "group",
		"120363000000000001@newsletter": "feed",
		"status@broadcast":              "feed",
		"1234567890@broadcast":          "feed",
	}
	for jid, want := range cases {
		if got := chatKind(jid); got != want {
			t.Errorf("%s: got %s want %s", jid, got, want)
		}
	}
}

func TestAuthorKeyOnlyFromAPhoneNumber(t *testing.T) {
	a := authorOf("77000000001", false, "77000000009@s.whatsapp.net")
	if a.Key == nil || *a.Key != "e164:+77000000001" || a.IsOwner {
		t.Fatalf("phone sender: %+v", a)
	}
	l := authorOf("1839000000000000@lid", false, "77000000009@s.whatsapp.net")
	if l.Key != nil || len(l.Raw) != 1 || l.Raw[0].Type != "wa_lid" {
		t.Fatalf("lid sender must have no key and one raw id: %+v", l)
	}
	me := authorOf("77000000009", true, "77000000009@s.whatsapp.net")
	if !me.IsOwner || me.NativeID != "77000000009@s.whatsapp.net" || me.Key == nil || *me.Key != "e164:+77000000009" {
		t.Fatalf("owner: %+v", me)
	}
}

// The form a live @lid message actually leaves in the store: whatsmeow hands
// us the lid JID, resolveSenderPN cannot map it to a phone, and the digits
// of a linked id are not a phone number. Keying them would put the boss's
// directives on a person who does not exist.
func TestAuthorNeverInventsAKeyForALinkedOrUnknownSender(t *testing.T) {
	live := authorOf("1839000000000000@lid", false, "77000000009@s.whatsapp.net")
	if live.Key != nil || len(live.Raw) != 1 || live.Raw[0].Type != "wa_lid" || live.Raw[0].Value != "1839000000000000@lid" {
		t.Fatalf("a live lid sender must carry no key and one raw id: %+v", live)
	}
	odd := authorOf("some-name", false, "77000000009@s.whatsapp.net")
	if odd.Key != nil || len(odd.Raw) != 1 || odd.Raw[0].Type != "wa_user" || odd.Raw[0].Value != "some-name" {
		t.Fatalf("an unrecognised sender must still carry a raw id: %+v", odd)
	}
	other := authorOf("120363000000000000@g.us", false, "77000000009@s.whatsapp.net")
	if other.Key != nil || len(other.Raw) != 1 {
		t.Fatalf("a non-phone server must not be keyed: %+v", other)
	}
	plain := authorOf("77000000001@s.whatsapp.net", false, "77000000009@s.whatsapp.net")
	if plain.Key == nil || *plain.Key != "e164:+77000000001" {
		t.Fatalf("a full phone JID is still a phone number: %+v", plain)
	}
}

// /bridge/v1 is served whether or not there is a WhatsApp session, so
// OwnerJID() can be empty. "e164:+" is not an identity.
func TestOwnerWithoutAJIDGetsNoKey(t *testing.T) {
	none := authorOf("", true, "")
	if none.Key != nil || !none.IsOwner || none.NativeID != "" {
		t.Fatalf("logged-out owner: %+v", none)
	}
	lid := authorOf("", true, "1839000000000000@lid")
	if lid.Key != nil || !lid.IsOwner || lid.NativeID != "1839000000000000@lid" {
		t.Fatalf("lid-first owner: %+v", lid)
	}
}

// What handleMessage writes for a sender still hidden behind a linked id:
// the full JID, never the bare digits.
func TestStoredSenderKeepsTheLidSuffix(t *testing.T) {
	lid := types.JID{User: "1839000000000000", Server: types.HiddenUserServer}
	pn := types.JID{User: "77000000001", Server: types.DefaultUserServer}
	info := types.MessageInfo{MessageSource: types.MessageSource{Sender: lid}}
	if got := storedSender(info, lid); got != "1839000000000000@lid" {
		t.Fatalf("unmapped lid sender stored as %q", got)
	}
	if got := storedSender(info, pn); got != "77000000001" {
		t.Fatalf("resolved phone sender stored as %q", got)
	}
	if got := storedSender(info, types.JID{}); got != "1839000000000000@lid" {
		t.Fatalf("empty resolution must fall back to the delivered sender, got %q", got)
	}
	// and the round trip: what we store is what authorOf refuses to key
	if a := authorOf(storedSender(info, lid), false, ""); a.Key != nil {
		t.Fatalf("stored lid sender got a key: %+v", a)
	}
}

func TestToContractMessageShapesFilesAndTimes(t *testing.T) {
	sent := time.Date(2026, 9, 7, 6, 40, 11, 0, time.UTC)
	m := ArrivedMessage{Seq: 3, ArrivedAt: sent, ID: "3AC2", ChatJID: "77000000001@s.whatsapp.net", Sender: "77000000001",
		Timestamp: sent, MediaType: "audio", Filename: "audio_20260907_064011.ogg", FileSHA256: []byte{0xab, 0xcd}, Kind: "voice"}
	dir := t.TempDir()
	out := toContractMessage(m, "77000000009@s.whatsapp.net", dir, dir+"/whatsapp")
	if out.Kind != "voice" || out.SentAt != "2026-09-07T06:40:11.000000+00:00" || out.Chat != m.ChatJID {
		t.Fatalf("%+v", out)
	}
	if len(out.Files) != 1 || out.Files[0].Path != nil || !out.Files[0].Pending || out.Files[0].Role != "voice" || *out.Files[0].SHA256 != "abcd" {
		t.Fatalf("pending file wrong: %+v", out.Files[0])
	}
	// once the file exists under the download dir, the path is relative to the shared root
	mustWrite(t, dir+"/whatsapp/77000000001@s.whatsapp.net/audio_20260907_064011.ogg")
	out = toContractMessage(m, "77000000009@s.whatsapp.net", dir, dir+"/whatsapp")
	if out.Files[0].Path == nil || *out.Files[0].Path != "whatsapp/77000000001@s.whatsapp.net/audio_20260907_064011.ogg" || out.Files[0].Pending {
		t.Fatalf("present file wrong: %+v", out.Files[0])
	}
	if out.Files[0].Mime != "audio/ogg" {
		t.Fatalf("mime: %s", out.Files[0].Mime)
	}
	e := toContractMessage(ArrivedMessage{ID: "e1", ChatJID: m.ChatJID, Sender: m.Sender, Timestamp: sent, Kind: "edit", Edits: "3AC2", Content: "can't today"},
		"77000000009@s.whatsapp.net", dir, dir+"/whatsapp")
	if e.Kind != "edit" || e.Edits == nil || *e.Edits != "3AC2" || e.EditedAt == nil {
		t.Fatalf("edit: %+v", e)
	}
}

// A media message stored before its filename was known must still show a
// file: no path, pending true. Reporting no files at all would hide it from
// Mindet, which would then never ask /media for it.
func TestMediaWithoutAFilenameIsStillReportedAsPending(t *testing.T) {
	dir := t.TempDir()
	m := ArrivedMessage{ID: "v2", ChatJID: "77000000001@s.whatsapp.net", Sender: "77000000001",
		Timestamp: time.Date(2026, 9, 7, 6, 40, 11, 0, time.UTC), MediaType: "audio", Kind: "voice"}
	out := toContractMessage(m, "77000000009@s.whatsapp.net", dir, dir+"/whatsapp")
	if len(out.Files) != 1 {
		t.Fatalf("a media row with no filename must still report a file: %+v", out.Files)
	}
	if out.Files[0].Path != nil || !out.Files[0].Pending || out.Files[0].Role != "voice" {
		t.Fatalf("%+v", out.Files[0])
	}
	// a message with no media at all still reports no files
	plain := toContractMessage(ArrivedMessage{ID: "t1", ChatJID: m.ChatJID, Sender: m.Sender,
		Timestamp: m.Timestamp, Content: "hi", Kind: "chat"}, "77000000009@s.whatsapp.net", dir, dir+"/whatsapp")
	if len(plain.Files) != 0 {
		t.Fatalf("text message: %+v", plain.Files)
	}
}

func TestClassifyProtocolRevokeAndEdit(t *testing.T) {
	revoke := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waCommon.MessageKey{ID: proto.String("m1")}}}
	kind, edits, text, ok := classifyProtocol(revoke)
	if !ok || kind != "tombstone" || edits != "m1" || text != "" {
		t.Fatalf("revoke: %s %s %q %v", kind, edits, text, ok)
	}
	edit := &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: &waCommon.MessageKey{ID: proto.String("m2")},
		EditedMessage: &waE2E.Message{Conversation: proto.String("can't today")}}}
	kind, edits, text, ok = classifyProtocol(edit)
	if !ok || kind != "edit" || edits != "m2" || text != "can't today" {
		t.Fatalf("edit: %s %s %q %v", kind, edits, text, ok)
	}
	if _, _, _, ok := classifyProtocol(&waE2E.Message{Conversation: proto.String("hi")}); ok {
		t.Fatal("a plain message is not a correction")
	}
}
