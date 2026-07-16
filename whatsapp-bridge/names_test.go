package main

import (
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
)

func TestNameHintForChat(t *testing.T) {
	// our own messages must never name the peer's chat after us
	if got := nameHintForChat(true, "42679257804940"); got != "" {
		t.Errorf("nameHintForChat(fromMe) = %q, want empty", got)
	}
	if got := nameHintForChat(false, "77001234567"); got != "77001234567" {
		t.Errorf("nameHintForChat(incoming) = %q, want sender", got)
	}
}

func TestRepairSelfNamedChats(t *testing.T) {
	ms := newTestMessageStore(t)
	ts := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	ownPN := "77089990000"
	ownLID := "42679257804940"

	// poisoned: peer chat named with the owner's lid
	mustExec(t, ms.db, "INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		"77089043649@s.whatsapp.net", ownLID, ts)
	// poisoned: peer chat named with the owner's phone number, contact known
	mustExec(t, ms.db, "INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		"77015081776@s.whatsapp.net", ownPN, ts)
	// healthy: real name must be untouched
	mustExec(t, ms.db, "INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		"77471998610@s.whatsapp.net", "Жандос", ts)
	// group whose name happens to collide is out of scope
	mustExec(t, ms.db, "INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		"1234-567@g.us", ownLID, ts)

	nameFor := func(jid types.JID) string {
		if jid.User == "77015081776" {
			return "Жандос Асылжанов"
		}
		return ""
	}

	repaired, err := repairSelfNamedChats(ms, []string{ownPN, ownLID}, nameFor)
	if err != nil {
		t.Fatalf("repairSelfNamedChats: %v", err)
	}
	if repaired != 2 {
		t.Errorf("repaired = %d, want 2", repaired)
	}

	assertName := func(jid, want string) {
		t.Helper()
		var name string
		if err := ms.db.QueryRow("SELECT name FROM chats WHERE jid = ?", jid).Scan(&name); err != nil {
			t.Fatalf("chat %s: %v", jid, err)
		}
		if name != want {
			t.Errorf("chat %s name = %q, want %q", jid, name, want)
		}
	}

	// no contact name known -> fall back to the peer's own number
	assertName("77089043649@s.whatsapp.net", "77089043649")
	// contact store resolves a real name
	assertName("77015081776@s.whatsapp.net", "Жандос Асылжанов")
	assertName("77471998610@s.whatsapp.net", "Жандос")
	assertName("1234-567@g.us", ownLID)
}

func TestRepairSelfNamedChats_NoSelfIDs(t *testing.T) {
	ms := newTestMessageStore(t)
	if n, err := repairSelfNamedChats(ms, nil, nil); err != nil || n != 0 {
		t.Errorf("repairSelfNamedChats(nil ids) = (%d, %v), want (0, nil)", n, err)
	}
}
