// Package rbacassign_direct_revoke_for_subject_red is a RED fixture for
// CREDENTIAL-INVALIDATE-FUNNEL-01. It references session.Store.RevokeForSubject
// from a non-allowlisted path in three distinct AST shapes — direct call,
// short-var function-value capture, and var-decl function-value capture. The
// form-complete scanner must detect EVERY one (3 references); a CallExpr-only
// scan would catch only the direct call and miss the two captures, so the
// wantMin=3 self-check in the archtest pins form-completeness.
package rbacassign_direct_revoke_for_subject_red

import (
	"context"

	"github.com/ghbvf/gocell/framework/runtime/auth/credentialfence"
	"github.com/ghbvf/gocell/framework/runtime/auth/session"
)

// 1. direct call — bypasses the credentialinvalidate funnel.
func badRevoke(ctx context.Context, store session.Store, subjectID string) error {
	return store.RevokeForSubject(ctx, subjectID, session.CredentialEventLock, credentialfence.Mint())
}

//  2. short-var method-value capture (AssignStmt) + deferred invocation. The
//     `fn(...)` call has Fun = *ast.Ident, invisible to a CallExpr-only scan.
func badRevokeAssignCapture(ctx context.Context, store session.Store, subjectID string) error {
	fn := store.RevokeForSubject
	return fn(ctx, subjectID, session.CredentialEventLock, credentialfence.Mint())
}

// 3. var-decl method-value capture (GenDecl ValueSpec — not an AssignStmt).
func badRevokeVarCapture(store session.Store) func(context.Context, string, session.CredentialEvent, credentialfence.FenceToken) error {
	var fn = store.RevokeForSubject
	return fn
}
