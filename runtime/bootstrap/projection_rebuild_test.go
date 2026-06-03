package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kauth "github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/projection"
)

// fakeRebuildController is a test double for the unexported rebuildController so
// the rebuild handler can be exercised without constructing a full Coordinator.
// The real implementer is *projection.Coordinator (kernel-tested separately).
type fakeRebuildController struct {
	rebuildErr   error
	snap         projection.Snapshot
	snapErr      error
	rebuildCalls int
}

func (f *fakeRebuildController) Rebuild(context.Context) error {
	f.rebuildCalls++
	return f.rebuildErr
}

func (f *fakeRebuildController) Snapshot(context.Context) (projection.Snapshot, error) {
	return f.snap, f.snapErr
}

var _ rebuildController = (*fakeRebuildController)(nil)

// serveMuxRouteMux adapts *http.ServeMux to cell.RouteMux so a RouteGroup's
// Register (which only calls Handle for a top-level non-prefixed mount) can be
// invoked in tests and the result served through Go 1.22 path-value routing.
type serveMuxRouteMux struct{ *http.ServeMux }

func (serveMuxRouteMux) Route(string, func(cell.RouteMux))                       {}
func (serveMuxRouteMux) Mount(string, http.Handler)                              {}
func (serveMuxRouteMux) Group(func(cell.RouteMux))                               {}
func (m serveMuxRouteMux) With(...func(http.Handler) http.Handler) cell.RouteMux { return m }

var _ cell.RouteMux = serveMuxRouteMux{}

// mountRebuildEndpoint wires b's rebuild RouteGroup onto a fresh ServeMux via the
// real production projectionRebuildRouteGroup().Register, asserting the group
// targets the AdminListener. The RouteGroup carries no operator-credential
// middleware (that is the listener-level AuthOperator chain, applied by
// bootstrap, not by the RouteGroup) — these handler tests exercise the
// 202/409/404/500 logic directly; the operator gate is tested via the
// apply-switch (TestApplyListenerAuthChain_Operator) and runtime/auth.
func mountRebuildEndpoint(t *testing.T, b *Bootstrap) *http.ServeMux {
	t.Helper()
	rg := b.projectionRebuildRouteGroup()
	require.Equal(t, cell.AdminListener, rg.Listener, "rebuild endpoint must mount on the AdminListener")
	mux := http.NewServeMux()
	require.NoError(t, rg.Register(serveMuxRouteMux{mux}))
	return mux
}

// rebuildBootstrap returns a Bootstrap with the rebuild endpoint opted in and the
// given registry contents.
func rebuildBootstrap(reg map[string]rebuildController) *Bootstrap {
	b := New(clock.Real(), WithProjectionRebuildEndpoint())
	b.projectionRebuilds = reg
	return b
}

// doRebuild issues a POST to the migrated admin path and returns the recorder.
// operator→system: no caller-cell principal.
func doRebuild(mux *http.ServeMux, cellID, projID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/admin/v1/projection/"+cellID+"/"+projID+"/rebuild", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestProjectionRebuild_202 asserts an admitted rebuild returns 202 with the
// {phase, pendingEvents, replayLagSeconds} snapshot body.
func TestProjectionRebuild_202(t *testing.T) {
	t.Parallel()
	fake := &fakeRebuildController{snap: projection.Snapshot{Phase: projection.PhaseLive, PendingEvents: 5, ReplayLagSeconds: 12.5}}
	b := rebuildBootstrap(map[string]rebuildController{"ordercell/order_status": fake})
	mux := mountRebuildEndpoint(t, b)

	rec := doRebuild(mux, "ordercell", "order_status")

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, 1, fake.rebuildCalls)
	var got projectionRebuildResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "live", got.Data.Phase)
	assert.Equal(t, int64(5), got.Data.PendingEvents)
	assert.InDelta(t, 12.5, got.Data.ReplayLagSeconds, 0.0001)
}

// TestProjectionRebuild_409 asserts an in-progress rebuild returns 409.
func TestProjectionRebuild_409(t *testing.T) {
	t.Parallel()
	fake := &fakeRebuildController{rebuildErr: projection.ErrRebuildInProgress}
	b := rebuildBootstrap(map[string]rebuildController{"ordercell/order_status": fake})
	mux := mountRebuildEndpoint(t, b)

	rec := doRebuild(mux, "ordercell", "order_status")

	require.Equal(t, http.StatusConflict, rec.Code)
}

