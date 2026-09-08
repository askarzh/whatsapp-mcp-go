package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
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
	if !me.IsOwner || me.NativeID != "77000000009@s.whatsapp.net" {
		t.Fatalf("owner: %+v", me)
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
