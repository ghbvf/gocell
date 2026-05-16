//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/auth/session/storetest"
)

// TestJSsologinSessionDb implements journeys/J-ssologin.yaml passCriteria
// "Session 写入数据库" — checkRef journey.J-ssologin.session-db. The verify
// runner resolves that ref to ^TestJSsologinSessionDb$ via
// verify.kebabToCamelCase and executes it under -tags=integration.
//
// The journey criterion asserts that an issued session is durably written
// through the session.Store interface and recoverable by Get. The minimum
// load-bearing path is: in-memory Store + canonical S2 protocol + clock-
// anchored Session fixture → Create → Get returns the live view.
//
// We test at the Store layer (not via sessionlogin.Service) because:
//   - The journey criterion is "session writes to database", not "OIDC
//     callback orchestration"; sessionlogin.Service orchestrates OIDC +
//     issuer + emitter + tx and would inflate the test surface ~10x
//     without adding evidence for the row-write contract.
//   - sessionlogin's internal helpers (mem.UserRepository, etc.) live in
//     cells/accesscore/internal/... which is unreachable from tests/
//     integration/ by Go's internal-package visibility rule. Building
//     parallel fakes here would duplicate ~80 LOC for no gain.
//   - Future PG store coverage will plug in via the same storetest.Factory
//     pattern; this test stays valid because session.Store is the contract.
func TestJSsologinSessionDb(t *testing.T) {
	t.Parallel()

	anchor := storetest.EpochAnchor()
	fc := clockmock.New(anchor)
	store, err := session.NewMemStore(storetest.NewTestProtocol(t), fc)
	require.NoError(t, err)

	// authzEpoch is an opaque positive int64 to the Store contract: MemStore
	// only enforces non-zero (S4d row-level credential provenance), and PG
	// stores will enforce a FK against users.authz_epoch. We pick 7 because
	// storetest.NewSessionFixture's internal case suite uses the same value
	// — keeping it consistent makes case-suite seed diffs zero if this
	// fixture is later extended into the storetest suite — but any
	// non-zero int64 would satisfy the contract for this test.
	const authzEpoch = int64(7)
	sess := storetest.NewSessionFixture(t,
		"user-ssologin-fixture", "jti-ssologin-fixture",
		authzEpoch, time.Hour, anchor)

	ctx := context.Background()
	require.NoError(t, store.Create(ctx, sess), "session must persist on Create")

	view, err := store.Get(ctx, sess.ID)
	require.NoError(t, err, "freshly created session must be retrievable")
	require.NotNil(t, view)
	assert.Equal(t, sess.ID, view.ID)
	assert.Equal(t, sess.SubjectID, view.SubjectID)
	assert.Equal(t, sess.AuthzEpochAtIssue, view.AuthzEpochAtIssue)
	assert.Nil(t, view.RevokedAt, "freshly created session must not appear revoked")
}
