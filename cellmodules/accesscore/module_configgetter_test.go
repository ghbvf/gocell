package accesscore

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/composition"
	"github.com/ghbvf/gocell/framework/runtime/transport"
)

// configGetterTestDeps builds a minimal SharedDeps for wireConfigGetter: the
// deployment-topology spec under test + an (optional) in-process transport + a
// valid keyring + clock. Only the fields wireConfigGetter reads are set.
func configGetterTestDeps(t *testing.T, spec bootstrap.DeploymentTopologySpec, tp *transport.InProcessTransport) *composition.SharedDeps {
	t.Helper()
	ring, err := auth.NewHMACKeyRing([]byte("test-hmac-key-32-bytes-long-xxxxx"), nil)
	require.NoError(t, err)
	return &composition.SharedDeps{
		DeploymentTopology:     spec,
		InProcessTransport:     tp,
		InternalServiceKeyring: ring,
		Clock:                  clock.Real(),
	}
}

// TestWireConfigGetter_Colocated_InjectsGetter: empty topology (all co-located)
// → the config getter is wired through the in-process transport.
func TestWireConfigGetter_Colocated_InjectsGetter(t *testing.T) {
	shared := configGetterTestDeps(t, bootstrap.DeploymentTopologySpec{}, transport.NewInProcess(nil))
	opts, res, err := wireConfigGetter(shared, nil)
	require.NoError(t, err)
	assert.Len(t, opts, 1, "colocated configcore must append exactly the config getter option")
	assert.Empty(t, res, "colocated configcore has no remote peer → no readiness resource")
}

// TestWireConfigGetter_RemoteConfigcore_InjectsGetter: configcore declared remote
// → the config getter is wired through a RemoteHTTPTransport (US5 #1966). This
// replaces the old US4 fail-fast placeholder: the remote CellTransport is now
// implemented, so a remote configcore is a valid (non-error) configuration.
func TestWireConfigGetter_RemoteConfigcore_InjectsGetter(t *testing.T) {
	spec := bootstrap.DeploymentTopologySpec{
		Colocated: []string{"accesscore"},
		Remote:    []bootstrap.RemoteCellEndpoint{{CellID: "configcore", Endpoint: "configcore:9090"}},
	}
	shared := configGetterTestDeps(t, spec, transport.NewInProcess(nil))
	opts, res, err := wireConfigGetter(shared, nil)
	require.NoError(t, err, "remote configcore must now succeed — US5 wires the RemoteHTTPTransport")
	assert.Len(t, opts, 1, "remote configcore must append exactly the config getter option")
	// Remote configcore contributes a TCP-dial readiness probe (#2251 P2.7).
	require.Len(t, res, 1, "remote configcore must contribute one readiness resource")
	probes := res[0].Probes()
	require.Len(t, probes, 1)
	assert.Equal(t, "configcore_remote_ready", probes[0].Name().String())
}

// TestWireConfigGetter_NilTransport_FailFast: colocated configcore but the
// composition root failed to mint the in-process transport → fail-fast.
func TestWireConfigGetter_NilTransport_FailFast(t *testing.T) {
	shared := configGetterTestDeps(t, bootstrap.DeploymentTopologySpec{}, nil)
	_, _, err := wireConfigGetter(shared, nil)
	require.Error(t, err)
	errcodetest.AssertCode(t, err, errcode.ErrCellInvalidConfig)
}

// TestWireConfigGetter_Unclassified_FailFast: explicit topology where configcore
// is neither colocated nor remote → fail-fast (not silently treated as in-proc).
func TestWireConfigGetter_Unclassified_FailFast(t *testing.T) {
	spec := bootstrap.DeploymentTopologySpec{Colocated: []string{"accesscore"}}
	shared := configGetterTestDeps(t, spec, transport.NewInProcess(nil))
	_, _, err := wireConfigGetter(shared, nil)
	require.Error(t, err)
	errcodetest.AssertCode(t, err, errcode.ErrCellInvalidConfig)
}
