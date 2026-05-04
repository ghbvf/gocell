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
// their injected clock.Clock through [Config].
type Clock interface {
	Now() time.Time
}

// Config contains all required SecureCookie construction inputs.
type Config struct {
	// HashKey is the HMAC-SHA256 signing key. It must be at least 32 bytes.
	HashKey []byte
	// BlockKey optionally enables AES-GCM encryption. nil means signing only;
	// non-nil values must be 16, 24, or 32 bytes.
	BlockKey []byte
	// Clock is required for timestamps and expiry checks.
	Clock Clock
	// MaxAge is required and must be greater than zero.
	MaxAge int
}

// SecureCookie encodes and decodes cookie values with HMAC-SHA256 signing
// and optional AES-GCM encryption.
type SecureCookie struct {
	hashKey []byte      // HMAC-SHA256 signing key (required, ≥32 bytes)
	aead    cipher.AEAD // AES-GCM AEAD (nil if no encryption)
	maxAge  int         // max cookie age in seconds (required, > 0)
	clock   Clock       // clock used for encoding timestamps and expiry checks (always non-nil after New)
}

const (
	timestampLen = 8  // unix seconds, big-endian
	nonceLen     = 12 // AES-GCM nonce
	macLen       = 32 // HMAC-SHA256
	minHashKey   = 32
)

var (
	ErrHashKeyTooShort = errcode.New(errcode.KindInvalid, errcode.ErrSecureCookieHashKeyTooShort,
		"securecookie: hashKey must be at least 32 bytes")
	ErrInvalidBlockKey = errcode.New(errcode.KindInvalid, errcode.ErrSecureCookieInvalidBlockKey,
		"securecookie: blockKey must be 16, 24, or 32 bytes (or nil)")
	ErrEncodingTooShort = errcode.New(errcode.KindInvalid, errcode.ErrSecureCookieEncodingTooShort,
		"securecookie: encoded value too short")
	ErrHMACInvalid = errcode.New(errcode.KindInvalid, errcode.ErrSecureCookieHMACInvalid,
		"securecookie: HMAC verification failed")
	ErrExpired = errcode.New(errcode.KindInvalid, errcode.ErrSecureCookieExpired,
		"securecookie: cookie has expired")
	ErrDecryptFailed = errcode.New(errcode.KindInvalid, errcode.ErrSecureCookieDecryptFailed,
		"securecookie: decryption failed")
	ErrClockRequired = errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"securecookie: clock is required (nil or typed-nil rejected)")
	ErrMaxAgeRequired = errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"securecookie: MaxAge must be greater than zero")
)

// New creates a SecureCookie from cfg. Returns ErrClockRequired if cfg.Clock is
// nil or a typed-nil (interface wrapping a nil pointer) — making this the
// single, error-style fail-fast point for clock injection (no panic anywhere in
// the package).
//
// HashKey is required (min 32 bytes). BlockKey may be nil (signing only) or
// 16/24/32 bytes (AES-128/192/256-GCM). Clock is required: pass the caller's
// injected clock.Clock at the composition root, or a clockmock.FakeClock in
// tests. MaxAge is required and must be greater than zero.
func New(cfg Config) (*SecureCookie, error) {
	if len(cfg.HashKey) < minHashKey {
		return nil, ErrHashKeyTooShort
	}
	if err := validateClock(cfg.Clock); err != nil {
		return nil, err
	}
	if cfg.MaxAge <= 0 {
		return nil, ErrMaxAgeRequired
	}
	if err := validateBlockKey(cfg.BlockKey); err != nil {
		return nil, err
	}

	// Deep copy hashKey to prevent caller mutation.
	hk := make([]byte, len(cfg.HashKey))
	copy(hk, cfg.HashKey)

	sc := &SecureCookie{
		hashKey: hk,
		maxAge:  cfg.MaxAge,
		clock:   cfg.Clock,
	}

	if cfg.BlockKey != nil {
		block, err := aes.NewCipher(cfg.BlockKey)
		if err != nil {
			return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrSecureCookieInvalidBlockKey,
				"securecookie: AES init failed", err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrSecureCookieInvalidBlockKey,
				"securecookie: GCM init failed", err)
		}
		sc.aead = aead
	}

	return sc, nil
}

// validateClock returns ErrClockRequired if clk is nil or a typed-nil
// (interface wrapping a nil pointer of any reference kind).
func validateClock(clk Clock) error {
	if clk == nil {
		return ErrClockRequired
	}
	v := reflect.ValueOf(clk)
	switch v.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Chan, reflect.Func, reflect.Slice, reflect.Interface:
		if v.IsNil() {
			return ErrClockRequired
		}
	}
	return nil
}

func validateBlockKey(blockKey []byte) error {
	if blockKey == nil {
		return nil
	}
	switch len(blockKey) {
	case 16, 24, 32:
		return nil
	default:
		return ErrInvalidBlockKey
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
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed,
			"securecookie: invalid encoded value", err)
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
	rawTS := binary.BigEndian.Uint64(ts)
	if rawTS > uint64(math.MaxInt64) {
		return nil, ErrExpired
	}
	created := int64(rawTS)
	if sc.clock.Now().Unix()-created >= int64(sc.maxAge) {
		return nil, ErrExpired
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
