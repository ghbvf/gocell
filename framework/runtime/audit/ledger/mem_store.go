package ledger

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/pkg/validation"
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

	mu sync.Mutex
	// chains partitions entries by tenant_id into independent per-(namespace,
	// tenant) hash chains (#1618). MemStore is per-namespace (one instance per
	// NamespaceID), so the key is the tenant_id alone; tenant_id == "" is the
	// system/framework sub-chain (pre-auth events with no principal tenant, e.g.
	// bootstrap.auth.fail). Each chain restarts at SeqNo=1 with PrevHash="".
	chains map[string]*memChain
}

// memChain is one per-tenant hash chain within a MemStore namespace: the entry
// slice (indexed by SeqNo-1) plus a per-tenant content-fingerprint set mirroring
// the PG uq_audit_ns_tenant_event_id idempotency index.
type memChain struct {
	entries      []*Entry
	fingerprints map[string]struct{}
}

// tenantScopeOrSystem resolves the tenant chain key for ctx-scoped reads
// (GetBySeq / Verify / Tail): the tenant.WithScope value when present, else ""
// (the system/framework chain). Mirrors the PG store deriving its explicit
// tenant predicate from tenant.ScopeFromContext, defaulting to "" when no scope
// is set — startup tail-verify and system-chain replay run unscoped (#1618).
func tenantScopeOrSystem(ctx context.Context) string {
	if t, ok := tenant.ScopeFromContext(ctx); ok {
		return t.String()
	}
	return ""
}

// chainFor returns the chain for tenant key t, creating it on first write.
// Callers must hold m.mu.
func (m *MemStore) chainFor(t string) *memChain {
	c := m.chains[t]
	if c == nil {
		c = &memChain{fingerprints: make(map[string]struct{})}
		m.chains[t] = c
	}
	return c
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
		protocol: protocol,
		clock:    clk,
		chains:   make(map[string]*memChain),
	}, nil
}

// Protocol returns the immutable protocol decisions backing this store.
func (m *MemStore) Protocol() *Protocol { return m.protocol }

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

	// Per-(namespace, tenant) chain: seq_no, prev-hash linkage and the
	// idempotency fingerprint are all scoped to the entry's own tenant (#1618).
	chain := m.chainFor(e.TenantID)

	// Idempotency check.
	if _, exists := chain.fingerprints[fp]; exists {
		return errcode.New(errcode.KindConflict, errcode.ErrAuditLedgerAlreadyExists,
			"audit ledger: duplicate content fingerprint")
	}

	// Determine PrevHash from the current tail of this tenant's chain.
	prevHash := ""
	if len(chain.entries) > 0 {
		prevHash = chain.entries[len(chain.entries)-1].Hash
	}

	// Build the stored entry (copy to prevent caller mutations from leaking).
	stored := copyEntry(e)
	// Assign a GLOBALLY-UNIQUE, deterministic store id derived from the entry's
	// (namespace, tenant, eventID) — the unique key of the audit chain
	// (uq_audit_ns_tenant_event_id). Global uniqueness is load-bearing for the
	// cross-tenant read: MemCrossTenantStore.GetByIDCrossTenant spans every tenant
	// AND both namespace chains, so a bare-EventID id (EventID is unique only per
	// (namespace, tenant)) would let two tenants sharing an EventID collide and
	// return the wrong row (#2288 review F1). Deterministic (not a random uuid.New
	// like the PG LedgerStore) so the keyset tie-break (id ASC) stays stable across
	// test runs. Both backends thus assign a globally-unique opaque handle — random
	// uuid on PG, deterministic hash here — that the wire `id` projects and GetByID
	// resolves; the contract treats id as an opaque SafeID string either way.
	stored.ID = deterministicEntryID(string(m.protocol.Namespace()), e.TenantID, e.EventID)
	stored.SeqNo = int64(len(chain.entries)) + 1
	stored.PrevHash = prevHash
	stored.Hash = m.protocol.ComputeHash(prevHash, stored)

	chain.entries = append(chain.entries, stored)
	chain.fingerprints[fp] = struct{}{}

	// Write back SeqNo, ID, and Hash to caller's entry so caller can observe them.
	e.ID = stored.ID
	e.SeqNo = stored.SeqNo
	e.PrevHash = stored.PrevHash
	e.Hash = stored.Hash

	return nil
}

