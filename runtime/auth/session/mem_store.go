package session

import (
	"context"
	"errors"

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
	// RED stub: Wave 2 lands the actual storage map and method bodies.
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
	if validation.IsNilInterface(protocol) {
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
	}, nil
}

// errMemStoreRedStub is the placeholder returned by every MemStore method in
// Wave 1. Wave 2 replaces these stubs with real implementations and the
// stub is deleted in the same commit.
var errMemStoreRedStub = errors.New("session.MemStore: RED stub — Wave 2 implements method bodies")

// Create — Wave 1 RED stub. Wave 2 implements actual persistence.
func (m *MemStore) Create(_ context.Context, _ *Session) error { return errMemStoreRedStub }

// Get — Wave 1 RED stub.
func (m *MemStore) Get(_ context.Context, _ string) (*Session, error) {
	return nil, errMemStoreRedStub
}

// Revoke — Wave 1 RED stub.
func (m *MemStore) Revoke(_ context.Context, _ string) error { return errMemStoreRedStub }

// RevokeForSubject — Wave 1 RED stub.
func (m *MemStore) RevokeForSubject(_ context.Context, _ string, _ CredentialEvent) error {
	return errMemStoreRedStub
}
