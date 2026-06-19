package certdeps

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
)

// mkTopo builds a validated Topology for the test matrix. The constructor only
// errors on an illegal adapter/storage coupling, none of which these cases hit.
func mkTopo(t *testing.T, adapterMode, storageBackend string) bootstrap.Topology {
	t.Helper()
	topo, err := bootstrap.NewTopology(adapterMode, storageBackend, false)
	require.NoError(t, err)
	return topo
}

// TestResolve_DemoMemory_SoftCA: the demo/memory branch wires a working softca
// signer + revocation store (sharing one issuance ledger by construction). A
// non-empty TrustBundle proves a real CA was wired, not a nil stub.
func TestResolve_DemoMemory_SoftCA(t *testing.T) {
	t.Parallel()

	got, err := Resolve(clock.Real(), mkTopo(t, "", "memory"))
	require.NoError(t, err)
	require.NotNil(t, got.Signer)
	require.NotNil(t, got.RevocationStore)

	bundle, err := got.Signer.TrustBundle(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, bundle, "demo softca must expose a non-empty trust bundle")
}

// TestResolve_Postgres_FailClosed: the postgres branch is fail-closed — no
// durable CA backend exists to wire, so Resolve MUST error rather than silently
// serve the dev soft-CA (rotating trust anchor + in-memory ledger). The zero
// CertDeps proves it does not fail-open.
func TestResolve_Postgres_FailClosed(t *testing.T) {
	t.Parallel()

	got, err := Resolve(clock.Real(), mkTopo(t, "real", "postgres"))
	require.Error(t, err)
	require.ErrorContains(t, err, "durable signing CA")
	require.Nil(t, got.Signer)
	require.Nil(t, got.RevocationStore)
}