// deterministicEntryID derives a globally-unique, deterministic in-memory store id
// from an entry's unique key (namespace, tenant, eventID). It reuses the store's
// already-imported sha256/hex (no new dependency); the NUL separators make the
// concatenation unambiguous (so "a","bc" and "ab","c" cannot collide). 16 bytes
// (128-bit) of digest is ample collision resistance for a demo/test store. See the
// Append call site for why global uniqueness (not the bare EventID) is required.
func deterministicEntryID(namespace, tenantID, eventID string) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + tenantID + "\x00" + eventID))
	return hex.EncodeToString(sum[:16])
}

// Tail returns the current tail snapshot of the ctx-scoped tenant chain (the
// tenant.WithScope value, or the "" system chain when unscoped — #1618). Returns
// a zero TailSnapshot for an empty/absent chain (SeqNo=0, PrevHash="",
// EntryCount=0). Per-(namespace, tenant) chains have no single "namespace tail";
// startup tail-verify runs unscoped and thus reads the "" system chain.
func (m *MemStore) Tail(ctx context.Context) (TailSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	chain := m.chains[tenantScopeOrSystem(ctx)]
	if chain == nil || len(chain.entries) == 0 {
		return TailSnapshot{}, nil
	}
	last := chain.entries[len(chain.entries)-1]
	return TailSnapshot{
		SeqNo:      last.SeqNo,
		PrevHash:   last.Hash,
		EntryCount: int64(len(chain.entries)),
	}, nil
}

