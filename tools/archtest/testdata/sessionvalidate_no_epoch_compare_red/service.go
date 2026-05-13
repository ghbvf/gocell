// Package sessionvalidate_no_epoch_compare_red is a RED fixture:
// it contains an enforceSessionState function that does NOT compare
// claims.AuthzEpoch against the user record. SESSIONVALIDATE-EPOCH-COMPARE-01
// must detect this as a violation.
package sessionvalidate_no_epoch_compare_red

import (
	"context"

	"github.com/ghbvf/gocell/runtime/auth"
)

// stubService is a stub holding a non-nil userRepo field to satisfy the
// scanner's function-body check (it only inspects the AST, not runtime deps).
type stubService struct{}

// enforceSessionState is the target function. This RED variant omits any
// comparison of claims.AuthzEpoch, triggering SESSIONVALIDATE-EPOCH-COMPARE-01.
func (s *stubService) enforceSessionState(ctx context.Context, claims auth.Claims) (auth.Claims, error) {
	// No claims.AuthzEpoch comparison — deliberately missing.
	// No userRepo.GetByID call — deliberately missing.
	return claims, nil
}
