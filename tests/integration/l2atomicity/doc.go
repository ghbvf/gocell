//go:build integration

// Package l2atomicity is the L2 (OutboxFact) e2e harness for accesscore
// session/refresh/revoke/validate fail-closed regression coverage. It boots
// a full PG + outbox-relay + in-process eventbus assembly (no broker;
// RabbitMQ-bound broker e2e tracked as a follow-up in
// docs/backlog/cap-14-tooling.md::L2-ATOMICITY-HARNESS-FOLLOWUPS) and
// exercises:
//
//   - login → sessions row + refresh_tokens row + outbox event committed atomically
//   - refresh → stable sid (OAuth2/OIDC sid invariant) + jti claim; epoch
//     provenance lives on sessions.authz_epoch_at_issue (S4d removed the
//     authz_epoch JWT claim)
//   - refresh reuse detected → credentialinvalidate funnel cascades (BumpAuthzEpoch +
//     RevokeForSubject + RevokeUser) under same tx
//   - rbacassign Revoke → same-tx credentialinvalidate funnel revokes the
//     victim's sessions + refresh chains synchronously (the cascade itself
//     is intra-tx, not eventbus-driven); _additionally_, the L2 outbox row
//     committed alongside is drained by runtime/outbox.NewRelay, republished
//     into the in-process eventbus, and observed by the auditcore subscriber
//     advancing the audit chain Tail. This is the producer → relay →
//     publisher → consumer evidence on the in-process transport.
//   - sessionvalidate epoch mismatch → 401 (session.authz_epoch_at_issue <
//     users.authz_epoch via row provenance)
//   - login uniform 401 wire shape: missing user / wrong password / inactive
//     account all collapse to same envelope (account-enumeration defense)
//   - ChangePassword wrong old password → 401 ERR_AUTH_OLD_PASSWORD_INCORRECT;
//     correct old password → 200 + new token pair + authz_epoch bump + old
//     access/refresh tokens revoked
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
//
// Wire-shape coverage scope: this harness asserts the programmatic integration
// shape (Go test). The declarative journey-YAML coverage (J-accountlockout /
// J-ssologin uniform-401 reverse assertion) is owned by FU-4 in plan §S4-FU;
// see docs/plans/202605082145-034-pg-corecell-b-route-plan.md §T4 ↔ FU-4.
//
// Running locally: `go test -tags=integration -count=1 ./tests/integration/l2atomicity/...`
// (requires Docker for testcontainer Postgres; bcrypt cost=12 takes ~5-15s under `-race`).
// Race lane: `go test -race -tags=integration -count=1 -timeout=15m ./tests/integration/l2atomicity/...`.
package l2atomicity
