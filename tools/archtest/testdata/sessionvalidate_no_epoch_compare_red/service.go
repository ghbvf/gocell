// Package sessionvalidate_no_epoch_compare_red is a RED fixture:
// it contains an enforceSessionState function that references AuthzEpoch and
// calls GetByID but uses the WRONG comparison operator (>) instead of (!=).
// SESSIONVALIDATE-EPOCH-COMPARE-01 must detect this as a violation because the
// epoch check must be a strict equality check (!=), not a greater-than check.
package sessionvalidate_no_epoch_compare_red

import (
	"context"

	"github.com/ghbvf/gocell/runtime/auth"
)

// stubService is a stub holding a non-nil userRepo field to satisfy the
// scanner's function-body check (it only inspects the AST, not runtime deps).
type stubService struct {
	userRepo interface {
		GetByID(ctx context.Context, id string) (stubUser, error)
	}
}

type stubUser struct {
	AuthzEpoch int64
}

// enforceSessionState is the target function. This RED variant references
// AuthzEpoch and calls GetByID but uses > instead of != — the epoch-inequality
// archtest must detect the wrong operator and report a violation.
func (s *stubService) enforceSessionState(ctx context.Context, claims auth.Claims) (auth.Claims, error) {
	user, err := s.userRepo.GetByID(ctx, claims.Subject)
	if err != nil {
		return auth.Claims{}, err
	}
	// BUG: uses > instead of !=; archtest must flag this as a violation.
	if user.AuthzEpoch > claims.AuthzEpoch {
		return auth.Claims{}, nil
	}
	return claims, nil
}