// TestProjectionRebuild_404 asserts an unknown projection — on either the cell or
// the name dimension of the {cell}/{name} key — returns 404 with the
// ErrProjectionNotFound code.
func TestProjectionRebuild_404(t *testing.T) {
	t.Parallel()
	// Registry holds only ordercell/order_status.
	b := rebuildBootstrap(map[string]rebuildController{
		"ordercell/order_status": &fakeRebuildController{},
	})
	mux := mountRebuildEndpoint(t, b)

	cases := []struct{ name, cell, proj string }{
		{"unknown projection name", "ordercell", "nosuchprojection"},
		{"unknown cell", "nosuchcell", "order_status"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := doRebuild(mux, tc.cell, tc.proj)
			require.Equal(t, http.StatusNotFound, rec.Code)
			assert.Contains(t, rec.Body.String(), "ERR_PROJECTION_NOT_FOUND")
		})
	}
}

// TestProjectionRebuild_DegradedSnapshotStill202 asserts a snapshot read error
// after admission does not downgrade the already-admitted rebuild to a 5xx.
func TestProjectionRebuild_DegradedSnapshotStill202(t *testing.T) {
	t.Parallel()
	// Distinctive non-default Phase so the assertion proves the handler passed the
	// (degraded) snapshot's valid Phase through, not a zeroed/default Snapshot.
	fake := &fakeRebuildController{
		snap:    projection.Snapshot{Phase: projection.PhaseLive},
		snapErr: errors.New("checkpoint store unreachable"),
	}
	b := rebuildBootstrap(map[string]rebuildController{"ordercell/order_status": fake})
	mux := mountRebuildEndpoint(t, b)

	rec := doRebuild(mux, "ordercell", "order_status")

	require.Equal(t, http.StatusAccepted, rec.Code)
	var got projectionRebuildResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "live", got.Data.Phase) // Phase still valid; pending/lag zeroed
	assert.Equal(t, int64(0), got.Data.PendingEvents)
}

// TestProjectionRebuild_UnexpectedRebuildError500 asserts that a Rebuild error
// other than ErrRebuildInProgress (unreachable for a registered coordinator) maps
// to a framework 500, not a client 400 — keeping the wire status set to the
// ADR-frozen 202/409/404 plus the implicit framework 5xx.
func TestProjectionRebuild_UnexpectedRebuildError500(t *testing.T) {
	t.Parallel()
	fake := &fakeRebuildController{rebuildErr: errors.New("coordinator not subscribed")}
	b := rebuildBootstrap(map[string]rebuildController{"ordercell/order_status": fake})
	mux := mountRebuildEndpoint(t, b)

	rec := doRebuild(mux, "ordercell", "order_status")

	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

// TestProjectionRebuild_LongPathParamClamped asserts an oversized path param is
// truncated in the 404 body (bounds the response/log against an oversized path).
func TestProjectionRebuild_LongPathParamClamped(t *testing.T) {
	t.Parallel()
	b := rebuildBootstrap(map[string]rebuildController{})
	mux := mountRebuildEndpoint(t, b)

	longName := strings.Repeat("a", 200)
	rec := doRebuild(mux, "ordercell", longName)

	require.Equal(t, http.StatusNotFound, rec.Code)
	// The full 200-char value must NOT appear verbatim — it is clamped to 64.
	assert.NotContains(t, rec.Body.String(), longName)
	assert.Contains(t, rec.Body.String(), strings.Repeat("a", maxPathParamReport))
}

// TestValidateProjectionRebuildEndpoint covers the phase0 fail-fast: opting into
// the endpoint requires an AdminListener; not opting in is a no-op.
func TestValidateProjectionRebuildEndpoint(t *testing.T) {
	t.Parallel()

	t.Run("not opted in → ok", func(t *testing.T) {
		t.Parallel()
		b := New(clock.Real())
		require.NoError(t, b.validateProjectionRebuildEndpoint())
	})

	t.Run("opted in without AdminListener → error", func(t *testing.T) {
		t.Parallel()
		b := New(clock.Real(), WithProjectionRebuildEndpoint())
		err := b.validateProjectionRebuildEndpoint()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "AdminListener")
	})

	t.Run("opted in with AdminListener → ok", func(t *testing.T) {
		t.Parallel()
		b := New(clock.Real(),
			WithListener(cell.AdminListener, "127.0.0.1:0", []kauth.ListenerAuth{newTestOperatorAuth(t)}),
			WithProjectionRebuildEndpoint())
		require.NoError(t, b.validateProjectionRebuildEndpoint())
	})
}

// newTestOperatorAuth builds a valid AuthOperator for listener-config tests.
func newTestOperatorAuth(t *testing.T) kauth.AuthOperator {
	t.Helper()
	op, err := kauth.NewAuthOperator([]byte("ops"), []byte("s3cret"), allowAllLimiter{}, nil)
	require.NoError(t, err)
	return op
}

// allowAllLimiter is a permissive OperatorRateLimiter for tests that only need a
// valid (non-nil) limiter.
type allowAllLimiter struct{}

func (allowAllLimiter) Allow(string) bool { return true }
