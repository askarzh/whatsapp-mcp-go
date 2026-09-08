package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// The contract needs three things the original schema never had: the order
// in which *we* received each message (the cursor), when we received it
// (`until`), and a place for edits and deletions to be messages of their own.
// Adding columns is the only migration this bridge has ever needed, so it
// stays inline: idempotent ALTERs, then a one-time backfill.
func ensureContractColumns(db *sql.DB) error {
	cols := []string{
		"arrival_seq BIGINT", "arrived_at_unix BIGINT", "kind TEXT", "edits TEXT",
	}
	for _, c := range cols {
		name := strings.Fields(c)[0]
		if hasColumn(db, "messages", name) {
			continue
		}
		if _, err := db.Exec("ALTER TABLE messages ADD COLUMN " + c); err != nil {
			return fmt.Errorf("add column %s: %v", name, err)
		}
	}
	if isPostgres {
		if _, err := db.Exec(`CREATE SEQUENCE IF NOT EXISTS messages_arrival_seq`); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS sent_by_key (
		idempotency_key TEXT PRIMARY KEY, native_id TEXT NOT NULL, sent_at_unix BIGINT NOT NULL)`); err != nil {
		return err
	}
	// A reservation belongs to a send in flight in this process, so it
	// cannot outlive the process. One left behind by a crash between
	// reserving and sending would 409 that idempotency key forever: one
	// message to the boss that can never be sent, retried every tick.
	if _, err := db.Exec(`DELETE FROM sent_by_key WHERE native_id = ''`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS contract_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return err
	}
	// The generation is written once and never again, so a cursor can say
	// which store it came from. A recreated volume or a restore from an
	// older dump gets a new one, and every cursor from the old store is
	// then a 400 rather than a page that is empty for the wrong reason.
	gen := make([]byte, 8)
	if _, err := rand.Read(gen); err != nil {
		return err
	}
	if _, err := db.Exec(fmt.Sprintf(
		`INSERT INTO contract_meta (key, value) VALUES ('generation', %s) ON CONFLICT DO NOTHING`,
		placeholder(1)), hex.EncodeToString(gen)); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS ix_messages_arrival ON messages (arrival_seq)`); err != nil {
		return err
	}
	return backfillArrival(db)
}

// Generation identifies this store, for the lifetime of its data. Read once
// and remembered: it cannot change under a running process.
func (store *MessageStore) Generation() string {
	store.genMu.Lock()
	defer store.genMu.Unlock()
	if store.generation != "" {
		return store.generation
	}
	var v string
	if err := store.db.QueryRow(`SELECT value FROM contract_meta WHERE key = 'generation'`).Scan(&v); err != nil {
		slog.Warn("contract: no store generation; cursors cannot detect a rebuilt store", "err", err)
		return ""
	}
	store.generation = v
	return v
}

func hasColumn(db *sql.DB, table, column string) bool {
	var q string
	if isPostgres {
		q = fmt.Sprintf(`SELECT 1 FROM information_schema.columns WHERE table_name=%s AND column_name=%s`,
			placeholder(1), placeholder(2))
	} else {
		q = `SELECT 1 FROM pragma_table_info(?) WHERE name = ?`
	}
	var one int
	return db.QueryRow(q, table, column).Scan(&one) == nil
}

// Rows written before the columns existed get an arrival order that follows
// their WhatsApp time: the best guess there is, and a stable one.
func backfillArrival(db *sql.DB) error {
	var q string
	if isPostgres {
		q = `UPDATE messages m SET arrival_seq = s.rn, arrived_at_unix = EXTRACT(EPOCH FROM m.timestamp)::bigint
		     FROM (SELECT id, chat_jid, row_number() OVER (ORDER BY timestamp, id) AS rn FROM messages WHERE arrival_seq IS NULL) s
		     WHERE m.id = s.id AND m.chat_jid = s.chat_jid`
	} else {
		q = `UPDATE messages SET
		       arrival_seq = (SELECT rn FROM (SELECT id, chat_jid, row_number() OVER (ORDER BY timestamp, id) AS rn FROM messages WHERE arrival_seq IS NULL) s
		                      WHERE s.id = messages.id AND s.chat_jid = messages.chat_jid),
		       arrived_at_unix = CAST(strftime('%s', timestamp) AS INTEGER)
		     WHERE arrival_seq IS NULL`
	}
	if _, err := db.Exec(q); err != nil {
		return fmt.Errorf("backfill arrival: %v", err)
	}
	if isPostgres {
		_, err := db.Exec(`SELECT setval('messages_arrival_seq', coalesce((SELECT max(arrival_seq) FROM messages), 0) + 1, false)`)
		return err
	}
	return nil
}

