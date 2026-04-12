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
	Scope   string `json:"s"` // hex hash of sort column definition (mandatory)
	Context string `json:"c"` // query context fingerprint (mandatory)
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
		// Marshal failure is a server-side programming error (un-serializable
		// values in Cursor.Values), not a client-provided bad cursor. Use
		// ErrInternal so it maps to HTTP 500, not 400.
		return "", errcode.Wrap(errcode.ErrInternal, "cursor: marshal failed", err)
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
		return Cursor{}, cursorInvalid("cursor token is empty")
	}

	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return Cursor{}, cursorInvalid("invalid base64 encoding")
	}

	dotIdx := bytes.LastIndexByte(raw, '.')
	if dotIdx < 0 || dotIdx >= len(raw)-1 {
		return Cursor{}, cursorInvalid("missing signature separator")
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
		return Cursor{}, cursorInvalid("signature verification failed")
	}

	var cur Cursor
	if err := json.Unmarshal(payload, &cur); err != nil {
		return Cursor{}, cursorInvalid("invalid payload")
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

// cursorInvalidMsg is the stable, client-facing message for all cursor
// validation failures. Specific diagnostics go into errcode.Error.Details
// so they appear in the response "details" field without polluting "message".
const cursorInvalidMsg = "invalid cursor; restart from first page"

// cursorInvalid returns a standardized cursor error with a stable client-facing
// message and diagnostic reason in the details field.
func cursorInvalid(reason string) *errcode.Error {
	return errcode.WithDetails(
		errcode.New(errcode.ErrCursorInvalid, cursorInvalidMsg),
		map[string]any{"reason": reason})
}

// cursorInvalidExtra returns a standardized cursor error with extra diagnostic
// key-value pairs merged into the details alongside the reason.
func cursorInvalidExtra(reason string, extra map[string]any) *errcode.Error {
	details := map[string]any{"reason": reason}
	for k, v := range extra {
		details[k] = v
	}
	return errcode.WithDetails(
		errcode.New(errcode.ErrCursorInvalid, cursorInvalidMsg),
		details)
}

// ValidateCursorScope checks that the decoded cursor carries the expected sort
// scope and query context, and that the value count matches. Both fields are
// mandatory on both sides — empty scope/context is rejected regardless of
// whether it comes from the cursor or from the caller.
func ValidateCursorScope(cur Cursor, sort []SortColumn, queryCtx string) error {
	if cur.Scope == "" {
		return cursorInvalid("sort scope is required")
	}
	if expected := SortScope(sort); cur.Scope != expected {
		return cursorInvalidExtra("sort scope mismatch",
			map[string]any{"got": cur.Scope, "want": expected})
	}
	if cur.Context == "" {
		return cursorInvalid("query context is required")
	}
	if cur.Context != queryCtx {
		return cursorInvalidExtra("query context mismatch",
			map[string]any{"got": cur.Context, "want": queryCtx})
	}
	if len(cur.Values) != len(sort) {
		return cursorInvalid(fmt.Sprintf("has %d values but expected %d sort columns", len(cur.Values), len(sort)))
	}
	return nil
}
