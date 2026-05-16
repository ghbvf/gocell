// Package l2_atomicity is the L2 (OutboxFact) e2e harness for accesscore
// session/refresh/revoke/validate fail-closed regression coverage. It boots
// a full PG + RabbitMQ-free in-process eventbus assembly and exercises:
//
//   - login → sessions row + refresh_tokens row + outbox event committed atomically
//   - refresh → stable sid (OAuth2/OIDC sid invariant) + jti/authz_epoch JWT claims
//   - refresh reuse detected → credentialinvalidate funnel cascades (BumpAuthzEpoch +
//     RevokeForSubject + RevokeUser) under same tx
//   - rbacassign Revoke → outbox emit → in-process eventbus consumer drain →
//     credential cascade → bob's sessions revoked + validate 401
//   - sessionvalidate epoch mismatch → 401 (claim epoch < user.authz_epoch)
//   - login uniform 401 wire shape: missing user / wrong password / inactive
//     account all collapse to same envelope (account-enumeration defense)
//   - ChangePassword wrong old password → 401 ERR_AUTH_OLD_PASSWORD_INCORRECT;
//     correct old password → 204 + authz_epoch bump + old access token 401
//
// Build tag: integration (testcontainer PG + in-process eventbus subscriber).
//
// Cross-reference: cmd/corebundle/setup_pg_integration_test.go already covers
// login outbox-failure → tx rollback (TestSessionLogin_OutboxFailureRollsBackPGRows).
// This package complements that with cross-layer L2 wire-shape and cascade
// verification, NOT a duplicate of single-slice tx atomicity.
//
// Why the harness is inlined here rather than imported from a shared package:
// the closest existing builder (cmd/corebundle/setup_pg_integration_test.go::
// newSessionPGHarness) lives in package main, which Go forbids importing from
// outside the binary. Extracting it requires converting cmd/corebundle to a
// library package + thin main.go shim — out of scope for this PR. The duplicated
// wiring (~250 lines) follows the same shape as the corebundle test harness;
// future extraction to tests/testutil/corebundle/ remains possible once the
// package main constraint is lifted.
package l2_atomicity
