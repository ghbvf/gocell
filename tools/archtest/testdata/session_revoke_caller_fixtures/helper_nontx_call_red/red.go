// Package helper_nontx_call_red is a RED fixture for
// SESSION-REVOKE-CALLER-INTX-01: a tx-scoped helper is called once correctly
// and once with a non-transaction context.
package helper_nontx_call_red

import "context"

type service struct{}

func (service) cleanupIssuedSession(context.Context, string) {}

func caller(ctx, txCtx context.Context, s service, id string) {
	s.cleanupIssuedSession(txCtx, id)
	s.cleanupIssuedSession(context.Background(), id)
}
