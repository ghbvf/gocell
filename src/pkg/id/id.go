// Package id provides cryptographically random ID generation for the GoCell
// framework. All entity IDs should use this package instead of time-based
// approaches (e.g., UnixNano) to avoid collisions and predictability.
package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// New generates a random ID with the given prefix.
// Format: "{prefix}-{16 hex chars}" (8 random bytes = 64 bits of entropy).
// Example: New("usr") → "usr-a1b2c3d4e5f67890"
func New(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("id: crypto/rand failed: %v", err))
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}
