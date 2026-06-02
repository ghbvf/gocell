package correlate_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/ghbvf/gocell/runtime/observability/correlate"
)

// TestCorrelateRouteGroups_MountsOnPrimaryWithoutClientsError is the regression
// guard for issue #1048: the correlate endpoint previously sat at
// /internal/v1/audit/correlate as a framework route, which crashed bootstrap at
// auth.Mount → wrapper.HTTPHandler → ContractSpec.validateHTTP ("internal path
// requires non-empty Clients"). No unit/integration test mounted the route
// through the real router, so only the e2e full-boot caught it.
//
// This test reproduces the exact mount path (Router.MountRouteGroup → Register →
// auth.Mount → validateHTTP, then FinalizeAuth) on a PrimaryListener router and
// asserts it succeeds. It would fail with the old internal-path placement and
// passes now that the route is GET /api/v1/observability/correlate (non-internal,
// no Clients) gated by an admin-role Policy.
func TestCorrelateRouteGroups_MountsOnPrimaryWithoutClientsError(t *testing.T) {
	t.Parallel()

	svc, err := correlate.NewService(newTestStore(nil, nil), nil, nil)
	require.NoError(t, err, "NewService")

	groups := correlate.CorrelateRouteGroups(svc)
	require.Len(t, groups, 1, "correlate must contribute exactly one RouteGroup")
	require.Equal(t, cell.PrimaryListener, groups[0].Listener,
		"correlate RouteGroup must target PrimaryListener (not InternalListener)")

	r, err := router.NewForListener(clock.Real(), cell.PrimaryListener)
	require.NoError(t, err, "NewForListener(primary)")

	// MountRouteGroup runs Register → auth.Mount → wrapper.HTTPHandler →
	// ContractSpec.validateHTTP. A non-internal path with no Clients is valid;
	// the old /internal/v1/* path with no Clients returned an error here.
	require.NoError(t, r.MountRouteGroup(groups[0]),
		"correlate must mount on PrimaryListener without the internal-path Clients error (#1048 regression)")

	require.NoError(t, r.FinalizeAuth(),
		"correlate route must finalize cleanly on PrimaryListener")
}
