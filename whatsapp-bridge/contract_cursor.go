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
func encodeCursor(seq int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte("v1:" + strconv.FormatInt(seq, 10)))
}

func decodeCursor(s string) (int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, ErrBadCursor
	}
	body, ok := strings.CutPrefix(string(raw), "v1:")
	if !ok {
		return 0, ErrBadCursor
	}
	seq, err := strconv.ParseInt(body, 10, 64)
	if err != nil || seq < 0 {
		return 0, fmt.Errorf("%w: %q", ErrBadCursor, s)
	}
	return seq, nil
}
