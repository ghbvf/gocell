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
	"fmt"
	"io"
	"math"
	"reflect"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Clock abstracts the wall-clock reads this package needs to time-stamp
// cookies and check expiry. It is intentionally local: pkg/ may not import
// kernel/, so SecureCookie defines the minimal Now() interface that any
// kernel/clock.Clock satisfies structurally. Callers in higher layers pass
// their injected clock.Clock directly via [SecureCookie.WithClock].
type Clock interface {
	Now() time.Time
}

// realClock is the default Clock implementation. It is exported as a private
// zero-value singleton so [New] can stamp a non-nil clock without forcing
// every caller to pass one.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// MustHaveClock panics when clk is nil or a typed-nil (interface wrapping a
// nil pointer). pkg/ may not import kernel/clock, so this is a local equivalent
// of clock.MustHaveClock with the same semantics.
func MustHaveClock(clk Clock) {
	if clk == nil {
		panic("securecookie.WithClock: Clock is required (nil rejected); pass a real clock or clockmock.New in tests")
	}
	v := reflect.ValueOf(clk)
	switch v.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Chan, reflect.Func, reflect.Slice, reflect.Interface:
		if v.IsNil() {
			panic("securecookie.WithClock: Clock is required (typed-nil rejected); pass a real clock or clockmock.New in tests")
		}
	}
}

// SecureCookie encodes and decodes cookie values with HMAC-SHA256 signing
// and optional AES-GCM encryption.
type SecureCookie struct {
	hashKey []byte      // HMAC-SHA256 signing key (required, ≥32 bytes)
	aead    cipher.AEAD // AES-GCM AEAD (nil if no encryption)
	maxAge  int         // max cookie age in seconds (0 = no expiry check)
	clock   Clock       // clock used for encoding timestamps and expiry checks
}

const (
	timestampLen = 8  // unix seconds, big-endian
	nonceLen     = 12 // AES-GCM nonce
	macLen       = 32 // HMAC-SHA256
	minHashKey   = 32
)

var (
	ErrHashKeyTooShort  = errcode.New(errcode.ErrSecureCookieHashKeyTooShort, "securecookie: hashKey must be at least 32 bytes")
	ErrInvalidBlockKey  = errcode.New(errcode.ErrSecureCookieInvalidBlockKey, "securecookie: blockKey must be 16, 24, or 32 bytes (or nil)")
	ErrEncodingTooShort = errcode.New(errcode.ErrSecureCookieEncodingTooShort, "securecookie: encoded value too short")
	ErrHMACInvalid      = errcode.New(errcode.ErrSecureCookieHMACInvalid, "securecookie: HMAC verification failed")
	ErrExpired          = errcode.New(errcode.ErrSecureCookieExpired, "securecookie: cookie has expired")
	ErrDecryptFailed    = errcode.New(errcode.ErrSecureCookieDecryptFailed, "securecookie: decryption failed")
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
		clock:   realClock{},
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
		clock:   sc.clock,
	}
}

// WithClock returns a copy of sc using the given clock for timestamps and
// expiry checks. Key material is deep-copied. clk must not be nil or a typed-nil;
// pass a real clock implementation (e.g. the caller's injected clock or clockmock.New
// in tests). Panics on nil or typed-nil to fail fast at construction time.
//
// Any kernel/clock.Clock satisfies the local Clock interface structurally;
// higher layers pass their injected clock.Clock here without explicit
// conversion. Tests that need deterministic time inject a clockmock.FakeClock
// the same way.
func (sc *SecureCookie) WithClock(clk Clock) *SecureCookie {
	MustHaveClock(clk)
	hk := make([]byte, len(sc.hashKey))
	copy(hk, sc.hashKey)
	return &SecureCookie{
		hashKey: hk,
		aead:    sc.aead,
		maxAge:  sc.maxAge,
		clock:   clk,
	}
}

// Encode signs (and optionally encrypts) value, returning a base64url string.
//
// Format: base64url( timestamp(8) | [nonce(12) | ciphertext(N)] or payload(N) | hmac(32) )
// HMAC input: len(name)(4) | name | timestamp | nonce | payload.
func (sc *SecureCookie) Encode(name string, value []byte) (string, error) {
	// 1. Timestamp
	ts := make([]byte, timestampLen)
	now := sc.clock.Now().Unix()
	if now < 0 {
		// Pre-1970 clock — treat as zero so the encoded value remains parseable.
		now = 0
	}
	binary.BigEndian.PutUint64(ts, uint64(now))

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
		raw := binary.BigEndian.Uint64(ts)
		if raw > uint64(math.MaxInt64) {
			return nil, ErrExpired
		}
		created := int64(raw)
		if sc.clock.Now().Unix()-created >= int64(sc.maxAge) {
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
	// 4 GiB cookie name is unreachable in practice; encode the length one
	// big-endian byte at a time so we never need an int→uint32 cast that
	// gosec G115 would flag. len() never returns a negative int by the Go
	// spec, so only the upper bound needs a guard.
	n := len(name)
	if n > math.MaxUint32 {
		n = math.MaxUint32
	}
	nameLen[0] = byte(n >> 24 & 0xff)
	nameLen[1] = byte(n >> 16 & 0xff)
	nameLen[2] = byte(n >> 8 & 0xff)
	nameLen[3] = byte(n & 0xff)
	h.Write(nameLen)
	h.Write([]byte(name))
	h.Write(ts)
	if nonce != nil {
		h.Write(nonce)
	}
	h.Write(payload)
	return h.Sum(nil)
}
