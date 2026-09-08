package main

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
)

const timeLayout = "2006-01-02T15:04:05.000000+00:00"

func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// chatKind names what a conversation is (spec §3.3). WhatsApp has groups and
// one-way feeds; everything else, including a linked-id chat, is a person.
func chatKind(jid string) string {
	switch {
	case strings.HasSuffix(jid, "@g.us"):
		return "group"
	case strings.HasSuffix(jid, "@newsletter"), strings.HasSuffix(jid, "@broadcast"):
		return "feed"
	default:
		return "direct"
	}
}

type rawID struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type contractAuthor struct {
	NativeID string  `json:"native_id"`
	Key      *string `json:"key"`
	Raw      []rawID `json:"raw,omitempty"`
	Name     string  `json:"name,omitempty"`
	IsOwner  bool    `json:"is_owner"`
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// authorOf turns the stored sender (a user part such as "7700…" or a full
// "@lid" JID) into the contract's author. A key is only ever made from a
// phone number (spec §4); a linked id is passed raw for the roster to bind.
func authorOf(sender string, isFromMe bool, ownerJID string) contractAuthor {
	if isFromMe {
		owner := ownerJID
		key := "e164:+" + strings.SplitN(ownerJID, "@", 2)[0]
		return contractAuthor{NativeID: owner, Key: &key, IsOwner: true}
	}
	user, server, hasServer := strings.Cut(sender, "@")
	if hasServer && server == "lid" {
		return contractAuthor{NativeID: sender, Raw: []rawID{{Type: "wa_lid", Value: sender}}}
	}
	if isDigits(user) {
		key := "e164:+" + user
		return contractAuthor{NativeID: user + "@s.whatsapp.net", Key: &key}
	}
	return contractAuthor{NativeID: sender}
}

type contractFile struct {
	Path    *string `json:"path"`
	Mime    string  `json:"mime,omitempty"`
	SHA256  *string `json:"sha256"`
	Role    string  `json:"role"`
	Pending bool    `json:"pending,omitempty"`
}

type contractMessage struct {
	NativeID string         `json:"native_id"`
	Chat     string         `json:"chat"`
	Thread   *struct{}      `json:"thread"`
	Author   contractAuthor `json:"author"`
	SentAt   string         `json:"sent_at"`
	Kind     string         `json:"kind"`
	Text     string         `json:"text"`
	ReplyTo  *string        `json:"reply_to"`
	Edits    *string        `json:"edits"`
	EditedAt *string        `json:"edited_at,omitempty"`
	Files    []contractFile `json:"files"`
	Meta     map[string]any `json:"meta"`
}

func mimeFor(mediaType, filename string) string {
	switch mediaType {
	case "audio":
		return "audio/ogg"
	case "image":
		return "image/jpeg"
	case "video":
		return "video/mp4"
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".pdf":
		return "application/pdf"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	}
	return "application/octet-stream"
}

func roleFor(mediaType string) string {
	switch mediaType {
	case "audio":
		return "voice"
	case "image", "video":
		return "image"
	}
	return "attachment"
}

// toContractMessage is the row on the wire. A file is `pending` until the
// bytes are under the download dir; then its path is relative to the shared
// root, which is what Mindet's daemon can open (spec §3.2, §10).
func toContractMessage(m ArrivedMessage, ownerJID, sharedRoot, downloadDir string) contractMessage {
	out := contractMessage{
		NativeID: m.ID, Chat: m.ChatJID, Author: authorOf(m.Sender, m.IsFromMe, ownerJID),
		SentAt: fmtTime(m.Timestamp), Kind: m.Kind, Text: m.Content, Files: []contractFile{}, Meta: map[string]any{},
	}
	if m.Kind == "edit" || m.Kind == "tombstone" {
		e := m.Edits
		out.Edits = &e
		if m.Kind == "edit" {
			at := fmtTime(m.Timestamp)
			out.EditedAt = &at
		}
	}
	if m.MediaType != "" && m.Filename != "" {
		f := contractFile{Mime: mimeFor(m.MediaType, m.Filename), Role: roleFor(m.MediaType), Pending: true}
		if len(m.FileSHA256) > 0 {
			h := hex.EncodeToString(m.FileSHA256)
			f.SHA256 = &h
		}
		abs := filepath.Join(downloadDir, m.ChatJID, m.Filename)
		if _, err := os.Stat(abs); err == nil {
			if rel, err := filepath.Rel(sharedRoot, abs); err == nil && !strings.HasPrefix(rel, "..") {
				f.Path = &rel
				f.Pending = false
			}
		}
		out.Files = append(out.Files, f)
	}
	return out
}

// classifyProtocol recognises the two protocol messages the ledger cares
// about. WhatsApp delivers them as messages of their own, and so do we.
func classifyProtocol(msg *waE2E.Message) (kind, edits, text string, ok bool) {
	if msg == nil {
		return "", "", "", false
	}
	pm := msg.GetProtocolMessage()
	if pm == nil {
		if em := msg.GetEditedMessage(); em != nil {
			pm = em.GetMessage().GetProtocolMessage()
		}
	}
	if pm == nil {
		return "", "", "", false
	}
	switch pm.GetType() {
	case waE2E.ProtocolMessage_REVOKE:
		return "tombstone", pm.GetKey().GetID(), "", true
	case waE2E.ProtocolMessage_MESSAGE_EDIT:
		return "edit", pm.GetKey().GetID(), extractTextContent(pm.GetEditedMessage()), true
	}
	return "", "", "", false
}
