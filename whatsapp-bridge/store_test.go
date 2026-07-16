package main

import (
	"testing"
	"time"
)

func TestListChats_NoDuplicatesOnTiedTimestamps(t *testing.T) {
	ms := newTestMessageStore(t)
	ts := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	mustExec(t, ms.db, "INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		"77001234567@s.whatsapp.net", "Aidos", ts)
	// two messages sharing the exact same timestamp
	mustExec(t, ms.db,
		"INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me) VALUES (?, ?, ?, ?, ?, ?)",
		"m1", "77001234567@s.whatsapp.net", "77001234567", "first", ts, false)
	mustExec(t, ms.db,
		"INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me) VALUES (?, ?, ?, ?, ?, ?)",
		"m2", "77001234567@s.whatsapp.net", "77001234567", "second", ts, false)

	chats, err := ms.ListChats(nil, 10, 0, true, "")
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(chats) != 1 {
		t.Fatalf("got %d chats, want 1: %+v", len(chats), chats)
	}
	if chats[0].LastMessage == "" {
		t.Errorf("LastMessage is empty, want one of the tied messages")
	}
}

func TestListChats_WithoutLastMessage(t *testing.T) {
	ms := newTestMessageStore(t)
	ts := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	mustExec(t, ms.db, "INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		"77001234567@s.whatsapp.net", "Aidos", ts)

	chats, err := ms.ListChats(nil, 10, 0, false, "")
	if err != nil {
		t.Fatalf("ListChats(includeLastMessage=false): %v", err)
	}
	if len(chats) != 1 {
		t.Fatalf("got %d chats, want 1", len(chats))
	}
	if chats[0].Name != "Aidos" {
		t.Errorf("Name = %q, want %q", chats[0].Name, "Aidos")
	}
}

func TestGetChat_ReturnsLatestMessageEvenWhenTimesDrift(t *testing.T) {
	ms := newTestMessageStore(t)
	msgTime := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	// chats.last_message_time drifted ahead of the newest stored message
	chatTime := msgTime.Add(2 * time.Second)

	mustExec(t, ms.db, "INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		"77001234567@s.whatsapp.net", "Aidos", chatTime)
	mustExec(t, ms.db,
		"INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me) VALUES (?, ?, ?, ?, ?, ?)",
		"m1", "77001234567@s.whatsapp.net", "77001234567", "hello", msgTime, false)

	chat, err := ms.GetChat("77001234567@s.whatsapp.net", true)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if chat == nil {
		t.Fatal("chat is nil, want a chat")
	}
	if chat.LastMessage != "hello" {
		t.Errorf("LastMessage = %q, want %q", chat.LastMessage, "hello")
	}
}

func TestGetDirectChatByContact_NotFoundReturnsNil(t *testing.T) {
	ms := newTestMessageStore(t)

	chat, err := ms.GetDirectChatByContact("70000000000")
	if err != nil {
		t.Fatalf("GetDirectChatByContact on empty store: %v, want nil error", err)
	}
	if chat != nil {
		t.Errorf("chat = %+v, want nil", chat)
	}
}