// nextArrivalSQL is the expression that allocates the next arrival sequence
// inside an INSERT: a real sequence on Postgres, max+1 on SQLite, where the
// bridge is the single writer.
func nextArrivalSQL() string {
	if isPostgres {
		return "nextval('messages_arrival_seq')"
	}
	return "(SELECT coalesce(max(arrival_seq), 0) + 1 FROM messages)"
}

// storeMu serialises arrival-sequence allocation; see StoreMessageKind.
var storeMu sync.Mutex

// A message with nothing to say is dropped, as before — unless it is a
// correction, whose whole point may be that it says nothing (a deletion).
func (store *MessageStore) StoreMessageKind(id, chatJID, sender, content string, timestamp time.Time, isFromMe bool,
	mediaType, filename, url string, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64, kind, edits string) error {
	if content == "" && mediaType == "" && kind != "tombstone" && kind != "edit" {
		return nil
	}
	if kind == "" {
		kind = "chat"
		if mediaType == "audio" {
			kind = "voice"
		}
	}
	now := time.Now().Unix()
	cols := "(id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length, kind, edits, arrival_seq, arrived_at_unix)"
	ph := make([]string, 0, 16)
	for i := 1; i <= 16; i++ {
		ph = append(ph, placeholder(i))
	}
	// arrival_seq and arrived_at_unix are NOT in the update list: a re-store
	// (history sync after a live delivery) keeps the row's place in time.
	q := fmt.Sprintf(`INSERT INTO messages %s VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s, %s)
		ON CONFLICT (id, chat_jid) DO UPDATE SET
		sender = EXCLUDED.sender, content = EXCLUDED.content, timestamp = EXCLUDED.timestamp,
		is_from_me = EXCLUDED.is_from_me, media_type = EXCLUDED.media_type, filename = EXCLUDED.filename,
		url = EXCLUDED.url, media_key = EXCLUDED.media_key, file_sha256 = EXCLUDED.file_sha256,
		file_enc_sha256 = EXCLUDED.file_enc_sha256, file_length = EXCLUDED.file_length,
		kind = EXCLUDED.kind, edits = EXCLUDED.edits`,
		cols, ph[0], ph[1], ph[2], ph[3], ph[4], ph[5], ph[6], ph[7], ph[8], ph[9], ph[10], ph[11], ph[12], ph[13], ph[14],
		nextArrivalSQL(), ph[15])
	// The arrival sequence is allocated inside this statement — max+1 on
	// SQLite, nextval on Postgres — and neither is safe against a second
	// writer. whatsmeow does not promise one: a history-sync batch runs in
	// its own goroutine for minutes and overlaps live delivery by
	// construction. Two writers could take the same number (SQLite) or
	// commit out of order across a poll (Postgres), and Mindet's cursor
	// would step over a message. The window is milliseconds; the message
	// might be the directive.
	storeMu.Lock()
	defer storeMu.Unlock()
	_, err := store.db.Exec(q, id, chatJID, sender, content, timestamp, isFromMe, mediaType, filename, url,
		mediaKey, fileSHA256, fileEncSHA256, fileLength, kind, edits, now)
	return err
}

type ArrivedMessage struct {
	Seq           int64
	ArrivedAt     time.Time
	ID, ChatJID   string
	Sender        string
	Content       string
	Timestamp     time.Time
	IsFromMe      bool
	MediaType     string
	Filename, URL string
	MediaKey      []byte
	FileSHA256    []byte
	FileEncSHA256 []byte
	FileLength    uint64
	Kind, Edits   string
}

const arrivedCols = `arrival_seq, arrived_at_unix, id, chat_jid, sender, coalesce(content,''), timestamp, is_from_me,
	coalesce(media_type,''), coalesce(filename,''), coalesce(url,''), media_key, file_sha256, file_enc_sha256,
	coalesce(file_length,0), coalesce(kind,''), coalesce(edits,'')`

func scanArrived(rows *sql.Rows) (ArrivedMessage, error) {
	var m ArrivedMessage
	var arrived int64
	var fileLength int64
	err := rows.Scan(&m.Seq, &arrived, &m.ID, &m.ChatJID, &m.Sender, &m.Content, &m.Timestamp, &m.IsFromMe,
		&m.MediaType, &m.Filename, &m.URL, &m.MediaKey, &m.FileSHA256, &m.FileEncSHA256, &fileLength, &m.Kind, &m.Edits)
	m.ArrivedAt = time.Unix(arrived, 0).UTC()
	m.Timestamp = m.Timestamp.UTC()
	m.FileLength = uint64(fileLength)
	if m.Kind == "" {
		m.Kind = "chat"
		if m.MediaType == "audio" {
			m.Kind = "voice"
		}
	}
	return m, err
}

