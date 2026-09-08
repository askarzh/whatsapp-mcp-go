package main

import "testing"

func TestCursorRoundTripsAndRejectsGarbage(t *testing.T) {
	c := encodeCursor(42)
	if c == "" {
		t.Fatal("cursor must not be empty")
	}
	seq, err := decodeCursor(c)
	if err != nil || seq != 42 {
		t.Fatalf("round trip: %d %v", seq, err)
	}
	if encodeCursor(0) == "" {
		t.Fatal("the zero cursor must still be a non-empty string")
	}
	for _, bad := range []string{"not a cursor", "djE6YWJj", "v2:1"} {
		if _, err := decodeCursor(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
