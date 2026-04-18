package outbox

import (
	"crypto/rand"
	"encoding/hex"
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
func NewEntryID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read only returns an error on broken entropy sources,
		// which is non-recoverable. Panic is consistent with stdlib uuid libs.
		panic("outbox: crypto/rand.Read failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122
	return EntryIDPrefix + hex.EncodeToString(b[:])
}
