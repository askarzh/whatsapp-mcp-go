package main

import (
	"testing"
	"time"
)

// What Mindet's suite can only assert against its fake (it cannot seed a
// real bridge): arrival order across a restart, edits and tombstones on the
// wire, a pending voice note becoming a path after media.
func TestEndToEndArrivalOrderEditsAndMedia(t *testing.T) {
	srv, s, _ := newContractServer(t)
	fm := &fakeMedia{dir: contractDepsOf(srv).Cfg.MediaDownloadDir}
	setMedia(srv, fm)
	mustExec(t, s.db, `INSERT INTO chats (jid, name) VALUES ('77000000001@s.whatsapp.net', 'engineer')`)
	chat := "77000000001@s.whatsapp.net"
	late := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	_ = s.StoreMessage("fresh", chat, "77000000001", "new", time.Now().UTC(), false, "", "", "", nil, nil, nil, 0)
	_ = s.StoreMessage("late", chat, "77000000001", "old", late, false, "", "", "", nil, nil, nil, 0)
	_ = s.StoreMessage("v1", chat, "77000000001", "", time.Now().UTC(), false, "audio", "v1.ogg", "u", []byte{1}, []byte{2}, []byte{3}, 4)
	_ = s.StoreMessageKind("e1", chat, "77000000001", "can't today", time.Now().UTC(), false, "", "", "", nil, nil, nil, 0, "edit", "fresh")
	_ = s.StoreMessageKind("d1", chat, "77000000001", "", time.Now().UTC(), false, "", "", "", nil, nil, nil, 0, "tombstone", "late")

	var ids, kinds []string
	cursor := ""
	for {
		_, p := get(t, srv.URL+"/bridge/v1/messages?limit=2&since="+cursor, testToken)
		msgs := p["messages"].([]any)
		if len(msgs) == 0 {
			break
		}
		for _, x := range msgs {
			m := x.(map[string]any)
			ids = append(ids, m["native_id"].(string))
			kinds = append(kinds, m["kind"].(string))
		}
		cursor = p["next"].(string)
	}
	if want := []string{"fresh", "late", "v1", "e1", "d1"}; !equal(ids, want) {
		t.Fatalf("ids %v", ids)
	}
	if want := []string{"chat", "chat", "voice", "edit", "tombstone"}; !equal(kinds, want) {
		t.Fatalf("kinds %v", kinds)
	}
	_, before := get(t, srv.URL+"/bridge/v1/messages?limit=1&since="+encodeCursor(s.Generation(), 2), testToken)
	f := before["messages"].([]any)[0].(map[string]any)["files"].([]any)[0].(map[string]any)
	if f["path"] != nil || f["pending"] != true {
		t.Fatalf("voice note must be pending before media: %+v", f)
	}
	get(t, srv.URL+"/bridge/v1/media/v1?chat="+chat, testToken)
	_, after := get(t, srv.URL+"/bridge/v1/messages?limit=1&since="+encodeCursor(s.Generation(), 2), testToken)
	f = after["messages"].([]any)[0].(map[string]any)["files"].([]any)[0].(map[string]any)
	if f["path"] != "whatsapp/"+chat+"/v1.ogg" {
		t.Fatalf("voice note path after media: %+v", f)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
