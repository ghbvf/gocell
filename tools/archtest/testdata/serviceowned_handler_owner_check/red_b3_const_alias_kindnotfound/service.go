// Package red_b3_const_alias_kindnotfound is a RED fixture for
// SERVICEOWNED-HANDLER-OWNER-CHECK-01 predicate B3 (const-alias detection):
// the service.go declares `const aliasedKind = errcode.KindNotFound` and
// passes the alias to errcode.New. Bare ResolvePackageRef on the local
// Ident would NOT resolve to (pkg/errcode, KindNotFound) — but go/types
// constant folding sets Types[aliasedKind].Value to the same constant
// value as errcode.KindNotFound, so isKindNotFoundArg's const-folding
// branch catches it.
//
// (Runtime-var alias `local := errcode.KindNotFound` remains a known
// blindspot — *types.Var has no Value; SSA dataflow needed. Tracked at
// gh #1199.)
package red_b3_const_alias_kindnotfound

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// aliasedKind is a typed-const alias of errcode.KindNotFound. go/types
// resolves its constant value to the same integer as the canonical
// symbol, making it caught by the const-folding branch of B3.
const aliasedKind = errcode.KindNotFound

type session struct{ SubjectID string }

type store interface {
	Get(ctx context.Context, id string) (*session, error)
	Revoke(ctx context.Context, id string) error
}

type Service struct{ s store }

// Logout passes aliasedKind (a const alias) to errcode.New. B3 must
// catch this via constant folding even though the local Ident does not
// type-resolve to the package-level errcode.KindNotFound symbol.
func (svc *Service) Logout(ctx context.Context, sessionID, callerUserID string) error {
	sess, err := svc.s.Get(ctx, sessionID)
	if err != nil {
		// BUG: const alias bypasses naive selector check; const folding catches.
		return errcode.New(aliasedKind, errcode.ErrSessionNotFound, "session not found")
	}
	if err := auth.CheckOwner(sess, func(s *session) string {
		if s == nil {
			return ""
		}
		return s.SubjectID
	}, callerUserID, errcode.ErrSessionNotFound); err != nil {
		return err
	}
	return svc.s.Revoke(ctx, sessionID)
}
