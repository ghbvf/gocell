package query

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// minCursorKeyBytes is the minimum HMAC key length for cursor signing.
const minCursorKeyBytes = 32

// Cursor holds the keyset values at a pagination boundary.
// Values correspond 1:1 with the SortColumns of the query.
type Cursor struct {
	Values  []any  `json:"v"`
	Scope   string `json:"s,omitempty"` // hex hash of sort column definition
	Context string `json:"c,omitempty"` // query context fingerprint (path + filters)
}

// CursorCodec encodes and decodes cursors with HMAC-SHA256 tamper protection.
// The HMAC key must be at least 32 bytes. Supports key rotation: verification
// tries the current key first, then the previous key (if set).
type CursorCodec struct {
	current  []byte
	previous []byte // may be nil for single-key mode
}

// NewCursorCodec creates a CursorCodec. current is used for signing;
// verification tries current first, then previous (if set).
// Both keys must be at least 32 bytes.
func NewCursorCodec(current []byte, previous ...[]byte) (*CursorCodec, error) {
	if len(current) < minCursorKeyBytes {
		return nil, errcode.New(errcode.ErrCursorInvalid,
			fmt.Sprintf("cursor HMAC key is %d bytes, minimum is %d", len(current), minCursorKeyBytes))
	}
	var prev []byte
	if len(previous) > 0 && len(previous[0]) > 0 {
		prev = previous[0]
		if len(prev) < minCursorKeyBytes {
			return nil, errcode.New(errcode.ErrCursorInvalid,
				fmt.Sprintf("previous cursor HMAC key is %d bytes, minimum is %d", len(prev), minCursorKeyBytes))
		}
	}
	return &CursorCodec{current: current, previous: prev}, nil
}

// Encode serializes a Cursor into an opaque, HMAC-signed token.
// Wire format: base64url(json_bytes + "." + hex(hmac-sha256(json_bytes)))
func (c *CursorCodec) Encode(cur Cursor) (string, error) {
	payload, err := json.Marshal(cur)
	if err != nil {
		return "", errcode.Wrap(errcode.ErrCursorInvalid, "cursor: marshal failed", err)
	}

	sig := c.signWith(c.current, payload)
	raw := make([]byte, 0, len(payload)+1+len(sig))
	raw = append(raw, payload...)
	raw = append(raw, '.')
	raw = append(raw, sig...)

	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Decode verifies and deserializes an opaque cursor token.
// Returns ErrCursorInvalid on any failure (tamper, format, etc.).
func (c *CursorCodec) Decode(token string) (Cursor, error) {
	if token == "" {
		return Cursor{}, errcode.New(errcode.ErrCursorInvalid, "cursor token is empty")
	}

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, errcode.New(errcode.ErrCursorInvalid, "cursor: invalid base64 encoding")
	}

	dotIdx := bytes.LastIndexByte(raw, '.')
	if dotIdx < 0 || dotIdx >= len(raw)-1 {
		return Cursor{}, errcode.New(errcode.ErrCursorInvalid, "cursor: missing signature separator")
	}

	payload := raw[:dotIdx]
	sigHex := raw[dotIdx+1:]

	// Try current key first, then previous (key rotation support).
	keys := [][]byte{c.current}
	if len(c.previous) > 0 {
		keys = append(keys, c.previous)
	}

	var verified bool
	for _, key := range keys {
		expected := c.signWith(key, payload)
		if hmac.Equal(sigHex, expected) {
			verified = true
			break
		}
	}
	if !verified {
		return Cursor{}, errcode.New(errcode.ErrCursorInvalid, "cursor: signature verification failed")
	}

	var cur Cursor
	if err := json.Unmarshal(payload, &cur); err != nil {
		return Cursor{}, errcode.New(errcode.ErrCursorInvalid, "cursor: invalid payload")
	}

	return cur, nil
}

// signWith computes the hex-encoded HMAC-SHA256 of data using the given key.
func (c *CursorCodec) signWith(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	sum := mac.Sum(nil)
	dst := make([]byte, hex.EncodedLen(len(sum)))
	hex.Encode(dst, sum)
	return dst
}

// SortScope computes a short hex fingerprint of the sort column definition.
// Cursors are only valid for queries with the same sort scope.
func SortScope(cols []SortColumn) string {
	h := sha256.New()
	for _, c := range cols {
		h.Write([]byte(c.Name))
		h.Write([]byte{0})
		h.Write([]byte(c.Direction))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16] // 8-byte prefix is sufficient
}

// QueryContext computes a fingerprint of the query context (endpoint identity
// + filter parameters). Cursors are only valid for the same query context.
// Pass key-value pairs describing the query identity, e.g.:
//
//	QueryContext("endpoint", "order-query")
//	QueryContext("endpoint", "audit-query", "eventType", "login", "actorId", "user-1")
//	QueryContext("endpoint", "device-command", "deviceId", "dev-1")
func QueryContext(pairs ...string) string {
	h := sha256.New()
	for _, p := range pairs {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ValidateCursorScope checks that the decoded cursor carries the expected sort
// scope and query context, and that the value count matches. Scope and context
// are mandatory — cursors missing either field are rejected.
func ValidateCursorScope(cur Cursor, sort []SortColumn, queryCtx string) error {
	if cur.Scope != SortScope(sort) {
		return errcode.New(errcode.ErrCursorInvalid, "cursor: sort scope mismatch")
	}
	if cur.Context != queryCtx {
		return errcode.New(errcode.ErrCursorInvalid, "cursor: query context mismatch")
	}
	if len(cur.Values) != len(sort) {
		return errcode.New(errcode.ErrCursorInvalid,
			fmt.Sprintf("cursor: has %d values but expected %d for sort columns", len(cur.Values), len(sort)))
	}
	return nil
}
