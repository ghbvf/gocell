//go:build e2e && pg

package sessionprojection

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/tests/e2e/internal/clients"
	e2erequire "github.com/ghbvf/gocell/tests/e2e/internal/require"
)

// registrySummaryPath is the read endpoint served by the accesscore
// sessionprojection slice (http.session.registry-summary.v1).
const registrySummaryPath = "/api/v1/access/sessions/registry-summary"

// TestE2E_SessionRegistryProjection_LiveCountConverges proves the EPIC #1504
// PR-04 production-default flip end-to-end through the real corebundle binary:
// the gate is gone, so accesscore's declared session_registry projection gets
// the durable projection_events source wired by default; a session.created
// event (the bootstrap-admin login already produced one for the admin's tenant)
// is journaled by the same-tx double-write decorator, relayed, consumed live by
// the framework Coordinator, and folded into the in-memory read model — which
// the tenant-scoped summary endpoint then serves. We poll until the count
// converges to ≥ 1 (live consumption is asynchronous: emit → outbox → relay →
// broker → ConsumerBase → Apply).
func TestE2E_SessionRegistryProjection_LiveCountConverges(t *testing.T) {
	e2erequire.Docker(t)
	e2erequire.PG(t)
	clients.WaitForReady(t, testtime.CtxLong)
	token := clients.AdminToken(t)

	// The admin token carries the bootstrap tenant; the summary is tenant-scoped
	// to it. The admin's own login (bootstrap-admin.sh) is the source session, so
	// the converged count is ≥ 1. Poll because the projection consumes the event
	// asynchronously off the broker.
	got := pollTotalSessions(t, token, 1, testtime.CtxLong)
	assert.GreaterOrEqual(t, got, int64(1),
		"session_registry projection must converge to ≥ 1 session for the admin tenant "+
			"(durable source wired by default after the #1771 gate removal)")
}

// TestE2E_SessionRegistryProjection_AnonymousRejected is the negative auth
// guard: an anonymous GET must be rejected (401/403). Without it, a token-loss
// regression would let the convergence test "pass" against an unauthed endpoint
// and silently mask an auth break (same rationale as the encryption suite).
func TestE2E_SessionRegistryProjection_AnonymousRejected(t *testing.T) {
	e2erequire.Docker(t)
	e2erequire.PG(t)
	clients.WaitForReady(t, testtime.CtxLong)

	resp := clients.DoJSON(t, http.MethodGet, registrySummaryPath, nil, "")
	defer resp.Body.Close()
	assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, resp.StatusCode,
		"anonymous GET %s must be 401 or 403, got %d", registrySummaryPath, resp.StatusCode)
}

// pollTotalSessions polls the registry-summary endpoint until data.totalSessions
// reaches want or the timeout elapses, returning the last observed value. Uses
// the real clock (e2e runs against live services). A non-200 response is a hard
// failure — the endpoint must be mounted and authorized for the admin token.
func pollTotalSessions(t *testing.T, token string, want int64, timeout time.Duration) int64 {
	t.Helper()
	clk := clock.Real()
	deadline := clk.Now().Add(timeout)
	var last int64
	for clk.Now().Before(deadline) {
		last = fetchTotalSessions(t, token)
		if last >= want {
			return last
		}
		_ = clk.Sleep(context.Background(), clk.Now().Add(500*time.Millisecond))
	}
	return last
}

// fetchTotalSessions does one GET and decodes data.totalSessions, asserting 200.
func fetchTotalSessions(t *testing.T, token string) int64 {
	t.Helper()
	resp := clients.DoJSON(t, http.MethodGet, registrySummaryPath, nil, token)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"authorized GET %s must return 200", registrySummaryPath)

	var body struct {
		Data struct {
			TotalSessions int64 `json:"totalSessions"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body.Data.TotalSessions
}
