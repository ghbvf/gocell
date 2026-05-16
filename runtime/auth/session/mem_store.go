package session

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// MemStore is an in-memory Store implementation for dev and tests. It is
// not a production substrate — the PG-backed Store landing in S3+S5 owns
// the production path. Three properties follow from the dev/test scope and
// are documented design choices, not gaps:
//
//   - Single RWMutex over a map[ID]*Session. RevokeForSubject scans under
//     the write lock (O(n) in subject count). Acceptable at dev/test
//     scale; PG handles batch revoke via SQL with indexed user_id.
//   - No GC and no capacity ceiling. Expired sessions remain Get-able
//     (ADR-Session D3); cleanup is a PG janitor concern.
//   - No instrumentation. Observability is a cell-layer concern (S4
//     wires slog/metrics around Store calls in accesscore).
//
// The contract test suite exercises concurrent access (`go test -race`) and
// the protocol decisions encoded in *Protocol drive RevokeForSubject
// scoping. See package doc for the full scope rationale.
type MemStore struct {
	protocol *Protocol
	clock    clock.Clock

	mu       sync.RWMutex
	sessions map[string]*Session
}

// NewMemStore constructs a MemStore. Both protocol and clk are strong-
// dependency wiring (they are not replaceable defaults); typed-nil and bare
// nil are rejected at construction so misconfiguration surfaces at startup
// rather than at the first request.
//
// runtime-api.md §Option 范式分层 — one or two unconditional dependencies are
// passed positionally; Option pattern only becomes warranted at ≥ 3 deps or
// when an accumulator (e.g. WithRevokeOn) appears.
func NewMemStore(protocol *Protocol, clk clock.Clock) (*MemStore, error) {
	// protocol is *Protocol (concrete pointer): bare-nil check suffices, no
	// typed-nil interface risk. clk is the clock.Clock interface: typed-nil
	// is possible (var c clock.Clock; c is nil but rv.Type() != nil), so
	// validation.IsNilInterface is required there.
	if protocol == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"session: NewMemStore requires non-nil Protocol")
	}
	if validation.IsNilInterface(clk) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"session: NewMemStore requires non-nil Clock")
	}
	return &MemStore{
		protocol: protocol,
		clock:    clk,
		sessions: make(map[string]*Session),
	}, nil
}

// Create persists s. Protocol-shape validation rejects records that violate
// the configured FingerprintMode (e.g. empty JTI under FingerprintJTIRef);
// duplicate IDs return ErrSessionConflict. Stored value is a defensive copy
// so caller mutations cannot bleed into the store after Create.
func (m *MemStore) Create(_ context.Context, s *Session) error {
	if s == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"session: Create requires non-nil Session")
	}
	if s.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"session: Session.ID required")
	}
	if s.SubjectID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"session: Session.SubjectID required")
	}
	// S4d: credential provenance is mandatory. Zero is the unset sentinel —
	// it can never equal a live users.authz_epoch after the first bump
	// (which advances from 0 → 1), so accepting 0 here would let a session
	// row exist that can never validate. Reject at the store boundary so the
	// invariant is enforced uniformly across mem and PG.
	if s.AuthzEpochAtIssue == 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"session: Session.AuthzEpochAtIssue required (non-zero)")
	}
	if err := m.validateFingerprintShape(s); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.sessions[s.ID]; exists {
		return errcode.New(errcode.KindConflict, errcode.ErrSessionConflict,
			"session: duplicate ID")
	}
	m.sessions[s.ID] = copySession(s)
	return nil
}

// Get returns the validate projection of the session keyed by id. Revoked
// sessions are still returned (caller inspects RevokedAt); GC eligibility
// (Session.ExpiresAt) is intentionally not exposed — validate paths must
// not gate on it.
func (m *MemStore) Get(_ context.Context, id string) (*ValidateView, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound,
			"session: not found",
			errcode.WithCategory(errcode.CategoryDomain))
	}
	return toValidateView(s), nil
}

// Revoke marks the session keyed by id dead. Idempotent: missing IDs and
// already-revoked sessions both succeed without modifying state. Once
// RevokedAt is set it is never re-stamped (append-only revoke semantics —
// ADR-Session D3 fail-closed by default).
func (m *MemStore) Revoke(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		// 防枚举 — must not leak existence; return nil regardless.
		return nil
	}
	if s.RevokedAt != nil {
		return nil
	}
	now := m.clock.Now()
	s.RevokedAt = &now
	return nil
}

// RevokeForSubject marks every active session belonging to subjectID dead.
// The CredentialEvent argument is informational under the current protocol
// (D3 fail-closed by default — every event triggers identical revoke
// scoping); future protocols may scope per-event when sealed alternatives
// are added. Missing subjects yield no error (always succeeds).
func (m *MemStore) RevokeForSubject(_ context.Context, subjectID string, event CredentialEvent) error {
	if subjectID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"session: RevokeForSubject requires non-empty subjectID")
	}
	if !ValidateCredentialEvent(event) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"session: RevokeForSubject received unknown CredentialEvent")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock.Now()
	for _, s := range m.sessions {
		if s.SubjectID != subjectID {
			continue
		}
		if s.RevokedAt != nil {
			continue
		}
		stamp := now
		s.RevokedAt = &stamp
	}
	return nil
}

// RepoReady implements cell.RepoHealthProber. In-memory store is always ready
// — there is no external relation or schema that can go missing. Returns nil
// unconditionally (MemStore convention per kernel/cell.RepoHealthProber godoc).
func (m *MemStore) RepoReady(_ context.Context) error {
	return nil
}

// validateFingerprintShape enforces the per-FingerprintMode invariants on a
// Session record. Sealed Protocol.Fingerprint() values are exhaustive; future
// modes register here when added (the if-chain grows with each new sibling
// type rather than collapsing to switch — single-case switch tripped gocritic).
func (m *MemStore) validateFingerprintShape(s *Session) error {
	if _, ok := m.protocol.Fingerprint().(FingerprintJTIRef); ok {
		if s.JTI == "" {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"session: FingerprintJTIRef requires non-empty JTI")
		}
	}
	return nil
}

// copySession returns a deep copy of s (RevokedAt pointer chased) so caller
// mutations do not bleed into the store and Get callers cannot mutate stored
// state through the returned pointer.
func copySession(s *Session) *Session {
	out := *s
	if s.RevokedAt != nil {
		stamp := *s.RevokedAt
		out.RevokedAt = &stamp
	}
	return &out
}

// toValidateView projects a stored *Session onto the narrow ValidateView
// returned by Store.Get. RevokedAt is pointer-chased so caller mutations
// cannot bleed back into the stored Session.
func toValidateView(s *Session) *ValidateView {
	v := &ValidateView{
		ID:                s.ID,
		SubjectID:         s.SubjectID,
		AuthzEpochAtIssue: s.AuthzEpochAtIssue,
	}
	if s.RevokedAt != nil {
		stamp := *s.RevokedAt
		v.RevokedAt = &stamp
	}
	return v
}
