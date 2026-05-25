// Package idutil provides shared ID validation and generation for
// observability-safe identifiers across kernel/ and runtime/ layers.
//
// ref: go.opentelemetry.io/otel/trace — IsValid() bool hot-path pattern
// ref: k8s.io/apimachinery/pkg/util/validation — exported length constants
package idutil

import (
	"crypto/rand"
	"fmt"
	"io"
)

const (
	// MaxHTTPIDLen is the maximum length for HTTP-borne correlation IDs
	// (RequestID, CorrelationID, et al). Length enforcement happens at the
	// HTTP boundary (handler/middleware) alongside IsSafeID; pkg/redaction
	// only masks values, it does not validate length.
	MaxHTTPIDLen = 128

	// MaxMetadataIDLen is the maximum length for broker metadata IDs
	// (observability metadata restored from async messages).
	MaxMetadataIDLen = 256
)

// IsSafeID reports whether s is non-empty and every byte is in the safe set
// for observability IDs: ASCII letters, digits, and the separators ._:/-
//
// Length checking is intentionally excluded — callers enforce their own limits
// via MaxHTTPIDLen or MaxMetadataIDLen.
//
// Typical usage:
//
//	if len(id) > idutil.MaxHTTPIDLen || !idutil.IsSafeID(id) { /* reject */ }
func IsSafeID(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == ':' || c == '/' || c == '-':
		default:
			return false
		}
	}
	return true
}

// NewUUID generates a UUID v4 string from crypto/rand.Reader via io.ReadFull.
// In Go 1.24+ crypto/rand.Reader.Read calls runtime.fatal on OS entropy
// failure rather than returning an error, so the error return is unreachable
// for the production path; keeping it surfaces a future stdlib contract
// change rather than swallowing it.
func NewUUID() (string, error) { return newUUID(rand.Reader) }

func newUUID(r io.Reader) (string, error) {
	var buf [16]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return "", fmt.Errorf("idutil: read entropy: %w", err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}
