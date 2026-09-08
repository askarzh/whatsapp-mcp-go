package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrBadCursor is what a cursor Mindet did not get from us decodes to.
var ErrBadCursor = errors.New("malformed cursor")

// The cursor is the arrival sequence of the last message handed out, wrapped
// so that it is opaque on the wire (spec §3.2): Mindet stores and returns it,
// never reads it. "v1:" leaves room to change the encoding without a
// consumer noticing anything but a 400 and a re-bootstrap.
//
// It also carries the store's generation, because a sequence alone is only
// meaningful within one store. Recreate the volume or restore an older dump
// and the sequence restarts; a cursor from the old store would then point
// past everything the new one holds, and the bridge would answer well-formed
// empty pages forever while ingestion was in fact dead. A generation
// mismatch is a 400, which is the one signal Mindet re-bootstraps on.
func encodeCursor(generation string, seq int64) string {
	return base64.RawURLEncoding.EncodeToString(
		[]byte("v1:" + generation + ":" + strconv.FormatInt(seq, 10)))
}

func decodeCursor(s string) (string, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return "", 0, ErrBadCursor
	}
	body, ok := strings.CutPrefix(string(raw), "v1:")
	if !ok {
		return "", 0, ErrBadCursor
	}
	// A cursor from before the generation existed has no second colon, and
	// is as stale as one from another store: it must not be honoured.
	generation, digits, ok := strings.Cut(body, ":")
	if !ok {
		return "", 0, fmt.Errorf("%w: %q", ErrBadCursor, s)
	}
	seq, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || seq < 0 {
		return "", 0, fmt.Errorf("%w: %q", ErrBadCursor, s)
	}
	return generation, seq, nil
}
