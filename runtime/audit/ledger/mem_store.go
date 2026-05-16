package ledger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
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
	entries      []*Entry            // indexed by SeqNo-1 (SeqNo starts at 1)
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
	// Assign a stable store-level ID from EventID (mirrors PG store assigning a
	// UUID primary key on INSERT). Using EventID keeps the ID deterministic so
	// Query tie-breaking by ID ASC is stable across test runs — random UUIDs
	// would make same-timestamp tie ordering non-deterministic.
	stored.ID = e.EventID
	stored.SeqNo = int64(len(m.entries)) + 1
	stored.PrevHash = prevHash
	stored.Hash = m.protocol.ComputeHash(prevHash, stored)

	m.entries = append(m.entries, stored)
	m.fingerprints[fp] = struct{}{}

	// Write back SeqNo, ID, and Hash to caller's entry so caller can observe them.
	e.ID = stored.ID
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

// Query returns entries matching the supplied filters sorted by timestamp DESC,
// with ID ASC as the tie-breaker for entries sharing the same timestamp.
// This matches the PG store ORDER BY clause (ORDER BY timestamp DESC, id ASC).
// Zero-value filter fields are treated as "no filter". Applies Limit if > 0.
func (m *MemStore) Query(_ context.Context, filters AuditFilters, params QueryListParams) ([]*Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Collect all matching entries first (before applying Limit) so that the
	// sort sees the full candidate set and Limit is applied after ordering.
	var candidates []*Entry
	for _, e := range m.entries {
		if matchesFilters(e, filters) {
			candidates = append(candidates, copyEntry(e))
		}
	}

	// Sort: primary timestamp DESC, secondary ID ASC (mirrors PG ORDER BY).
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Timestamp.Equal(candidates[j].Timestamp) {
			return candidates[i].ID < candidates[j].ID
		}
		return candidates[i].Timestamp.After(candidates[j].Timestamp)
	})

	// Apply Limit after sorting.
	results := candidates
	if params.Limit > 0 && len(results) > params.Limit {
		results = results[:params.Limit]
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

// RepoReady implements cell.RepoHealthProber. The in-memory store has no
// differentiated failure domain (it holds state entirely in process memory),
// so this always returns nil — matching the MemStore convention documented in
// kernel/cell.RepoHealthProber.
func (m *MemStore) RepoReady(_ context.Context) error {
	return nil
}

// MustTamperEntryHash directly modifies the Hash field of the stored entry at
// the given seq_no (1-indexed). Used only by storetest for negative Verify cases.
//
// This method is intentionally exported from the test-only MemStore so that
// storetest can construct tampered-chain scenarios without coupling the test
// infrastructure to the concrete store type outside this package. The Must*
// prefix follows PANIC-REGISTERED-01: panicking on an out-of-range seq_no is a
// B-class assertion (programming error in the test caller).
func (m *MemStore) MustTamperEntryHash(seq int64, newHash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := seq - 1
	if int(idx) < 0 || int(idx) >= len(m.entries) {
		panic(panicregister.Approved("audit-mem-tamper-hash-out-of-range",
			errcode.Assertion("MustTamperEntryHash: seq %d out of range [1, %d]", seq, len(m.entries))))
	}
	m.entries[idx].Hash = newHash
}

// MustTamperEntryPrevHash directly modifies the PrevHash field of the stored
// entry at the given seq_no (1-indexed). Used only by storetest for negative
// Verify cases. The Must* prefix follows PANIC-REGISTERED-01: panicking on an
// out-of-range seq_no is a B-class assertion (programming error in the test caller).
func (m *MemStore) MustTamperEntryPrevHash(seq int64, newPrevHash string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := seq - 1
	if int(idx) < 0 || int(idx) >= len(m.entries) {
		panic(panicregister.Approved("audit-mem-tamper-prev-hash-out-of-range",
			errcode.Assertion("MustTamperEntryPrevHash: seq %d out of range [1, %d]", seq, len(m.entries))))
	}
	m.entries[idx].PrevHash = newPrevHash
}

// validatePayloadJSON checks that payload is a valid JSON object or null.
// nil or empty payload is treated as JSON null (valid). Uses bytes.NewReader to avoid alloc.
// F21: arrays and scalar JSON values are rejected — must be a JSON object.
func validatePayloadJSON(payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.NewDecoder(bytes.NewReader(payload)).Decode(&m); err != nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: payload must be a JSON object or null",
			errcode.WithInternal(fmt.Sprintf("json decode: %v", err)),
		)
	}
	return nil
}

// contentFingerprint computes a SHA-256 hex digest over the entry's stable
// identity: EventID (UUID). At-least-once redelivery produces the same EventID
// regardless of when the re-delivery occurs, so using only EventID guarantees
// that duplicate events are detected even when the clock advances between
// attempts (e.g., at-least-once outbox relay redelivery).
//
// Fields deliberately excluded:
//   - EventType, ActorID: stable per-event metadata but redundant when EventID
//     is globally unique; including them adds no collision resistance while
//     preventing dedup on partial-metadata redelivery.
//   - Timestamp (clk.Now()): changes on every redelivery — including it would
//     produce a different fingerprint for each retry, defeating idempotency.
//   - Payload: may differ due to schema evolution; EventID is the stable key.
//
// The DB-level UNIQUE INDEX on (namespace, event_id) (migration 021) acts as a
// second-line guard against concurrent bypass of this application-level check.
//
// ref: Watermill router.go — message.UUID as dedup key (handler receives each
// UUID at most once per consumer group).
// ref: NServiceBus MessageDeduplicationBehavior — message ID as idempotency key.
// ref: google/trillian types/logroot.go — SHA-256 of leaf identity (not data).
func contentFingerprint(e *Entry) string {
	h := sha256.New()
	h.Write([]byte(e.EventID))
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
