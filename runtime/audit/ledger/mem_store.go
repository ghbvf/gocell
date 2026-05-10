package ledger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// MemStore is an in-memory Store implementation for dev and tests. It is not a
// production substrate — the PG-backed Store landing in S8+ owns the production
// path. Three properties follow from the dev/test scope and are documented
// design choices, not gaps:
//
//   - Single sync.Mutex over the entry slice. Append serializes under the
//     write lock to guarantee monotonic SeqNo assignment and correct chain
//     linkage. Acceptable at dev/test scale; PG handles concurrency via
//     pg_advisory_xact_lock + SELECT FOR UPDATE.
//   - No capacity ceiling. Memory is bounded by the test's entry count.
//   - No instrumentation. Observability is a cell-layer concern.
//
// Restart simulation: MemStore is ephemeral — entries are lost when the
// instance is discarded. The storetest Restart_Recovery case simulates restart
// by replaying entries from storeA into storeB via GetBySeq/Append; the PG
// store restores state from the DB on construction.
type MemStore struct {
	protocol *Protocol
	clock    clock.Clock

	mu           sync.Mutex
	entries      []*Entry          // indexed by SeqNo-1 (SeqNo starts at 1)
	fingerprints map[string]struct{} // content fingerprint → exists
}

// NewMemStore constructs a MemStore. Both protocol and clk are strong-
// dependency wiring (they are not replaceable defaults); typed-nil and bare
// nil are rejected at construction so misconfiguration surfaces at startup
// rather than at the first request.
//
// runtime-api.md §Option 范式分层 — one or two unconditional dependencies are
// passed positionally; Option pattern only becomes warranted at ≥ 3 deps or
// when an accumulator appears.
func NewMemStore(protocol *Protocol, clk clock.Clock) (*MemStore, error) {
	// protocol is *Protocol (concrete pointer): bare-nil check suffices, no
	// typed-nil interface risk. clk is the clock.Clock interface: typed-nil
	// is possible (var c clock.Clock; c is nil but rv.Type() != nil), so
	// validation.IsNilInterface is required.
	if protocol == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: NewMemStore requires non-nil Protocol")
	}
	if validation.IsNilInterface(clk) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: NewMemStore requires non-nil Clock")
	}
	return &MemStore{
		protocol:     protocol,
		clock:        clk,
		entries:      make([]*Entry, 0),
		fingerprints: make(map[string]struct{}),
	}, nil
}

// Append appends a new entry to the chain. It:
//  1. Validates the entry payload is valid JSON (strict mode).
//  2. Checks the content fingerprint for idempotency (ErrAuditLedgerAlreadyExists).
//  3. Computes PrevHash from the current tail.
//  4. Assigns the next SeqNo.
//  5. Computes Hash via Protocol.ComputeHash.
//  6. Persists the entry.
//
// Thread-safe: all state mutations are serialized under the write lock.
func (m *MemStore) Append(_ context.Context, e *Entry) error {
	if e == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: Append requires non-nil Entry")
	}

	// Strict payload validation: must be valid JSON (or nil/empty = null).
	if err := validatePayloadJSON(e.Payload); err != nil {
		return err
	}

	// Compute content fingerprint before acquiring the lock (pure CPU).
	fp := contentFingerprint(e)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Idempotency check.
	if _, exists := m.fingerprints[fp]; exists {
		return errcode.New(errcode.KindConflict, errcode.ErrAuditLedgerAlreadyExists,
			"audit ledger: duplicate content fingerprint")
	}

	// Determine PrevHash from the current tail.
	prevHash := ""
	if len(m.entries) > 0 {
		prevHash = m.entries[len(m.entries)-1].Hash
	}

	// Build the stored entry (copy to prevent caller mutations from leaking).
	stored := copyEntry(e)
	stored.SeqNo = int64(len(m.entries)) + 1
	stored.PrevHash = prevHash
	stored.Hash = m.protocol.ComputeHash(prevHash, stored)

	m.entries = append(m.entries, stored)
	m.fingerprints[fp] = struct{}{}

	// Write back SeqNo and Hash to caller's entry so caller can observe them.
	e.SeqNo = stored.SeqNo
	e.PrevHash = stored.PrevHash
	e.Hash = stored.Hash

	return nil
}

// Tail returns the current chain tail snapshot. Returns a zero TailSnapshot
// for an empty store (SeqNo=0, PrevHash="", EntryCount=0).
func (m *MemStore) Tail(_ context.Context) (TailSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) == 0 {
		return TailSnapshot{}, nil
	}
	last := m.entries[len(m.entries)-1]
	return TailSnapshot{
		SeqNo:      last.SeqNo,
		PrevHash:   last.Hash,
		EntryCount: int64(len(m.entries)),
	}, nil
}

