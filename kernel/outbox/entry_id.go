package outbox

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// EntryIDPrefix is the prefix for all outbox entry IDs — distinguishes
// entry IDs from other UUID-based IDs (audit entries, sessions, etc.) in logs.
const EntryIDPrefix = "evt-"

// NewEntryID returns a new outbox.Entry.ID value in the canonical format
// "evt-<hex32>" (RFC 4122 v4-style UUID, serialized as 32 hex chars without
// dashes). Centralized so the prefix stays consistent across all slices that
// write to the outbox.
//
// Uses crypto/rand directly (not github.com/google/uuid) because kernel/ may
// only depend on the Go standard library per CLAUDE.md's layering rule.
//
// Returns the wrapped crypto/rand.Read error if the OS entropy source is
// broken; callers that prefer fail-fast wiring can use MustNewEntryID.
func NewEntryID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("outbox: crypto/rand.Read failed: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122
	return EntryIDPrefix + hex.EncodeToString(b[:]), nil
}

// MustNewEntryID is the panic-on-error variant of NewEntryID. crypto/rand.Read
// only fails on broken OS entropy, which is non-recoverable, so most call
// sites use this Must variant; outbox writers that want to surface the error
// to upstream tx callers should use NewEntryID directly.
func MustNewEntryID() string {
	id, err := NewEntryID()
	if err != nil {
		panic(errcode.Assertion("outbox: entry id: %v", err))
	}
	return id
}
