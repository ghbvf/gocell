// Package allowlisted_function_extra_call_red is a RED fixture for
// SESSION-REVOKE-CALLER-INTX-01: a function whose first Revoke call is
// sanctioned, but whose second Revoke call uses a non-transaction context.
package allowlisted_function_extra_call_red

import (
	"context"

	"github.com/ghbvf/gocell/runtime/auth/session"
)

func allowedButBad(ctx, txCtx context.Context, s session.Store, id string) error {
	if err := s.Revoke(txCtx, id); err != nil {
		return err
	}
	return s.Revoke(context.Background(), id)
}
