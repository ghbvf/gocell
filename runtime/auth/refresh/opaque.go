package refresh

import (
	"encoding/base64"
	"io"
	"strings"
)

// SelectorLen is the raw byte length of the selector half.
//
// 16 bytes = 128 bits of search space. Collision probability across a
// population of N live tokens ≈ N² / 2^129 — for any realistic N this is
// negligible. Matches the OWASP selector+verifier pattern recommendation.
const SelectorLen = 16

// VerifierLen is the raw byte length of the verifier half.
//
// 32 bytes = 256 bits of entropy; preimage resistance of SHA-256(verifier)
// against a DB snapshot is 2^256. Matches ory/fosite minimum entropy.
const VerifierLen = 32

// WireLen is the deterministic length of the encoded wire token:
// base64url_nopad(16B) = 22 chars, "." = 1, base64url_nopad(32B) = 43 chars.
const WireLen = 22 + 1 + 43

// wireSeparator is the delimiter between selector and verifier halves.
// Matches ory/fosite token/hmac/hmacsha.go convention.
const wireSeparator = "."

// GeneratePair reads SelectorLen + VerifierLen random bytes from rand.
// Returns distinct buffers; never returns the same buffer twice.
//
// Both memstore and the postgres adapter delegate to this helper (F10) so
// the generation logic lives in exactly one place.
func GeneratePair(rand io.Reader) (selector, verifier []byte, err error) {
	sel := make([]byte, SelectorLen)
	ver := make([]byte, VerifierLen)
	if _, err := io.ReadFull(rand, sel); err != nil {
		return nil, nil, err
	}
	if _, err := io.ReadFull(rand, ver); err != nil {
		return nil, nil, err
	}
	return sel, ver, nil
}

// EncodeOpaque renders selector || separator || verifier as a URL-safe,
// base64 no-padding string. Callers hand this to clients verbatim.
//
// Panics if either half is nil — callers must have both halves from
// crypto/rand before reaching this point.
func EncodeOpaque(selector, verifier []byte) string {
	enc := base64.RawURLEncoding
	return enc.EncodeToString(selector) + wireSeparator + enc.EncodeToString(verifier)
}

// ParseOpaque is the strict inverse of EncodeOpaque. Returns ok=false for
// any deviation — wrong number of dots, wrong base64, wrong length for
// either half. Callers map !ok to ErrRejected with slog reason "malformed".
//
// Rationale: a single uniform rejection path for every shape defect makes
// parse failures indistinguishable from unknown-selector DB misses at both
// the error-shape level (both return ErrRejected) and the code-path level
// (both perform no DB traffic before deciding).
func ParseOpaque(s string) (selector, verifier []byte, ok bool) {
	if len(s) != WireLen {
		return nil, nil, false
	}
	selStr, verStr, found := strings.Cut(s, wireSeparator)
	if !found {
		return nil, nil, false
	}
	enc := base64.RawURLEncoding
	selB, e1 := enc.DecodeString(selStr)
	verB, e2 := enc.DecodeString(verStr)
	if e1 != nil || e2 != nil {
		return nil, nil, false
	}
	if len(selB) != SelectorLen || len(verB) != VerifierLen {
		return nil, nil, false
	}
	return selB, verB, true
}
