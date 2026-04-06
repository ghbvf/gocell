// Package uid provides cryptographically random UUID v4 generation for the
// GoCell framework. All entity IDs should use this package to ensure
// unpredictability and collision resistance (128-bit entropy).
//
// This replaces the previous pkg/id package which used only 64-bit entropy.
package uid

import (
	"crypto/rand"
	"fmt"
)

// New generates a cryptographically random UUID v4 string.
// Format: "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx" (RFC 4122 version 4).
func New() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("uid: crypto/rand failed: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// NewWithPrefix generates a cryptographically random UUID v4 string with a
// domain prefix for readability.
// Format: "{prefix}-xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".
func NewWithPrefix(prefix string) string {
	return prefix + "-" + New()
}
