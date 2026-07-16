package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

func newTestMessageStore(t *testing.T) *MessageStore {
	t.Helper()
	db, err := sql.Open("sqlite3", "file::memory:?_foreign_keys=on")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := createTables(db); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	return &MessageStore{db: db}
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func lidMapping(lid, pn string) store.LIDMapping {
	return store.LIDMapping{
		LID: types.NewJID(lid, types.HiddenUserServer),
		PN:  types.NewJID(pn, types.DefaultUserServer),
	}
}

func TestApplyLIDMappings_RekeysChatAndMessages(t *testing.T) {
	ms := newTestMessageStore(t)
	lidJID := "191813407211689@lid"
	pnJID := "77001234567@s.whatsapp.net"

	ts := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	mustExec(t, ms.db, "INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		lidJID, "191813407211689", ts)
	mustExec(t, ms.db,
		"INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me) VALUES (?, ?, ?, ?, ?, ?)",
		"m1", lidJID, "191813407211689", "hello", ts, false)

	err := applyLIDMappings(ms, []store.LIDMapping{lidMapping("191813407211689", "77001234567")},
		func(pn types.JID) string { return "Aidos Q" })
	if err != nil {
		t.Fatalf("applyLIDMappings: %v", err)
	}

	// lid chat is gone, pn chat exists with resolved name
	var name string
	if err := ms.db.QueryRow("SELECT name FROM chats WHERE jid = ?", pnJID).Scan(&name); err != nil {
		t.Fatalf("pn chat not found: %v", err)
	}
	if name != "Aidos Q" {
		t.Errorf("chat name = %q, want %q", name, "Aidos Q")
	}
	var n int
	if err := ms.db.QueryRow("SELECT COUNT(*) FROM chats WHERE jid = ?", lidJID).Scan(&n); err != nil || n != 0 {
		t.Errorf("lid chat still present (count=%d, err=%v)", n, err)
	}

	// message re-keyed and sender rewritten to the phone number
	var sender, chatJID string
	if err := ms.db.QueryRow("SELECT sender, chat_jid FROM messages WHERE id = 'm1'").Scan(&sender, &chatJID); err != nil {
		t.Fatalf("message not found: %v", err)
	}
	if chatJID != pnJID {
		t.Errorf("message chat_jid = %q, want %q", chatJID, pnJID)
	}
	if sender != "77001234567" {
		t.Errorf("message sender = %q, want %q", sender, "77001234567")
	}
}

func TestApplyLIDMappings_MergesIntoExistingPNChat(t *testing.T) {
	ms := newTestMessageStore(t)
	lidJID := "191813407211689@lid"
	pnJID := "77001234567@s.whatsapp.net"

	older := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	mustExec(t, ms.db, "INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		pnJID, "Real Name", older)
	mustExec(t, ms.db, "INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		lidJID, "191813407211689", newer)

	// duplicate message id under both keys, plus one unique to the lid chat
	mustExec(t, ms.db,
		"INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me) VALUES (?, ?, ?, ?, ?, ?)",
		"dup", pnJID, "77001234567", "old copy", older, false)
	mustExec(t, ms.db,
		"INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me) VALUES (?, ?, ?, ?, ?, ?)",
		"dup", lidJID, "191813407211689", "lid copy", older, false)
	mustExec(t, ms.db,
		"INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me) VALUES (?, ?, ?, ?, ?, ?)",
		"uniq", lidJID, "191813407211689", "only in lid", newer, false)

	err := applyLIDMappings(ms, []store.LIDMapping{lidMapping("191813407211689", "77001234567")},
		func(pn types.JID) string { return "" })
	if err != nil {
		t.Fatalf("applyLIDMappings: %v", err)
	}

	// single chat left, existing real name kept, newest last_message_time wins
	var count int
	if err := ms.db.QueryRow("SELECT COUNT(*) FROM chats").Scan(&count); err != nil || count != 1 {
		t.Fatalf("chat count = %d, want 1 (err=%v)", count, err)
	}
	var name string
	var lmt time.Time
	if err := ms.db.QueryRow("SELECT name, last_message_time FROM chats WHERE jid = ?", pnJID).Scan(&name, &lmt); err != nil {
		t.Fatalf("pn chat not found: %v", err)
	}
	if name != "Real Name" {
		t.Errorf("chat name = %q, want %q", name, "Real Name")
	}
	if !lmt.Equal(newer) {
		t.Errorf("last_message_time = %v, want %v", lmt, newer)
	}

	// no duplicate PK rows; all messages under the pn chat
	var msgCount int
	if err := ms.db.QueryRow("SELECT COUNT(*) FROM messages WHERE chat_jid = ?", pnJID).Scan(&msgCount); err != nil || msgCount != 2 {
		t.Fatalf("pn message count = %d, want 2 (err=%v)", msgCount, err)
	}
	if err := ms.db.QueryRow("SELECT COUNT(*) FROM messages WHERE chat_jid = ?", lidJID).Scan(&msgCount); err != nil || msgCount != 0 {
		t.Errorf("lid message count = %d, want 0 (err=%v)", msgCount, err)
	}
}

func TestApplyLIDMappings_RewritesSuffixedHistorySenders(t *testing.T) {
	ms := newTestMessageStore(t)
	groupJID := "1234-5678@g.us"
	ts := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	mustExec(t, ms.db, "INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		groupJID, "Work Group", ts)
	// history sync stores participant senders with the full @lid suffix
	mustExec(t, ms.db,
		"INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me) VALUES (?, ?, ?, ?, ?, ?)",
		"g1", groupJID, "191813407211689@lid", "from group", ts, false)

	err := applyLIDMappings(ms, []store.LIDMapping{lidMapping("191813407211689", "77001234567")},
		func(pn types.JID) string { return "" })
	if err != nil {
		t.Fatalf("applyLIDMappings: %v", err)
	}

	var sender string
	if err := ms.db.QueryRow("SELECT sender FROM messages WHERE id = 'g1'").Scan(&sender); err != nil {
		t.Fatalf("message not found: %v", err)
	}
	if sender != "77001234567" {
		t.Errorf("sender = %q, want %q", sender, "77001234567")
	}
}

func TestApplyLIDMappings_FallsBackToPNUserWhenNoName(t *testing.T) {
	ms := newTestMessageStore(t)
	lidJID := "191813407211689@lid"
	pnJID := "77001234567@s.whatsapp.net"
	ts := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	mustExec(t, ms.db, "INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		lidJID, "191813407211689", ts)

	err := applyLIDMappings(ms, []store.LIDMapping{lidMapping("191813407211689", "77001234567")},
		func(pn types.JID) string { return "" })
	if err != nil {
		t.Fatalf("applyLIDMappings: %v", err)
	}

	var name string
	if err := ms.db.QueryRow("SELECT name FROM chats WHERE jid = ?", pnJID).Scan(&name); err != nil {
		t.Fatalf("pn chat not found: %v", err)
	}
	if name != "77001234567" {
		t.Errorf("chat name = %q, want phone fallback %q", name, "77001234567")
	}
}

// createWhatsmeowFixtureTables creates minimal copies of the whatsmeow store
// tables that SearchContacts queries (shared-DB deployments only).
func createWhatsmeowFixtureTables(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `CREATE TABLE whatsmeow_contacts (
		our_jid TEXT, their_jid TEXT, first_name TEXT, full_name TEXT,
		push_name TEXT, business_name TEXT,
		PRIMARY KEY (our_jid, their_jid)
	)`)
	mustExec(t, db, `CREATE TABLE whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT)`)
}

func TestSearchContacts_ResolvesLIDToPhoneNumber(t *testing.T) {
	ms := newTestMessageStore(t)
	createWhatsmeowFixtureTables(t, ms.db)

	mustExec(t, ms.db,
		"INSERT INTO whatsmeow_contacts (our_jid, their_jid, first_name, full_name, push_name) VALUES (?, ?, ?, ?, ?)",
		"me", "191813407211689@lid", "", "Aidos Qurmanov", "aidos")
	mustExec(t, ms.db, "INSERT INTO whatsmeow_lid_map (lid, pn) VALUES (?, ?)",
		"191813407211689", "77001234567")

	contacts, err := ms.SearchContacts("Aidos")
	if err != nil {
		t.Fatalf("SearchContacts: %v", err)
	}
	if len(contacts) != 1 {
		t.Fatalf("got %d contacts, want 1: %+v", len(contacts), contacts)
	}
	c := contacts[0]
	if c.PhoneNumber != "77001234567" {
		t.Errorf("PhoneNumber = %q, want real number %q", c.PhoneNumber, "77001234567")
	}
	if c.Name != "Aidos Qurmanov" {
		t.Errorf("Name = %q, want full name %q", c.Name, "Aidos Qurmanov")
	}
}

func TestSearchContacts_DedupesLIDAndPNRowsOfSamePerson(t *testing.T) {
	ms := newTestMessageStore(t)
	createWhatsmeowFixtureTables(t, ms.db)

	// same person synced under both identifiers; only the pn row carries the name
	mustExec(t, ms.db,
		"INSERT INTO whatsmeow_contacts (our_jid, their_jid, first_name, full_name, push_name) VALUES (?, ?, ?, ?, ?)",
		"me", "77001234567@s.whatsapp.net", "Aidos", "Aidos Qurmanov", "")
	mustExec(t, ms.db,
		"INSERT INTO whatsmeow_contacts (our_jid, their_jid, first_name, full_name, push_name) VALUES (?, ?, ?, ?, ?)",
		"me", "191813407211689@lid", "", "", "aidos")
	mustExec(t, ms.db, "INSERT INTO whatsmeow_lid_map (lid, pn) VALUES (?, ?)",
		"191813407211689", "77001234567")

	contacts, err := ms.SearchContacts("77001234567")
	if err != nil {
		t.Fatalf("SearchContacts: %v", err)
	}
	if len(contacts) != 1 {
		t.Fatalf("got %d contacts, want 1 (deduped): %+v", len(contacts), contacts)
	}
	c := contacts[0]
	if c.JID != "77001234567@s.whatsapp.net" {
		t.Errorf("JID = %q, want pn-form JID", c.JID)
	}
	if c.Name != "Aidos Qurmanov" {
		t.Errorf("Name = %q, want %q", c.Name, "Aidos Qurmanov")
	}
}

func TestSearchContacts_UnmappedLIDKeepsLIDAsPhone(t *testing.T) {
	ms := newTestMessageStore(t)
	createWhatsmeowFixtureTables(t, ms.db)

	mustExec(t, ms.db,
		"INSERT INTO whatsmeow_contacts (our_jid, their_jid, first_name, full_name, push_name) VALUES (?, ?, ?, ?, ?)",
		"me", "555000111222333@lid", "", "", "mystery")

	contacts, err := ms.SearchContacts("mystery")
	if err != nil {
		t.Fatalf("SearchContacts: %v", err)
	}
	if len(contacts) != 1 {
		t.Fatalf("got %d contacts, want 1: %+v", len(contacts), contacts)
	}
	if contacts[0].PhoneNumber != "555000111222333" {
		t.Errorf("PhoneNumber = %q, want lid fallback", contacts[0].PhoneNumber)
	}
	if contacts[0].Name != "mystery" {
		t.Errorf("Name = %q, want push name fallback %q", contacts[0].Name, "mystery")
	}
}

// fakeLIDs stubs the whatsmeow LID store with a fixed lid→pn map.
type fakeLIDs struct {
	store.LIDStore
	pnForLID map[string]types.JID
}

func (f *fakeLIDs) GetPNForLID(ctx context.Context, lid types.JID) (types.JID, error) {
	return f.pnForLID[lid.User], nil
}

func TestResolveSenderPN(t *testing.T) {
	lids := &fakeLIDs{pnForLID: map[string]types.JID{
		"191813407211689": types.NewJID("77001234567", types.DefaultUserServer),
	}}
	ctx := context.Background()

	// SenderAlt carried on the message wins over a store lookup
	info := types.MessageInfo{}
	info.Sender = types.NewJID("191813407211689", types.HiddenUserServer)
	info.SenderAlt = types.NewJID("77009999999", types.DefaultUserServer)
	if got := resolveSenderPN(ctx, lids, info); got.User != "77009999999" {
		t.Errorf("resolveSenderPN with SenderAlt = %v, want 77009999999", got)
	}

	// without SenderAlt, falls back to the lid store
	info.SenderAlt = types.EmptyJID
	if got := resolveSenderPN(ctx, lids, info); got.User != "77001234567" {
		t.Errorf("resolveSenderPN via store = %v, want 77001234567", got)
	}

	// non-lid senders pass through untouched
	info.Sender = types.NewJID("77001234567", types.DefaultUserServer)
	if got := resolveSenderPN(ctx, nil, info); got != info.Sender {
		t.Errorf("resolveSenderPN(pn sender) = %v, want unchanged", got)
	}
}

func TestResolvePNJID(t *testing.T) {
	lids := &fakeLIDs{pnForLID: map[string]types.JID{
		"191813407211689": types.NewJID("77001234567", types.DefaultUserServer),
	}}
	ctx := context.Background()

	// known lid resolves to the phone-number JID
	got := resolvePNJID(ctx, lids, types.NewJID("191813407211689", types.HiddenUserServer))
	if got.User != "77001234567" || got.Server != types.DefaultUserServer {
		t.Errorf("resolvePNJID(known lid) = %v, want 77001234567@s.whatsapp.net", got)
	}

	// unknown lid stays as-is
	unknown := types.NewJID("999", types.HiddenUserServer)
	if got := resolvePNJID(ctx, lids, unknown); got != unknown {
		t.Errorf("resolvePNJID(unknown lid) = %v, want unchanged", got)
	}

	// non-lid JIDs pass through untouched (no store call needed)
	pn := types.NewJID("77001234567", types.DefaultUserServer)
	if got := resolvePNJID(ctx, nil, pn); got != pn {
		t.Errorf("resolvePNJID(pn) = %v, want unchanged", got)
	}
}
