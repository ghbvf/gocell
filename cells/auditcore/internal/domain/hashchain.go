package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// HashChain maintains an append-only, HMAC-linked chain of AuditEntry records.
// Each entry's Hash is HMAC-SHA256(prevHash + entry fields) using the chain's
// symmetric key, providing tamper evidence.
type HashChain struct {
	hmacKey []byte
	entries []*AuditEntry
}

// NewHashChain creates a HashChain with the given HMAC key.
func NewHashChain(hmacKey []byte) *HashChain {
	return &HashChain{
		hmacKey: hmacKey,
		entries: make([]*AuditEntry, 0),
	}
}

// Append adds a new entry to the chain. It computes the HMAC-SHA256 hash
// linking this entry to its predecessor and returns the resulting AuditEntry.
func (hc *HashChain) Append(eventID, eventType, actorID string, payload []byte) *AuditEntry {
	prevHash := ""
	if len(hc.entries) > 0 {
		prevHash = hc.entries[len(hc.entries)-1].Hash
	}

	entry := &AuditEntry{
		EventID:   eventID,
		EventType: eventType,
		ActorID:   actorID,
		Timestamp: time.Now(),
		Payload:   payload,
		PrevHash:  prevHash,
	}

	entry.Hash = hc.computeHash(entry)
	hc.entries = append(hc.entries, entry)
	return entry
}

// Verify checks the integrity of the given entries slice by recomputing each
// HMAC and verifying the chain linkage. It returns true if all entries are
// valid, or false with the index of the first invalid entry.
func (hc *HashChain) Verify(entries []*AuditEntry) (valid bool, firstInvalidIndex int) {
	for i, entry := range entries {
		expectedPrevHash := ""
		if i > 0 {
			expectedPrevHash = entries[i-1].Hash
		}

		if entry.PrevHash != expectedPrevHash {
			return false, i
		}

		expectedHash := hc.computeHash(entry)
		if entry.Hash != expectedHash {
			return false, i
		}
	}
	return true, -1
}

// Len returns the number of entries in the chain.
func (hc *HashChain) Len() int {
	return len(hc.entries)
}

// computeHash produces the HMAC-SHA256 hex digest for an entry.
// The message is: prevHash + eventID + eventType + actorID + timestamp + payload.
func (hc *HashChain) computeHash(entry *AuditEntry) string {
	mac := hmac.New(sha256.New, hc.hmacKey)
	fmt.Fprintf(mac, "%s|%s|%s|%s|%d|%s", //nolint:errcheck // hash.Hash.Write never returns error
		entry.PrevHash,
		entry.EventID,
		entry.EventType,
		entry.ActorID,
		entry.Timestamp.UnixNano(),
		string(entry.Payload),
	)
	return hex.EncodeToString(mac.Sum(nil))
}
