package main

import (
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow/types"
)

// nameHintForChat returns the sender GetChatName may fall back to when the
// contact store has no name for a chat. Our own messages carry the owner's
// JID as sender, which must never become the peer chat's name.
func nameHintForChat(isFromMe bool, senderUser string) string {
	if isFromMe {
		return ""
	}
	return senderUser
}

// repairSelfNamedChats renames non-group chats whose stored name is one of
// the account owner's own identifiers (phone number or LID user) — the
// residue of an old bug where outgoing messages named the peer's chat after
// the sender. nameFor supplies a contact name for a chat JID; when it
// returns "", the peer's own user part is used. Returns the number of chats
// renamed.
func repairSelfNamedChats(ms *MessageStore, selfUsers []string, nameFor func(jid types.JID) string) (int, error) {
	if len(selfUsers) == 0 {
		return 0, nil
	}

	marks := make([]string, len(selfUsers))
	args := make([]any, len(selfUsers))
	for i, u := range selfUsers {
		marks[i] = placeholder(i + 1)
		args[i] = u
	}

	rows, err := ms.db.Query(
		"SELECT jid FROM chats WHERE jid NOT LIKE '%@g.us' AND name IN ("+strings.Join(marks, ", ")+")",
		args...,
	)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var jids []string
	for rows.Next() {
		var jid string
		if err := rows.Scan(&jid); err != nil {
			continue
		}
		jids = append(jids, jid)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	repaired := 0
	for _, raw := range jids {
		jid, err := types.ParseJID(raw)
		if err != nil {
			continue
		}
		name := ""
		if nameFor != nil {
			name = nameFor(jid)
		}
		if name == "" {
			name = jid.User
		}
		if _, err := ms.db.Exec(
			"UPDATE chats SET name = "+placeholder(1)+" WHERE jid = "+placeholder(2),
			name, raw,
		); err != nil {
			return repaired, fmt.Errorf("rename chat %s: %w", raw, err)
		}
		repaired++
	}
	return repaired, nil
}
