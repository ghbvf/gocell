//go:build integration

package l2atomicity

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/kernel/clock"
)

// ---------------------------------------------------------------------------
// TestL2Atomicity_setup_RollsBack (L2-OUTBOX-ATOMICITY-COVERAGE-01)
// ---------------------------------------------------------------------------

// TestL2Atomicity_setup_RollsBack proves that when the outbox writer fails
// inside setup.Service's transaction (setup/admin POST), the domain write
// (admin user INSERT) rolls back atomically and no admin user persists.
//
// The harness is created WITHOUT seeding an admin (newL2HarnessNoSeed) because
// setup/admin is non-idempotent: a second POST after the first succeeds returns
// 410 ERR_SETUP_ALREADY_INITIALIZED. The test itself drives POST
// /api/v1/access/setup/admin directly, controlling the outbox failure window.
//
// event.user.created.v1 is the topic emitted by setup.Service when it creates
// the initial admin user — the same topic as identitymanage creates for
// ordinary users (shared identitymanage.Service path).
//
// ref: rbacassign_atomicity_failureproof_e2e_test.go (canonical pattern)
// ref: Transactional Outbox Pattern (Microservices Patterns, Chris Richardson)
func TestL2Atomicity_setup_RollsBack(t *testing.T) {
	sw := newSelectiveFailWriter(adapterpg.NewOutboxWriter(clock.Real()))
	// Boot without seeding admin — the test drives setup/admin directly.
	h := newL2HarnessNoSeed(t, sw)

	setupBody, _ := json.Marshal(map[string]string{
		"username": adminUsername,
		"email":    adminEmail,
		"password": adminPassword,
	})
	newSetupBody := func() *bytes.Reader { return bytes.NewReader(setupBody) }

	require.False(t, setupHasAdmin(t, h.base),
		"baseline: no admin must exist before setup-under-test")

	// Trigger window: event.user.created.v1 is the topic emitted by
	// setup.Service when it creates the initial admin user.
	sw.failType.Store(eventUserCreatedV1)

	// The setup-under-test: POST /api/v1/access/setup/admin with bootstrap
	// basic auth. The outbox writer fails when setup.Service tries to emit
	// event.user.created.v1 inside the transaction, rolling back the INSERT.
	resp, err := postSetupAdmin(h.base, newSetupBody())
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode,
		"setup/admin must surface 500 when outbox writer fails")

	require.Equal(t, int64(1), sw.failCount.Load(),
		"writer.Write must have been invoked exactly once before transaction rollback")

	// Rollback proof: no admin persisted — setup/admin can be retried.
	require.False(t, setupHasAdmin(t, h.base),
		"admin MUST NOT persist after outbox-write failure rollback")

	// Negative control: with the trigger cleared, setup/admin must succeed
	// and an admin must be present.
	sw.failType.Store("")
	resp2, err := postSetupAdmin(h.base, newSetupBody())
	require.NoError(t, err)
	_ = resp2.Body.Close()
	require.Equal(t, http.StatusCreated, resp2.StatusCode,
		"setup/admin must return 201 on happy path after rollback")
	assert.True(t, setupHasAdmin(t, h.base),
		"control: admin MUST exist after successful setup/admin")
}
