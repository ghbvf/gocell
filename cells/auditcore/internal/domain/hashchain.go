package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// minHMACKeyBytes is the smallest HMAC-SHA256 key the audit chain accepts.
// Keys shorter than the hash output (32 bytes) make the HMAC strength itself
// the weakest link of the audit chain, which violates RFC 2104 §3 and
// NIST SP 800-107 / FIPS 198-1 — they are rejected at construction time.
//
// INVARIANT: AUDIT-HMAC-KEY-MINLEN-01
// Enforcement: Go type system — NewHashChain returns (*HashChain, error),
// and every caller (cell.Init → auditappend.NewService / auditverify.NewService)
// is forced to handle the error. Per CLAUDE.md "新增 invariant 决策原则" step 2
// (type system 自然拦), no archtest layer is added: no caller can construct a
// HashChain without going through this function. Regression test:
// TestNewHashChain_KeyLength (table-driven) + TestAuditCore_HMACKeyTooShort
// (end-to-end propagation through the slice wrapper).
const minHMACKeyBytes = 32

// HashChain maintains an append-only, HMAC-linked chain of AuditEntry records.
// Each entry's Hash is HMAC-SHA256(prevHash + entry fields) using the chain's
// symmetric key, providing tamper evidence.
type HashChain struct {
	hmacKey []byte
	entries []*AuditEntry
}

// NewHashChain creates a HashChain with the given HMAC key.
//
// HMAC-SHA256 keys must be at least 32 bytes (RFC 2104 §3, NIST SP 800-107):
// keys shorter than the hash output collapse HMAC's security strength, so this
// function returns errcode.ErrValidationFailed with minimumBytes / actualBytes
// details (the key bytes themselves are never echoed back).
func NewHashChain(hmacKey []byte) (*HashChain, error) {
	if len(hmacKey) < minHMACKeyBytes {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit hmac key too short",
			errcode.WithDetails(
				slog.Int("minimumBytes", minHMACKeyBytes),
				slog.Int("actualBytes", len(hmacKey)),
			))
	}
	return &HashChain{
		hmacKey: hmacKey,
		entries: make([]*AuditEntry, 0),
	}, nil
}

// Append adds a new entry to the chain. It computes the HMAC-SHA256 hash
// linking this entry to its predecessor and returns the resulting AuditEntry.
// now is the wall-clock instant provided by the caller's clock.Clock.
func (hc *HashChain) Append(eventID, eventType, actorID string, payload []byte, now time.Time) *AuditEntry {
	prevHash := ""
	if len(hc.entries) > 0 {
		prevHash = hc.entries[len(hc.entries)-1].Hash
	}

	entry := &AuditEntry{
		EventID:   eventID,
		EventType: eventType,
		ActorID:   actorID,
		Timestamp: now,
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
	msg := fmt.Sprintf("%s|%s|%s|%s|%d|%s",
		entry.PrevHash,
		entry.EventID,
		entry.EventType,
		entry.ActorID,
		entry.Timestamp.UnixNano(),
		string(entry.Payload),
	)
	// hash.Hash.Write per the io.Writer contract (and the package doc) always
	// returns (len(b), nil); the discarded return matches the stdlib-documented
	// hmac idiom (see crypto/hmac godoc Example). errcheck is configured to
	// skip this method globally — see .golangci.yml errcheck.exclude-functions.
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}
