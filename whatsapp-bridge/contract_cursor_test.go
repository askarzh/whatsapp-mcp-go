package main

import "testing"

func TestCursorRoundTripsAndRejectsGarbage(t *testing.T) {
	c := encodeCursor("a1b2c3d4", 42)
	if c == "" {
		t.Fatal("cursor must not be empty")
	}
	gen, seq, err := decodeCursor(c)
	if err != nil || seq != 42 || gen != "a1b2c3d4" {
		t.Fatalf("round trip: %q %d %v", gen, seq, err)
	}
	if encodeCursor("a1b2c3d4", 0) == "" {
		t.Fatal("the zero cursor must still be a non-empty string")
	}
	for _, bad := range []string{"not a cursor", "djE6YWJj", "v2:1"} {
		if _, _, err := decodeCursor(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	// the pre-generation form, which a Mindet upgraded in place still holds
	old := "djE6NTAwMDA" // base64 of "v1:50000"
	if _, _, err := decodeCursor(old); err == nil {
		t.Fatal("a cursor with no generation must be rejected, not honoured")
	}
}