// ListArrived is the contract's page: strictly after a sequence, at most
// limit, bounded by arrival time when until is set.
func (store *MessageStore) ListArrived(afterSeq int64, until time.Time, limit int) ([]ArrivedMessage, error) {
	q := fmt.Sprintf(`SELECT %s FROM messages WHERE arrival_seq > %s`, arrivedCols, placeholder(1))
	args := []any{afterSeq}
	if !until.IsZero() {
		q += fmt.Sprintf(` AND arrived_at_unix <= %s`, placeholder(2))
		args = append(args, until.Unix())
	}
	// id is the tie-break: if two rows ever shared a sequence, an unstable
	// order could drop one at a page boundary and never hand it out again.
	q += fmt.Sprintf(` ORDER BY arrival_seq, id LIMIT %s`, placeholder(len(args)+1))
	args = append(args, limit)
	rows, err := store.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArrivedMessage
	for rows.Next() {
		m, err := scanArrived(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (store *MessageStore) LastArrival() (time.Time, bool, error) {
	var v sql.NullInt64
	if err := store.db.QueryRow(`SELECT max(arrived_at_unix) FROM messages`).Scan(&v); err != nil {
		return time.Time{}, false, err
	}
	if !v.Valid {
		return time.Time{}, false, nil
	}
	return time.Unix(v.Int64, 0).UTC(), true, nil
}

func (store *MessageStore) LookupMessage(id, chatJID string) (*ArrivedMessage, error) {
	rows, err := store.db.Query(fmt.Sprintf(`SELECT %s FROM messages WHERE id = %s AND chat_jid = %s`,
		arrivedCols, placeholder(1), placeholder(2)), id, chatJID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	m, err := scanArrived(rows)
	return &m, err
}

func (store *MessageStore) ListChatsForContract() ([]contractChat, error) {
	rows, err := store.db.Query(`SELECT jid, name FROM chats ORDER BY jid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []contractChat{}
	for rows.Next() {
		var jid string
		var name sql.NullString
		if err := rows.Scan(&jid, &name); err != nil {
			return nil, err
		}
		c := contractChat{NativeID: jid, Kind: chatKind(jid)}
		if name.Valid && name.String != "" {
			n := name.String
			c.Name = &n
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// nameFallback mirrors SearchContacts (main.go:2114): full_name, then
// first_name, then push_name.
const nameFallbackSQL = `coalesce(nullif(full_name,''), nullif(first_name,''), nullif(push_name,''), '')`

// HasContacts probes whether whatsmeow's own address-book table is reachable
// from this store's database. On some deployments (SQLite: whatsmeow keeps
// its tables in store/whatsapp.db, a different file from store/messages.db)
// it is not, and the contract has to know that up front rather than 500ing
// on every call to /contacts.
func (store *MessageStore) HasContacts() bool {
	var one int
	err := store.db.QueryRow(`SELECT 1 FROM whatsmeow_contacts LIMIT 1`).Scan(&one)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("contract: whatsmeow_contacts not reachable; /contacts disabled", "err", err)
		return false
	}
	return true
}

// Contacts come from whatsmeow's own address-book table. A phone row carries
// every linked id the lid map knows for the same number as an alias (spec
// §4); a @lid row with no mapping to a phone is emitted on its own, with no
// key — the roster is left to bind it. A @lid row that IS mapped is skipped
// here: it already went out as an alias on the phone row, and listing it
// again would double the same person.
func (store *MessageStore) ListContactsForContract() ([]contractContact, error) {
	agg := "group_concat"
	if isPostgres {
		agg = "string_agg"
	}
	out := []contractContact{}

	// lid and pn in whatsmeow_lid_map are bare user parts; the alias goes out
	// as a full JID so the raw id matches what a message's author carries.
	phoneRows, err := store.db.Query(fmt.Sprintf(`SELECT c.their_jid, %s AS name,
		coalesce((SELECT %s(l.lid || '@lid', ',') FROM whatsmeow_lid_map l WHERE l.pn || '@s.whatsapp.net' = c.their_jid), '') AS lids
		FROM whatsmeow_contacts c WHERE c.their_jid LIKE '%%@s.whatsapp.net' ORDER BY c.their_jid`, nameFallbackSQL, agg))
	if err != nil {
		return nil, err
	}
	for phoneRows.Next() {
		var jid, name, lids string
		if err := phoneRows.Scan(&jid, &name, &lids); err != nil {
			phoneRows.Close()
			return nil, err
		}
		c := contractContact{NativeID: jid, Aliases: []rawID{}}
		if user, _, _ := strings.Cut(jid, "@"); isDigits(user) {
			k := "e164:+" + user
			c.Key = &k
		}
		if name != "" {
			n := name
			c.Name = &n
		}
		for _, lid := range strings.Split(lids, ",") {
			if lid != "" {
				c.Aliases = append(c.Aliases, rawID{Type: "wa_lid", Value: lid})
			}
		}
		out = append(out, c)
	}
	if err := phoneRows.Err(); err != nil {
		phoneRows.Close()
		return nil, err
	}
	phoneRows.Close()

	// @lid rows the lid map cannot resolve to a phone: their own row, key
	// null, no aliases.
	lidRows, err := store.db.Query(fmt.Sprintf(`SELECT c.their_jid, %s AS name
		FROM whatsmeow_contacts c
		WHERE c.their_jid LIKE '%%@lid'
		  AND NOT EXISTS (SELECT 1 FROM whatsmeow_lid_map l WHERE l.lid || '@lid' = c.their_jid)
		ORDER BY c.their_jid`, nameFallbackSQL))
	if err != nil {
		return nil, err
	}
	defer lidRows.Close()
	for lidRows.Next() {
		var jid, name string
		if err := lidRows.Scan(&jid, &name); err != nil {
			return nil, err
		}
		c := contractContact{NativeID: jid, Aliases: []rawID{}}
		if name != "" {
			n := name
			c.Name = &n
		}
		out = append(out, c)
	}
	return out, lidRows.Err()
}

// ReserveSend, CompleteSend, ReleaseSend and RecallSend back the idempotency
// key on /bridge/v1/send (spec §3.6). A check-then-act ("is this key known?
// then send") lets two concurrent requests with the same key both reach
// WhatsApp, so the key is reserved — a row with an empty native_id — before
// the send happens; CompleteSend fills it in once the send actually
// succeeds, and ReleaseSend removes the reservation after a failed send so a
// retry with the same key is free to try again.

// sendReservationTTL is how long a send may plausibly be in flight. Past it,
// a reservation with no result is debris from a process that died.
const sendReservationTTL = 10 * time.Minute

// ReserveSend claims key for a send that is about to happen. It reports
// whether this call made the reservation: false means the key was already
// there, either as a finished send (RecallSend has the answer) or as another
// request's send still in flight.
func (store *MessageStore) ReserveSend(key string) (bool, error) {
	// A reservation this old cannot still be in flight: the send it belonged
	// to either finished (and would have a native_id) or died with the
	// process that made it. Reclaiming it is what lets a crashed send be
	// retried at all, without waiting for a restart.
	if _, err := store.db.Exec(fmt.Sprintf(`DELETE FROM sent_by_key WHERE native_id = '' AND sent_at_unix < %s`,
		placeholder(1)), time.Now().Add(-sendReservationTTL).Unix()); err != nil {
		return false, err
	}
	res, err := store.db.Exec(fmt.Sprintf(`INSERT INTO sent_by_key (idempotency_key, native_id, sent_at_unix) VALUES (%s, '', %s)
		ON CONFLICT (idempotency_key) DO NOTHING`, placeholder(1), placeholder(2)), key, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// CompleteSend fills in a reservation once the send has actually happened.
func (store *MessageStore) CompleteSend(key, nativeID string, sentAt time.Time) error {
	_, err := store.db.Exec(fmt.Sprintf(`UPDATE sent_by_key SET native_id = %s, sent_at_unix = %s WHERE idempotency_key = %s`,
		placeholder(1), placeholder(2), placeholder(3)), nativeID, sentAt.Unix(), key)
	return err
}

// ReleaseSend drops a reservation that never turned into a send, so the same
// key can be retried.
func (store *MessageStore) ReleaseSend(key string) error {
	_, err := store.db.Exec(fmt.Sprintf(`DELETE FROM sent_by_key WHERE idempotency_key = %s`, placeholder(1)), key)
	return err
}

// RecallSend answers a repeated request from what was recorded before. A row
// with an empty native_id is a reservation still being worked on, not a
// result — RecallSend reports that as "not found" (ok == false), same as no
// row at all; the handler tells the two apart by having just tried
// ReserveSend itself.
func (store *MessageStore) RecallSend(key string) (string, time.Time, bool, error) {
	var id string
	var at int64
	err := store.db.QueryRow(fmt.Sprintf(`SELECT native_id, sent_at_unix FROM sent_by_key WHERE idempotency_key = %s`,
		placeholder(1)), key).Scan(&id, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, err
	}
	if id == "" {
		return "", time.Time{}, false, nil
	}
	return id, time.Unix(at, 0).UTC(), true, nil
}
