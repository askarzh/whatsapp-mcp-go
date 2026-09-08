package main

import (
	"testing"
	"time"
)

func TestArrivalSequenceFollowsInsertOrderNotSentAt(t *testing.T) {
	s := newTestMessageStore(t)
	mustExec(t, s.db, `INSERT INTO chats (jid, name) VALUES ('77000000001@s.whatsapp.net', 'x')`)
	late := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)   // old sent_at, arrives second
	fresh := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	if err := s.StoreMessage("fresh", "77000000001@s.whatsapp.net", "77000000001", "new", fresh, false, "", "", "", nil, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreMessage("late", "77000000001@s.whatsapp.net", "77000000001", "old", late, false, "", "", "", nil, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListArrived(0, time.Time{}, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != "fresh" || rows[1].ID != "late" {
		t.Fatalf("arrival order wrong: %+v", rows)
	}
	if rows[0].Seq >= rows[1].Seq || rows[0].ArrivedAt.IsZero() {
		t.Fatalf("seq/arrived_at not set: %+v", rows)
	}
	// a re-store of the same message keeps its place in the arrival order
	if err := s.StoreMessage("fresh", "77000000001@s.whatsapp.net", "77000000001", "new (again)", fresh, false, "", "", "", nil, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListArrived(0, time.Time{}, 500)
	if rows[0].ID != "fresh" || rows[0].Content != "new (again)" {
		t.Fatalf("re-store moved or lost the row: %+v", rows)
	}
}

func TestListArrivedPagesAfterSeqAndBoundsByUntil(t *testing.T) {
	s := newTestMessageStore(t)
	mustExec(t, s.db, `INSERT INTO chats (jid, name) VALUES ('c@s.whatsapp.net', 'x')`)
	for _, id := range []string{"a", "b", "c"} {
		if err := s.StoreMessage(id, "c@s.whatsapp.net", "c", id, time.Now().UTC(), false, "", "", "", nil, nil, nil, 0); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := s.ListArrived(0, time.Time{}, 2)
	if len(first) != 2 || first[0].ID != "a" {
		t.Fatalf("limit ignored: %+v", first)
	}
	rest, _ := s.ListArrived(first[1].Seq, time.Time{}, 2)
	if len(rest) != 1 || rest[0].ID != "c" {
		t.Fatalf("after-seq paging wrong: %+v", rest)
	}
	ancient, _ := s.ListArrived(0, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), 500)
	if len(ancient) != 0 {
		t.Fatalf("until not applied: %+v", ancient)
	}
}

func TestTombstoneAndEditAreStoredWithoutContent(t *testing.T) {
	s := newTestMessageStore(t)
	mustExec(t, s.db, `INSERT INTO chats (jid, name) VALUES ('c@s.whatsapp.net', 'x')`)
	now := time.Now().UTC()
	if err := s.StoreMessageKind("d1", "c@s.whatsapp.net", "c", "", now, false, "", "", "", nil, nil, nil, 0, "tombstone", "m1"); err != nil {
		t.Fatal(err)
	}
	if err := s.StoreMessageKind("e1", "c@s.whatsapp.net", "c", "can't today", now, false, "", "", "", nil, nil, nil, 0, "edit", "m1"); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.ListArrived(0, time.Time{}, 500)
	if len(rows) != 2 || rows[0].Kind != "tombstone" || rows[0].Edits != "m1" || rows[1].Kind != "edit" {
		t.Fatalf("corrections not stored: %+v", rows)
	}
	// the old guard still drops a message that says nothing and is not a correction
	if err := s.StoreMessage("empty", "c@s.whatsapp.net", "c", "", now, false, "", "", "", nil, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListArrived(0, time.Time{}, 500)
	if len(rows) != 2 {
		t.Fatalf("empty message stored: %+v", rows)
	}
}

func TestBackfillGivesExistingRowsAnArrivalOrderBySentAt(t *testing.T) {
	s := newTestMessageStore(t)
	mustExec(t, s.db, `INSERT INTO chats (jid, name) VALUES ('c@s.whatsapp.net', 'x')`)
	// rows written by an older bridge: no arrival columns filled
	mustExec(t, s.db, `UPDATE messages SET arrival_seq = NULL`) // no-op on empty table, proves the column exists
	mustExec(t, s.db, `INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me) VALUES
		('second', 'c@s.whatsapp.net', 'c', 'b', '2026-09-02 00:00:00', 0),
		('first',  'c@s.whatsapp.net', 'c', 'a', '2026-09-01 00:00:00', 0)`)
	if err := backfillArrival(s.db); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.ListArrived(0, time.Time{}, 500)
	if len(rows) != 2 || rows[0].ID != "first" || rows[1].ID != "second" {
		t.Fatalf("backfill order wrong: %+v", rows)
	}
	last, ok, _ := s.LastArrival()
	if !ok || last.IsZero() {
		t.Fatalf("LastArrival: %v %v", last, ok)
	}
}
