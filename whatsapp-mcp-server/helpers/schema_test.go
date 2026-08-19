package helpers

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// query returns the params the bridge actually received, failing loudly if the
// request never got there (an unescaped space makes the HTTP client itself
// refuse the URL, so the stub is never called).
func query(t *testing.T, last *http.Request) url.Values {
	t.Helper()
	if last.URL == nil {
		t.Fatal("bridge was never reached — the request URL was rejected before it left the client")
	}
	return last.URL.Query()
}

// The go-sdk marks EVERY field without json ",omitempty" as required, and it
// treats the whole `jsonschema` tag as the property description — so
// `jsonschema:"default:20"` sets no default, it just prints "default:20" as the
// description. Tools that leaned on those tags for defaults ended up demanding
// the caller pass limit/page/include_context/... on every call.
func requiredOf[T any](t *testing.T) []string {
	t.Helper()
	s, err := jsonschema.For[T](nil)
	if err != nil {
		t.Fatalf("infer schema: %v", err)
	}
	return s.Required
}

func TestOnlyGenuinelyRequiredFieldsAreRequired(t *testing.T) {
	tests := []struct {
		name string
		got  []string
		want []string
	}{
		{"list_messages", requiredOf[listMessagesInput](t), nil},
		{"list_chats", requiredOf[listChatsInput](t), nil},
		{"get_message_context", requiredOf[getMessageContextInput](t), []string{"message_id"}},
		{"get_chat", requiredOf[getChatInput](t), []string{"chat_jid"}},
		{"get_contact_chats", requiredOf[getContactChatsInput](t), []string{"jid"}},
		{"search_contacts", requiredOf[searchContactsInput](t), []string{"query"}},
		{"send_message", requiredOf[sendMessageInput](t), []string{"recipient", "message"}},
	}
	for _, tc := range tests {
		slices.Sort(tc.got)
		slices.Sort(tc.want)
		if !slices.Equal(tc.got, tc.want) {
			t.Errorf("%s required = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

// A leftover `jsonschema:"default:N"` tag renders as the property description,
// which is what the model reads. Catch any that come back.
func TestNoDefaultTagsLeakIntoDescriptions(t *testing.T) {
	s, err := jsonschema.For[listMessagesInput](nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, prop := range s.Properties {
		if len(prop.Description) >= 8 && prop.Description[:8] == "default:" {
			t.Errorf("property %q description is a stray tag: %q", name, prop.Description)
		}
	}
	if _, ok := s.Properties["after"]; !ok {
		t.Errorf("expected snake_case property \"after\", got properties %v", s.Properties)
	}
}

// Same defect class as search_contacts: raw concatenation puts a literal space
// in the request line and the bridge answers 400.
func TestListMessagesEscapesParams(t *testing.T) {
	_, last := newBridgeStub(t, `plain text response`)

	q := "обед в 14:00"
	jid := "77077254753@s.whatsapp.net"
	res, _, err := listMessagesHandler(context.Background(), nil, listMessagesInput{Query: &q, ChatJid: &jid})
	if err != nil || res.IsError {
		t.Fatalf("handler failed: %v %+v", err, res)
	}
	if got := query(t, last).Get("search"); got != q {
		t.Errorf("search = %q, want %q", got, q)
	}
	if got := query(t, last).Get("chat"); got != jid {
		t.Errorf("chat = %q, want %q", got, jid)
	}
}

func TestListChatsEscapesParams(t *testing.T) {
	_, last := newBridgeStub(t, `{"chats":[]}`)

	q := "Руслан Шалтыков"
	if _, _, err := listChatsHandler(context.Background(), nil, listChatsInput{Query: &q}); err != nil {
		t.Fatal(err)
	}
	if got := query(t, last).Get("q"); got != q {
		t.Errorf("q = %q, want %q", got, q)
	}
}

// Omitted optional fields must fall back to the documented defaults rather than
// Go zero values — limit=0 would fetch nothing.
func TestListMessagesAppliesDefaults(t *testing.T) {
	_, last := newBridgeStub(t, `plain`)

	if _, _, err := listMessagesHandler(context.Background(), nil, listMessagesInput{}); err != nil {
		t.Fatal(err)
	}
	if got := query(t, last).Get("limit"); got != "20" {
		t.Errorf("limit = %q, want 20", got)
	}
	if got := query(t, last).Get("context"); got != "true" {
		t.Errorf("context = %q, want true (include_context defaults on)", got)
	}
}

func TestListChatsAppliesDefaults(t *testing.T) {
	_, last := newBridgeStub(t, `{"chats":[]}`)

	if _, _, err := listChatsHandler(context.Background(), nil, listChatsInput{}); err != nil {
		t.Fatal(err)
	}
	if got := query(t, last).Get("limit"); got != "20" {
		t.Errorf("limit = %q, want 20", got)
	}
	if got := query(t, last).Get("sort"); got != "last_active" {
		t.Errorf("sort = %q, want last_active", got)
	}
}
