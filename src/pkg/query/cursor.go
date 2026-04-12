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
	Values []any `json:"v"`
}

// CursorCodec encodes and decodes cursors with HMAC-SHA256 tamper protection.
// The HMAC key must be at least 32 bytes.
type CursorCodec struct {
	key []byte
}

// NewCursorCodec creates a CursorCodec with the given HMAC signing key.
// The key must be at least 32 bytes.
func NewCursorCodec(hmacKey []byte) (*CursorCodec, error) {
	if len(hmacKey) < minCursorKeyBytes {
		return nil, errcode.New(errcode.ErrCursorInvalid,
			fmt.Sprintf("cursor HMAC key is %d bytes, minimum is %d", len(hmacKey), minCursorKeyBytes))
	}
	return &CursorCodec{key: hmacKey}, nil
}

// Encode serializes a Cursor into an opaque, HMAC-signed token.
// Wire format: base64url(json_bytes + "." + hex(hmac-sha256(json_bytes)))
func (c *CursorCodec) Encode(cur Cursor) (string, error) {
	payload, err := json.Marshal(cur)
	if err != nil {
		return "", errcode.Wrap(errcode.ErrCursorInvalid, "cursor: marshal failed", err)
	}

	sig := c.sign(payload)
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

	expected := c.sign(payload)
	if !hmac.Equal(sigHex, expected) {
		return Cursor{}, errcode.New(errcode.ErrCursorInvalid, "cursor: signature verification failed")
	}

	var cur Cursor
	if err := json.Unmarshal(payload, &cur); err != nil {
		return Cursor{}, errcode.New(errcode.ErrCursorInvalid, "cursor: invalid payload")
	}

	return cur, nil
}

// sign computes the hex-encoded HMAC-SHA256 of data.
func (c *CursorCodec) sign(data []byte) []byte {
	mac := hmac.New(sha256.New, c.key)
	mac.Write(data)
	sum := mac.Sum(nil)
	dst := make([]byte, hex.EncodedLen(len(sum)))
	hex.Encode(dst, sum)
	return dst
}
