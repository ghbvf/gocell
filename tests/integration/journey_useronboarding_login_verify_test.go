//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/auth/session/storetest"
)

// TestJUseronboardingLoginVerify implements journeys/J-useronboarding.yaml
// passCriteria "新用户可成功登录" — checkRef
// journey.J-useronboarding.login-verify. The verify runner resolves that
// ref to ^TestJUseronboardingLoginVerify$ via verify.kebabToCamelCase and
// executes it under -tags=integration via `gocell verify journey
// --id=J-useronboarding`. J-useronboarding is lifecycle: active, so
// governance VERIFY-06 (kernel/governance/rules_verify.go function
// validateVERIFY06Journey) runs this test inside `gocell validate --strict`
// — so this test MUST be Docker-free, mirroring TestJSsologinSessionDb.
//
// The journey criterion asserts that an onboarded user, on first
// authenticated session issue, is durably written through the session.Store
// interface and recoverable by Get. The minimum load-bearing path is:
// in-memory Store + canonical S2 protocol + clock-anchored Session fixture
// → Create → Get returns the live view with subject + authz_epoch + nil
// revocation intact.
//
// Layer compromise (same philosophy as TestJSsologinSessionDb): we test at
// the runtime/auth/session.Store layer rather than driving identitymanage.
// Service.Create + sessionlogin.Service.Login + the full PG/outbox/relay
// roundtrip because:
//   - The criterion is "newly-onboarded user can successfully sign in",
//     proven at the session-row persistence seam; the full user-create →
//     role-assign → login chain has independent in-package and l2atomicity
//     coverage (corecells/accesscore/slices/identitymanage/* tests +
//     tests/integration/l2atomicity/login_refresh_e2e_test.go).
//   - corecells/accesscore/internal/... is unreachable from tests/integration/
//     by Go's internal-package rule, so wiring identitymanage.NewService
//     from here is not possible Docker-free (it needs *credentialinvalidate.
//     Invalidator which lives under internal/). The full programmatic proof
//     is owned by tests/integration/l2atomicity/ with testcontainers PG.
//   - session.Store is the contract for "session persisted"; asserting at
//     that seam IS asserting the persistence-shape contract.
//
// J-useronboarding has 4 passCriteria. login-verify is the ONLY criterion
// with a Docker-free load-bearing seam reachable from tests/integration/.
// The other 3 (user-create / role-assign / event-publish) are declared
// mode: manual because their production code paths cross the cells/
// accesscore/internal/ boundary (identitymanage.Service / rbacassign.
// Service constructors require internal credentialinvalidate.Invalidator)
// and have no meaningful Docker-free seam at tests/integration/. They will
// be promoted back to mode: auto by JOURNEY-USERONBOARDING-AUTO-EXPANSION-01
// when either (a) cmd/corebundle is extracted to a library package and a
// shared tests/testutil/corebundle/ harness becomes importable, or (b)
// tests/integration/l2atomicity/ adds a J-useronboarding testcontainers
// e2e sub-suite. The journey YAML inline-comments each manual criterion with
// JOURNEY-USERONBOARDING-AUTO-EXPANSION-01 so a future AI co-author does
// not silently revert mode: manual → mode: auto without providing one of
// the two harness options first.
func TestJUseronboardingLoginVerify(t *testing.T) {
	t.Parallel()

	anchor := storetest.EpochAnchor()
	fc := clockmock.New(anchor)
	store, err := session.NewMemStore(storetest.NewTestProtocol(t), fc)
	require.NoError(t, err)

	// authzEpoch is an opaque positive int64 to the Store contract: MemStore
	// only enforces non-zero (S4d row-level credential provenance), and PG
	// stores will enforce a FK against users.authz_epoch. We pick 7 to keep
	// parity with TestJSsologinSessionDb's fixture seed; any non-zero int64
	// would satisfy the contract.
	const authzEpoch = int64(7)
	sess := storetest.NewSessionFixture(t,
		"user-useronboarding-fixture", "jti-useronboarding-fixture",
		authzEpoch, time.Hour, anchor)

	testTenantID, err := tenant.ParseTenantID("00000000-0000-0000-0000-000000000001")
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, store.Create(ctx, testTenantID, sess),
		"newly-onboarded user's first session must persist on Create")

	view, err := store.Get(ctx, sess.ID)
	require.NoError(t, err,
		"freshly created session must be retrievable for the onboarded user")
	require.NotNil(t, view)
	assert.Equal(t, sess.ID, view.ID)
	assert.Equal(t, sess.SubjectID, view.SubjectID,
		"subject id must round-trip — onboarded user identity is the credential anchor")
	assert.Equal(t, sess.AuthzEpochAtIssue, view.AuthzEpochAtIssue,
		"authz_epoch provenance must round-trip — bound at session issue per S4d row-level pin")
	assert.Nil(t, view.RevokedAt,
		"first session of an onboarded user must not appear revoked")
}
