package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

// resolvePNJID returns the phone-number form of jid when jid is a @lid
// address with a known lid↔pn mapping; otherwise jid is returned unchanged.
func resolvePNJID(ctx context.Context, lids store.LIDStore, jid types.JID) types.JID {
	if jid.Server != types.HiddenUserServer || lids == nil {
		return jid
	}
	pn, err := lids.GetPNForLID(ctx, jid)
	if err != nil || pn.IsEmpty() {
		return jid
	}
	return pn
}

// resolveSenderPN returns the phone-number form of a message sender,
// preferring the SenderAlt already carried on the message over a store lookup.
func resolveSenderPN(ctx context.Context, lids store.LIDStore, info types.MessageInfo) types.JID {
	if info.Sender.Server != types.HiddenUserServer {
		return info.Sender
	}
	if info.SenderAlt.Server == types.DefaultUserServer && !info.SenderAlt.IsEmpty() {
		return info.SenderAlt
	}
	return resolvePNJID(ctx, lids, info.Sender)
}

// applyLIDMappings re-keys @lid chats and senders in the bridge DB to their
// phone-number JIDs. nameFor supplies a display name for a phone-number JID
// when neither the lid nor pn chat row has a usable one; it may be nil.
// Safe to run repeatedly.
func applyLIDMappings(ms *MessageStore, mappings []store.LIDMapping, nameFor func(pn types.JID) string) error {
	for _, m := range mappings {
		if m.LID.User == "" || m.PN.User == "" {
			continue
		}
		if err := applyOneLIDMapping(ms, m, nameFor); err != nil {
			return fmt.Errorf("lid %s -> pn %s: %w", m.LID.User, m.PN.User, err)
		}
	}
	return nil
}

func applyOneLIDMapping(ms *MessageStore, m store.LIDMapping, nameFor func(pn types.JID) string) error {
	placeholder := func(n int) string {
		if isPostgres {
			return fmt.Sprintf("$%d", n)
		}
		return "?"
	}

	lidJID := types.NewJID(m.LID.User, types.HiddenUserServer).String()
	pnJID := types.NewJID(m.PN.User, types.DefaultUserServer).String()

	tx, err := ms.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Rewrite senders stored as either the bare lid user (live messages)
	// or the full @lid JID (history sync participants).
	_, err = tx.Exec(
		"UPDATE messages SET sender = "+placeholder(1)+
			" WHERE sender IN ("+placeholder(2)+", "+placeholder(3)+")",
		m.PN.User, m.LID.User, lidJID,
	)
	if err != nil {
		return err
	}

	var lidName sql.NullString
	var lidLMT sql.NullTime
	err = tx.QueryRow(
		"SELECT name, last_message_time FROM chats WHERE jid = "+placeholder(1), lidJID,
	).Scan(&lidName, &lidLMT)
	if err == sql.ErrNoRows {
		// No lid-keyed chat; the sender rewrite above is all there is to do.
		return tx.Commit()
	}
	if err != nil {
		return err
	}

	var pnName sql.NullString
	var pnLMT sql.NullTime
	err = tx.QueryRow(
		"SELECT name, last_message_time FROM chats WHERE jid = "+placeholder(1), pnJID,
	).Scan(&pnName, &pnLMT)
	pnExists := err == nil
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	// A name is junk when it's just one of the raw identifiers.
	junk := func(s string) bool {
		return s == "" || s == m.LID.User || s == m.PN.User
	}
	var name string
	switch {
	case pnExists && !junk(pnName.String):
		name = pnName.String
	case !junk(lidName.String):
		name = lidName.String
	default:
		if nameFor != nil {
			name = nameFor(m.PN)
		}
		if name == "" {
			name = m.PN.User
		}
	}

	lmt := lidLMT.Time
	if pnExists && pnLMT.Valid && pnLMT.Time.After(lmt) {
		lmt = pnLMT.Time
	}

	if isPostgres {
		_, err = tx.Exec(
			`INSERT INTO chats (jid, name, last_message_time) VALUES ($1, $2, $3)
			 ON CONFLICT (jid) DO UPDATE SET
			    name = EXCLUDED.name,
			    last_message_time = EXCLUDED.last_message_time`,
			pnJID, name, lmt,
		)
	} else {
		_, err = tx.Exec(
			"INSERT OR REPLACE INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
			pnJID, name, lmt,
		)
	}
	if err != nil {
		return err
	}

	// Drop lid-side copies of messages that already exist under the pn chat,
	// then move the rest over and remove the lid chat row.
	_, err = tx.Exec(
		"DELETE FROM messages WHERE chat_jid = "+placeholder(1)+
			" AND id IN (SELECT id FROM messages WHERE chat_jid = "+placeholder(2)+")",
		lidJID, pnJID,
	)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		"UPDATE messages SET chat_jid = "+placeholder(1)+" WHERE chat_jid = "+placeholder(2),
		pnJID, lidJID,
	)
	if err != nil {
		return err
	}
	_, err = tx.Exec("DELETE FROM chats WHERE jid = "+placeholder(1), lidJID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// collectLIDCandidates returns the distinct @lid JIDs present in the bridge's
// chats and messages tables.
func collectLIDCandidates(ms *MessageStore) ([]types.JID, error) {
	rows, err := ms.db.Query(`
		SELECT jid FROM chats WHERE jid LIKE '%@lid'
		UNION
		SELECT DISTINCT sender FROM messages WHERE sender LIKE '%@lid'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lids []types.JID
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		jid, err := types.ParseJID(raw)
		if err != nil || jid.Server != types.HiddenUserServer {
			continue
		}
		lids = append(lids, jid)
	}
	return lids, rows.Err()
}

// migrateLIDData re-keys @lid chats and senders accumulated in the bridge DB
// to phone-number JIDs, using whatsmeow's lid↔pn map and contact store for
// names. Mappings whatsmeow doesn't know yet are left as-is and picked up on
// a later run.
func migrateLIDData(ctx context.Context, client *whatsmeow.Client, ms *MessageStore) {
	lids, err := collectLIDCandidates(ms)
	if err != nil {
		slog.Error("lid migration: failed to collect candidates", "err", err)
		return
	}
	if len(lids) == 0 {
		return
	}

	var mappings []store.LIDMapping
	for _, lid := range lids {
		pn, err := client.Store.LIDs.GetPNForLID(ctx, lid)
		if err != nil || pn.IsEmpty() {
			continue
		}
		mappings = append(mappings, store.LIDMapping{LID: lid, PN: pn})
	}
	if len(mappings) == 0 {
		slog.Info("lid migration: no known lid->pn mappings yet", "lid_candidates", len(lids))
		return
	}

	nameFor := func(pn types.JID) string {
		contact, err := client.Store.Contacts.GetContact(ctx, pn)
		if err != nil {
			return ""
		}
		if contact.FullName != "" {
			return contact.FullName
		}
		return contact.PushName
	}

	if err := applyLIDMappings(ms, mappings, nameFor); err != nil {
		slog.Error("lid migration failed", "err", err)
		return
	}
	slog.Info("lid migration complete", "resolved", len(mappings), "candidates", len(lids))
}
