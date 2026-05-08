// Package credentialrevoke contains the narrow accesscore helper for invalidating
// all credentials owned by one user.
package credentialrevoke

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

// User revokes all access sessions and refresh-token chains for userID.
// Callers should run this inside their existing TxRunner transaction when the
// revoke must be atomic with the business state change.
func User(ctx context.Context, sessionRepo ports.SessionRepository, refreshStore refresh.Store, userID, op string) error {
	if err := sessionRepo.RevokeByUserID(ctx, userID); err != nil {
		return fmt.Errorf("%s revoke sessions: %w", op, err)
	}
	if err := refreshStore.RevokeUser(ctx, userID); err != nil {
		return fmt.Errorf("%s revoke refresh chains: %w", op, err)
	}
	return nil
}
