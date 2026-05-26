// Package credentialinvalidate is the single entry point for credential
// revocation events: it bumps the user's authz_epoch, revokes all active
// sessions, and revokes all refresh chains in one ambient transaction.
//
// AI-robust archtest (Hard, see tools/archtest/credential_invalidate_funnel_test.go):
//   - CREDENTIAL-INVALIDATE-FUNNEL-01:  session.Store.RevokeForSubject callers ⊆ {this pkg, store impl, storetest, *_test.go}
//   - USER-AUTHZ-EPOCH-BUMP-FUNNEL-01:  UserRepository.BumpAuthzEpoch callers ⊆ {this pkg, repo impl, *_test.go}
//   - REFRESH-REVOKE-USER-FUNNEL-01:    refresh.Store.RevokeUser callers ⊆ {this pkg, store impl, *_test.go}
//   - FENCE-TOKEN-MINT-FUNNEL-01:       credentialfence.Mint callers ⊆ {this pkg, storetest/conformance, *_test.go}
//
// The three mutation methods take a credentialfence.FenceToken capability
// proof (#1033). Apply mints a FenceToken via credentialfence.Mint and
// passes it to each downstream call — combined with the type-system seal on
// FenceToken (external packages cannot implement the interface or construct
// the unexported impl), this closes the upstream half of the funnel: only
// code reachable from this package can produce the token argument the
// mutation methods require.
//
// Apply must be called inside an ambient transaction (txCtx derived from
// persistence.CellTxManager.RunInTx). All three operations commit atomically;
// call order is irrelevant to correctness.
package credentialinvalidate

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth/credentialfence"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// Invalidator is the single entry point for the credential-revocation trifecta:
// bump user authz_epoch + revoke sessions + revoke refresh chain.
// All callers must go through Apply; direct calls to the underlying stores
// are enforced by archtest funnels (see package-level godoc).
type Invalidator struct {
	users    ports.UserRepository
	sessions session.Store
	refresh  refresh.Store
}

// New constructs an Invalidator, fail-fasting on nil deps (including typed-nil).
func New(users ports.UserRepository, sessions session.Store, refreshStore refresh.Store) (*Invalidator, error) {
	if validation.IsNilInterface(users) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"credentialinvalidate: UserRepository required")
	}
	if validation.IsNilInterface(sessions) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"credentialinvalidate: session.Store required")
	}
	if validation.IsNilInterface(refreshStore) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"credentialinvalidate: refresh.Store required")
	}
	return &Invalidator{users: users, sessions: sessions, refresh: refreshStore}, nil
}

// Apply executes the three credential-revocation operations inside the ambient
// tx carried by txCtx. The operations are ordered for short-circuit on error:
//
//  1. BumpAuthzEpoch — invalidates all future tokens by advancing the epoch
//  2. RevokeForSubject — marks active sessions dead
//  3. RevokeUser — marks all refresh chains dead
//
// All three operations commit atomically when the surrounding tx commits.
// Order is defined only for short-circuit predictability; correctness does not
// depend on the order.
func (i *Invalidator) Apply(txCtx context.Context, subjectID string, event session.CredentialEvent) error {
	// Mint the FenceToken once and pass the same value through all three
	// mutations. The token is identity-less (any non-nil FenceToken is
	// equally valid), so sharing it across the three calls is
	// semantically equivalent to minting per call — and saves two
	// allocations on the hot path.
	tok := credentialfence.Mint()
	// New epoch value is intentionally discarded: sessionvalidate re-reads
	// authz_epoch from the DB on every request, so the caller does not need
	// the bumped value here. The DB row is the single source of truth.
	if _, err := i.users.BumpAuthzEpoch(txCtx, subjectID, tok); err != nil {
		return fmt.Errorf("credentialinvalidate: bump authz_epoch: %w", err)
	}
	if err := i.sessions.RevokeForSubject(txCtx, subjectID, event, tok); err != nil {
		return fmt.Errorf("credentialinvalidate: revoke sessions: %w", err)
	}
	if err := i.refresh.RevokeUser(txCtx, subjectID, tok); err != nil {
		return fmt.Errorf("credentialinvalidate: revoke refresh chain: %w", err)
	}
	return nil
}
