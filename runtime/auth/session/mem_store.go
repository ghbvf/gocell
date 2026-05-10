package session

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// MemStore is an in-memory Store implementation suitable for tests and dev.
// All operations take a single RWMutex; the contract test suite exercises
// concurrent access (`go test -race`) and the protocol decisions encoded in
// *Protocol drive RevokeForSubject scoping.
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

// Get returns a defensive copy of the session keyed by id. Revoked or expired
// sessions are still returned; callers inspect Session.RevokedAt and
// Session.ExpiresAt to make policy decisions.
func (m *MemStore) Get(_ context.Context, id string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound,
			"session: not found")
	}
	return copySession(s), nil
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
	if !credentialEventValid(event) {
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