// GetBySeq returns a defensive copy of the entry at the given sequence number
// within the ctx-scoped tenant chain (#1618). Two orthogonal axes are enforced
// and both collapse to ErrAuditLedgerNotFound (IDOR-safe — existence is not
// leaked): the TENANT axis (seq must exist in the tenant.ScopeFromContext chain,
// "" system chain when unscoped — a cross-tenant by-seq read finds nothing,
// closing PR #1715 F1) and the OWNER axis (vis.Allows(entry.ActorID)).
func (m *MemStore) GetBySeq(ctx context.Context, vis tenant.RowVisibility, seq int64) (*Entry, error) {
	if err := vis.Validate(); err != nil {
		return nil, err
	}
	if vis.Scope() == tenant.RowScopeAll {
		return nil, RowScopeAllUnsupportedError()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	chain := m.chains[tenantScopeOrSystem(ctx)]
	if chain == nil || seq < 1 || int(seq) > len(chain.entries) {
		return nil, errcode.New(
			errcode.KindNotFound, errcode.ErrAuditLedgerNotFound,
			"audit ledger: entry not found",
			errcode.WithDetails(errcode.PublicInt("seqNo", seq)),
		)
	}
	e := chain.entries[seq-1]
	if !vis.Allows(e.ActorID) {
		// IDOR-safe collapse: do not reveal that the entry exists.
		return nil, errcode.New(
			errcode.KindNotFound, errcode.ErrAuditLedgerNotFound,
			"audit ledger: entry not found",
			errcode.WithDetails(errcode.PublicInt("seqNo", seq)),
		)
	}
	return copyEntry(e), nil
}

// auditEntryNotFoundByID is the IDOR-safe not-found sentinel for the single-entry
// reads that key on an opaque id (MemStore.GetByID / MemCrossTenantStore.
// GetByIDCrossTenant); the name mirrors the PG adapter's auditEntryNotFoundByID. It carries NO public detail: unlike the by-seq path (which
// echoes the integer seqNo), the id is a caller-supplied opaque string, so it is
// kept off the wire and out of logs. Existence is never revealed — the same code
// is returned for "absent", "another tenant's row", and "outside owner scope".
func auditEntryNotFoundByID() error {
	return errcode.New(errcode.KindNotFound, errcode.ErrAuditLedgerNotFound,
		"audit ledger: entry not found")
}

// GetByID returns a defensive copy of the entry with the given opaque id within
// tenant t. Like GetBySeq it enforces two orthogonal axes and collapses both to
// ErrAuditLedgerNotFound (IDOR-safe — existence is not leaked). Unlike GetBySeq it
// takes the explicit typed tenant t (the post-auth http.audit.get.v1 funnel, like
// Query) rather than the ctx scope: it scans t's chain PLUS the "" system chain
// (the mem analog of the PG `(tenant_id = ” OR tenant_id = $t)` predicate + FORCE
// RLS), so a cross-tenant id read finds nothing.
func (m *MemStore) GetByID(_ context.Context, t tenant.TenantID, vis tenant.RowVisibility, id string) (*Entry, error) {
	if err := ValidateQueryTenant(t); err != nil {
		return nil, err
	}
	if err := vis.Validate(); err != nil {
		return nil, err
	}
	if vis.Scope() == tenant.RowScopeAll {
		return nil, RowScopeAllUnsupportedError()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	tenantKey := t.String()
	for chainKey, chain := range m.chains {
		if !tenantMatches(chainKey, tenantKey) {
			continue
		}
		for _, e := range chain.entries {
			if e.ID != id {
				continue
			}
			if !vis.Allows(e.ActorID) {
				// IDOR-safe collapse: do not reveal that the entry exists.
				return nil, auditEntryNotFoundByID()
			}
			return copyEntry(e), nil
		}
	}
	return nil, auditEntryNotFoundByID()
}

// validateQueryArgs validates the mandatory preconditions shared by all MemStore
// query paths. Extracted from Query to keep cognitive complexity ≤ 15.
func validateQueryArgs(t tenant.TenantID, vis tenant.RowVisibility, filters AuditFilters, params query.ListParams) error {
	if err := ValidateQueryTenant(t); err != nil {
		return err
	}
	if err := ValidateQueryFilters(filters); err != nil {
		return err
	}
	if err := vis.Validate(); err != nil {
		return err
	}
	if vis.Scope() == tenant.RowScopeAll {
		return RowScopeAllUnsupportedError()
	}
	if len(params.Sort) == 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: query requires a non-empty sort")
	}
	return nil
}

// Query returns entries matching the supplied filters using keyset cursor
// pagination: candidates are filtered, sorted by params.Sort, then ApplyCursor
// skips past params.CursorValues and returns up to params.FetchLimit() (Limit+1)
// rows for N+1 hasMore detection. Zero-value filter fields are treated as "no
// filter".
//
// params.Sort must be non-empty (callers pass QuerySort). An empty Sort is a
// programmer error and yields ErrValidationFailed — the same rejection the PG
// keyset builder produces, so both backends reject it identically.
func (m *MemStore) Query(
	_ context.Context, t tenant.TenantID, vis tenant.RowVisibility,
	filters AuditFilters, params query.ListParams,
) ([]*Entry, error) {
	if err := validateQueryArgs(t, vis, filters, params); err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// TENANT axis (#1618): scan tenant t's chain PLUS the "" system chain
	// (tenantMatches mirrors the PG `tenant_id = $N OR tenant_id = ''` predicate;
	// t is a non-empty canonical tenant, so exactly those two chains qualify).
	// This is the mem-store analog of FORCE RLS. Collect all matching entries
	// first so the sort/keyset see the full candidate set; ApplyCursor then trims
	// to FetchLimit after ordering.
	tenantKey := t.String()
	var candidates []*Entry
	for chainKey, chain := range m.chains {
		if !tenantMatches(chainKey, tenantKey) {
			continue
		}
		for _, e := range chain.entries {
			if matchesFilters(e, filters) && vis.Allows(e.ActorID) {
				candidates = append(candidates, copyEntry(e))
			}
		}
	}

	query.Sort(candidates, params.Sort, compareEntryField)
	results, err := query.ApplyCursor(candidates, params, entryFieldValue)
	if err != nil {
		return nil, err
	}
	if results == nil {
		results = []*Entry{}
	}
	return results, nil
}

// compareEntryField compares a single named field of two ledger entries for
// in-memory keyset sorting. Only QuerySort columns are recognized.
func compareEntryField(a, b *Entry, field string) int {
	switch field {
	case "timestamp":
		return a.Timestamp.Compare(b.Timestamp)
	case "id":
		return cmp.Compare(a.ID, b.ID)
	default:
		return 0
	}
}

// entryFieldValue extracts a cursor-comparable value from a ledger entry. The
// timestamp is returned as time.Time (not a formatted string) so that
// query.CompareAny uses temporal comparison against the RFC3339Nano string
// cursor value rather than lexical string comparison.
func entryFieldValue(e *Entry, field string) any {
	switch field {
	case "timestamp":
		return e.Timestamp
	case "id":
		return e.ID
	default:
		return ""
	}
}

// Verify re-computes the HMAC-SHA256 hash for each entry in [fromSeq, toSeq]
// and checks chain linkage (PrevHash). Returns valid=true and firstInvalidSeq=-1
// when all entries are intact.
func (m *MemStore) Verify(ctx context.Context, fromSeq, toSeq int64) (valid bool, firstInvalidSeq int64, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if fromSeq < 1 || toSeq < fromSeq {
		return false, fromSeq, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: Verify requires 1 <= fromSeq <= toSeq")
	}

	// Verify the ctx-scoped tenant chain (#1618; "" system chain when unscoped —
	// startup tail-verify runs unscoped over the system/bootstrap chain).
	var entries []*Entry
	if chain := m.chains[tenantScopeOrSystem(ctx)]; chain != nil {
		entries = chain.entries
	}

	for seq := fromSeq; seq <= toSeq; seq++ {
		idx := seq - 1
		if int(idx) >= len(entries) {
			return false, seq, errcode.New(errcode.KindNotFound, errcode.ErrAuditLedgerNotFound,
				"audit ledger: entry not found during Verify")
		}
		e := entries[idx]

		expectedPrev := ""
		if idx > 0 {
			expectedPrev = entries[idx-1].Hash
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

// RepoReady implements healthz.RepoProber. The in-memory store has no
// differentiated failure domain (it holds state entirely in process memory),
// so this always returns nil — matching the MemStore convention documented in
// kernel/healthz.RepoProber.
func (m *MemStore) RepoReady(_ context.Context) error {
	return nil
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
		return errcode.New(
			errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: payload must be a JSON object or null",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("json decode: %v", err))),
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

// tenantMatches mirrors the PG store's tenant predicate `(tenant_id = ” OR
// tenant_id = $N)` exactly, where $N is the query tenant (#1618). A non-empty
// query tenant matches its OWN tenant's chain PLUS the tenant-less
// system/framework chain (entryTenant == "" — e.g. bootstrap.auth.fail), never
// another tenant's rows. An EMPTY query tenant (queryTenant == "") collapses the
// PG predicate to `tenant_id = ”` → system rows ONLY (NOT "all"): a tenant-less
// query is fail-closed and can never read another tenant's rows. The query
// tenant is the mandatory Store.Query t parameter, always non-empty in
// production. See Store.Query.
func tenantMatches(entryTenant, queryTenant string) bool {
	return entryTenant == queryTenant || entryTenant == ""
}

// matchesFilters reports whether e matches all non-zero non-tenant filter
// predicates. The TENANT axis is enforced separately (Query chain selection via
// tenantMatches), not here — AuditFilters no longer carries a tenant field.
func matchesFilters(e *Entry, f AuditFilters) bool {
	if f.EventType != "" && e.EventType != f.EventType {
		return false
	}
	if f.ActorID != "" && e.ActorID != f.ActorID {
		return false
	}
	if f.SubjectID != "" && e.SubjectID != f.SubjectID {
		return false
	}
	if f.TraceID != "" && e.TraceID != f.TraceID {
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