// GetBySeq returns a defensive copy of the entry at the given sequence number.
// Returns ErrAuditLedgerNotFound for missing sequence numbers.
func (m *MemStore) GetBySeq(_ context.Context, seq int64) (*Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if seq < 1 || int(seq) > len(m.entries) {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuditLedgerNotFound,
			"audit ledger: entry not found",
			errcode.WithDetails(slog.Int64("seqNo", seq)),
		)
	}
	return copyEntry(m.entries[seq-1]), nil
}

// Query returns entries matching the supplied filters in ascending SeqNo order.
// Zero-value filter fields are treated as "no filter". Applies Limit if > 0.
func (m *MemStore) Query(_ context.Context, filters AuditFilters, params QueryListParams) ([]*Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var results []*Entry
	for _, e := range m.entries {
		if !matchesFilters(e, filters) {
			continue
		}
		results = append(results, copyEntry(e))
		if params.Limit > 0 && len(results) >= params.Limit {
			break
		}
	}
	if results == nil {
		results = []*Entry{}
	}
	return results, nil
}

// Verify re-computes the HMAC-SHA256 hash for each entry in [fromSeq, toSeq]
// and checks chain linkage (PrevHash). Returns valid=true and firstInvalidSeq=-1
// when all entries are intact.
func (m *MemStore) Verify(_ context.Context, fromSeq, toSeq int64) (valid bool, firstInvalidSeq int64, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if fromSeq < 1 || toSeq < fromSeq {
		return false, fromSeq, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: Verify requires 1 <= fromSeq <= toSeq")
	}

	for seq := fromSeq; seq <= toSeq; seq++ {
		idx := seq - 1
		if int(idx) >= len(m.entries) {
			return false, seq, errcode.New(errcode.KindNotFound, errcode.ErrAuditLedgerNotFound,
				"audit ledger: entry not found during Verify")
		}
		e := m.entries[idx]

		expectedPrev := ""
		if idx > 0 {
			expectedPrev = m.entries[idx-1].Hash
		}
		if e.PrevHash != expectedPrev {
			return false, seq, nil
		}

		expectedHash := m.protocol.ComputeHash(e.PrevHash, e)
		if e.Hash != expectedHash {
			return false, seq, nil
		}
	}
	return true, -1, nil
}

// validatePayloadJSON checks that payload is valid JSON. nil or empty payload
// is treated as JSON null (valid). Uses bytes.NewReader to avoid alloc.
func validatePayloadJSON(payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: payload is not valid JSON",
			errcode.WithInternal(fmt.Sprintf("json decode: %v", err)),
		)
	}
	return nil
}

// contentFingerprint computes a SHA-256 hex digest over the entry's identity
// fields (eventID + eventType + actorID + UnixNano + payload). Used as the
// idempotency key for IdempotencyContentFingerprint mode.
//
// Note: uses SHA-256 (not HMAC) for fingerprinting because the idempotency
// key is not a security primitive — it is a collision-resistant content
// address. The HMAC key is reserved for the tamper-evident chain (ComputeHash).
//
// ref: google/trillian types/logroot.go — LeafIdentityHash uses SHA-256 of
// the leaf data as a content-addressed deduplication key.
func contentFingerprint(e *Entry) string {
	h := sha256.New()
	// Write each field separated by a NUL byte to prevent prefix collisions.
	h.Write([]byte(e.EventID))
	h.Write([]byte{0})
	h.Write([]byte(e.EventType))
	h.Write([]byte{0})
	h.Write([]byte(e.ActorID))
	h.Write([]byte{0})
	h.Write([]byte(fmt.Sprintf("%d", e.Timestamp.UnixNano())))
	h.Write([]byte{0})
	h.Write(e.Payload)
	return hex.EncodeToString(h.Sum(nil))
}

// matchesFilters reports whether e matches all non-zero filter predicates.
func matchesFilters(e *Entry, f AuditFilters) bool {
	if f.EventType != "" && e.EventType != f.EventType {
		return false
	}
	if f.ActorID != "" && e.ActorID != f.ActorID {
		return false
	}
	if !f.From.IsZero() && e.Timestamp.Before(f.From) {
		return false
	}
	if !f.To.IsZero() && e.Timestamp.After(f.To) {
		return false
	}
	return true
}

// copyEntry returns a deep copy of e. Payload slice is copied to prevent
// caller mutations from bleeding into stored entries.
func copyEntry(e *Entry) *Entry {
	out := *e
	if e.Payload != nil {
		payload := make([]byte, len(e.Payload))
		copy(payload, e.Payload)
		out.Payload = payload
	}
	return &out
}
