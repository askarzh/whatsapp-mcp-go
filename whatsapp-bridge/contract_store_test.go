package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestArrivalSequenceFollowsInsertOrderNotSentAt(t *testing.T) {
	s := newTestMessageStore(t)
	mustExec(t, s.db, `INSERT INTO chats (jid, name) VALUES ('77000000001@s.whatsapp.net', 'x')`)
	late := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) // old sent_at, arrives second
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

// A send reserves its idempotency key before it happens, so a crash between
// reserving and sending leaves a row that means "in flight" for a process
// that no longer exists. Without a way back, that key is 409 forever and the
// message can never be sent.
func TestAStaleSendReservationCanBeReclaimed(t *testing.T) {
	s := newTestMessageStore(t)
	first, err := s.ReserveSend("k1")
	if err != nil || !first {
		t.Fatalf("first reservation: %v %v", first, err)
	}
	again, err := s.ReserveSend("k1")
	if err != nil || again {
		t.Fatalf("a fresh reservation must still block a second send: %v %v", again, err)
	}
	// the same reservation, older than any send could plausibly be
	mustExec(t, s.db, `UPDATE sent_by_key SET sent_at_unix = ? WHERE idempotency_key = 'k1'`,
		time.Now().Add(-11*time.Minute).Unix())
	reclaimed, err := s.ReserveSend("k1")
	if err != nil || !reclaimed {
		t.Fatalf("a reservation older than the TTL must be reclaimable: %v %v", reclaimed, err)
	}
	// a completed send is not a reservation and is never reclaimed
	if err := s.CompleteSend("k1", "3AC2", time.Now()); err != nil {
		t.Fatal(err)
	}
	mustExec(t, s.db, `UPDATE sent_by_key SET sent_at_unix = ? WHERE idempotency_key = 'k1'`,
		time.Now().Add(-99*time.Hour).Unix())
	if taken, err := s.ReserveSend("k1"); err != nil || taken {
		t.Fatalf("a finished send must keep its answer: %v %v", taken, err)
	}
	if id, _, ok, err := s.RecallSend("k1"); err != nil || !ok || id != "3AC2" {
		t.Fatalf("recall: %q %v %v", id, ok, err)
	}
}

// Startup clears reservations no live process can own any more.
func TestStartupDropsReservationsLeftByADeadProcess(t *testing.T) {
	s := newTestMessageStore(t)
	if _, err := s.ReserveSend("k2"); err != nil {
		t.Fatal(err)
	}
	if err := ensureContractColumns(s.db); err != nil { // stands in for a restart
		t.Fatal(err)
	}
	if free, err := s.ReserveSend("k2"); err != nil || !free {
		t.Fatalf("a restart must free an unfinished reservation: %v %v", free, err)
	}
}

// Live delivery and a history-sync batch write at the same time; every
// message must get an arrival sequence of its own or Mindet's cursor steps
// over one. Run with -race.
func TestConcurrentStoresGetDistinctArrivalSequences(t *testing.T) {
	s := newTestMessageStore(t)
	mustExec(t, s.db, `INSERT INTO chats (jid, name) VALUES ('c@s.whatsapp.net', 'x')`)
	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- s.StoreMessage(fmt.Sprintf("m%02d", i), "c@s.whatsapp.net", "c", "hi",
				time.Now().UTC(), false, "", "", "", nil, nil, nil, 0)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("store: %v", err)
		}
	}
	rows, err := s.ListArrived(0, time.Time{}, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != n {
		t.Fatalf("stored %d of %d messages", len(rows), n)
	}
	seen := map[int64]bool{}
	for _, m := range rows {
		if seen[m.Seq] {
			t.Fatalf("arrival sequence %d handed out twice", m.Seq)
		}
		seen[m.Seq] = true
	}
	// and every one of them is reachable by paging one at a time
	var walked int
	cursor := int64(0)
	for {
		page, err := s.ListArrived(cursor, time.Time{}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		walked++
		cursor = page[0].Seq
	}
	if walked != n {
		t.Fatalf("paging handed out %d of %d messages", walked, n)
	}
}

// The timestamp column has no zone: what gets stored is the wall clock of
// whatever zone the time carries. whatsmeow hands the bridge a local time,
// so a bridge running in the owner's own zone would store 10:06 for an
// instant the contract then reports as 05:06Z — five hours of drift, no
// error anywhere, and arrived_at (a real epoch) silently disagreeing with
// sent_at on the same row.
func TestStoredTimesKeepTheirInstantWhateverTheZone(t *testing.T) {
	s := newTestMessageStore(t)
	mustExec(t, s.db, `INSERT INTO chats (jid, name) VALUES ('c@s.whatsapp.net', 'x')`)
	zone := time.FixedZone("+05", 5*60*60)
	instant := time.Date(2026, 9, 8, 5, 6, 7, 0, time.UTC)
	if err := s.StoreMessage("tz", "c@s.whatsapp.net", "c", "hi", instant.In(zone), false, "", "", "", nil, nil, nil, 0); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListArrived(0, time.Time{}, 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list: %+v %v", rows, err)
	}
	if !rows[0].Timestamp.Equal(instant) {
		t.Fatalf("read back %s, want the same instant as %s", rows[0].Timestamp, instant)
	}
	if rows[0].Timestamp.Location() != time.UTC {
		t.Fatalf("the contract labels every time UTC; got %s", rows[0].Timestamp.Location())
	}
	if got := fmtTime(rows[0].Timestamp); got != "2026-09-08T05:06:07.000000+00:00" {
		t.Fatalf("on the wire: %s", got)
	}
}
