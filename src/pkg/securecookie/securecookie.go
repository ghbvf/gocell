// Package securecookie encodes and decodes cookie values with HMAC-SHA256
// signing and optional AES-GCM encryption using Go standard library crypto only.
//
// ref: gorilla/securecookie — HMAC+AES codec pattern
// ref: gofiber/fiber — stdlib-only cookie security approach
package securecookie

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

// SecureCookie encodes and decodes cookie values with HMAC-SHA256 signing
// and optional AES-GCM encryption.
type SecureCookie struct {
	hashKey []byte      // HMAC-SHA256 signing key (required, ≥32 bytes)
	aead    cipher.AEAD // AES-GCM AEAD (nil if no encryption)
	maxAge  int         // max cookie age in seconds (0 = no expiry check)
}

const (
	timestampLen = 8  // unix seconds, big-endian
	nonceLen     = 12 // AES-GCM nonce
	macLen       = 32 // HMAC-SHA256
	minHashKey   = 32
)

var (
	ErrHashKeyTooShort  = errors.New("securecookie: hashKey must be at least 32 bytes")
	ErrInvalidBlockKey  = errors.New("securecookie: blockKey must be 16, 24, or 32 bytes (or nil)")
	ErrEncodingTooShort = errors.New("securecookie: encoded value too short")
	ErrHMACInvalid      = errors.New("securecookie: HMAC verification failed")
	ErrExpired          = errors.New("securecookie: cookie has expired")
	ErrDecryptFailed    = errors.New("securecookie: decryption failed")
)

// New creates a SecureCookie with the given hash key and optional block key.
// hashKey is required (min 32 bytes). blockKey may be nil (signing only)
// or 16/24/32 bytes (AES-128/192/256-GCM).
func New(hashKey, blockKey []byte) (*SecureCookie, error) {
	if len(hashKey) < minHashKey {
		return nil, ErrHashKeyTooShort
	}

	// Deep copy hashKey to prevent caller mutation.
	hk := make([]byte, len(hashKey))
	copy(hk, hashKey)

	sc := &SecureCookie{
		hashKey: hk,
		maxAge:  86400, // default 24h
	}

	if blockKey != nil {
		block, err := aes.NewCipher(blockKey)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidBlockKey, err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("securecookie: GCM init: %w", err)
		}
		sc.aead = aead
	}

	return sc, nil
}

// WithMaxAge returns a copy of sc with the given max age in seconds.
// 0 means no expiry check. Key material is deep-copied.
func (sc *SecureCookie) WithMaxAge(seconds int) *SecureCookie {
	hk := make([]byte, len(sc.hashKey))
	copy(hk, sc.hashKey)
	return &SecureCookie{
		hashKey: hk,
		aead:    sc.aead, // cipher.AEAD is safe to share (immutable after init)
		maxAge:  seconds,
	}
}

// Encode signs (and optionally encrypts) value, returning a base64url string.
//
// Format: base64url( timestamp(8) | [nonce(12) | ciphertext(N)] or payload(N) | hmac(32) )
// HMAC input: len(name)(4) | name | timestamp | nonce | payload
func (sc *SecureCookie) Encode(name string, value []byte) (string, error) {
	// 1. Timestamp
	ts := make([]byte, timestampLen)
	binary.BigEndian.PutUint64(ts, uint64(time.Now().Unix()))

	// 2. Payload (optionally encrypted)
	var nonce []byte
	payload := value
	if sc.aead != nil {
		nonce = make([]byte, nonceLen)
		if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
			return "", fmt.Errorf("securecookie: rand nonce: %w", err)
		}
		payload = sc.aead.Seal(nil, nonce, value, ts)
	}

	// 3. HMAC over (len(name) | name | timestamp | nonce | payload)
	mac := sc.computeMAC(name, ts, nonce, payload)

	// 4. Assemble: timestamp | nonce | payload | mac
	total := timestampLen + len(nonce) + len(payload) + macLen
	buf := make([]byte, 0, total)
	buf = append(buf, ts...)
	buf = append(buf, nonce...)
	buf = append(buf, payload...)
	buf = append(buf, mac...)

	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Decode verifies signature, checks freshness, decrypts, and returns the
// original value.
func (sc *SecureCookie) Decode(name string, encoded string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("securecookie: base64: %w", err)
	}

	// Minimum length: timestamp + mac (no nonce, no payload for sign-only empty value)
	minLen := timestampLen + macLen
	if sc.aead != nil {
		minLen += nonceLen
	}
	if len(raw) < minLen {
		return nil, ErrEncodingTooShort
	}

	// Split components
	ts := raw[:timestampLen]
	macStart := len(raw) - macLen
	gotMAC := raw[macStart:]

	var nonce []byte
	var payload []byte
	if sc.aead != nil {
		nonce = raw[timestampLen : timestampLen+nonceLen]
		payload = raw[timestampLen+nonceLen : macStart]
	} else {
		payload = raw[timestampLen:macStart]
	}

	// 1. Verify HMAC (constant-time)
	expectedMAC := sc.computeMAC(name, ts, nonce, payload)
	if subtle.ConstantTimeCompare(gotMAC, expectedMAC) != 1 {
		return nil, ErrHMACInvalid
	}

	// 2. Check freshness
	if sc.maxAge > 0 {
		created := int64(binary.BigEndian.Uint64(ts))
		if time.Now().Unix()-created >= int64(sc.maxAge) {
			return nil, ErrExpired
		}
	}

	// 3. Decrypt (if encrypted)
	if sc.aead != nil {
		plaintext, err := sc.aead.Open(nil, nonce, payload, ts)
		if err != nil {
			return nil, ErrDecryptFailed
		}
		return plaintext, nil
	}

	return payload, nil
}

// computeMAC calculates HMAC-SHA256 over (len(name) | name | ts | nonce | payload).
// The 4-byte big-endian length prefix for name prevents cross-cookie MAC
// collisions where name1||ts1 == name2||ts2 for different name lengths.
func (sc *SecureCookie) computeMAC(name string, ts, nonce, payload []byte) []byte {
	h := hmac.New(sha256.New, sc.hashKey)
	nameLen := make([]byte, 4)
	binary.BigEndian.PutUint32(nameLen, uint32(len(name)))
	h.Write(nameLen)
	h.Write([]byte(name))
	h.Write(ts)
	if nonce != nil {
		h.Write(nonce)
	}
	h.Write(payload)
	return h.Sum(nil)
}
