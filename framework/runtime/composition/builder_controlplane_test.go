package composition

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/idempotency"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
)

// TestBuilder_InjectsControlPlaneTopology_RejectsDivergentInternalStore is the
// end-to-end proof that composition.Builder.Build closes the #1410 review F1
// loop: it injects the trusted SharedDeps.Topology into bootstrap
// (WithControlPlaneTopology), so bootstrap phase0 validates the store that
// ACTUALLY guards /internal/v1/* — built by the caller's RuntimeOptionsFunc —
// rather than only the declared SharedDeps.NonceStore.
//
// The scenario is precisely the bypass F1 warns about: SharedDeps.NonceStore is a
// valid distributed store (so SharedDeps.validate passes), but the
// RuntimeOptionsFunc builds the internal-listener AuthServiceToken from a DIFFERENT,
// single-process in-memory store. In a real multi-pod topology that store is not
// replay-safe; phase0 must reject it at startup. Before this fix the divergent
// store slipped through because nothing validated the actual auth-plan store.
func TestBuilder_InjectsControlPlaneTopology_RejectsDivergentInternalStore(t *testing.T) {
	ctx := context.Background()

	// Real multi-pod SharedDeps with a distributed nonce store + claimer so
	// SharedDeps.validate() itself passes — the gap is downstream of it.
	shared := buildValidRealModeSharedDeps(t)
	shared.Topology = realMultiPodTopo(t)
	distNonce, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, shared.Clock)
	require.NoError(t, err)
	shared.NonceStore = fakeDistributedNonceStore{distNonce}
	shared.ConsumerClaimer = fakeDistributedClaimer{idempotency.NewInMemClaimer(shared.Clock)}
	require.NoError(t, shared.validate(), "real multi-pod baseline must validate before the divergence")

	// The RuntimeOptionsFunc guards the internal listener with a DIVERGENT
	// in-memory store — not shared.NonceStore. This is the opaque-callback bypass.
	runtimeOptsFn := func([]cell.Cell) ([]bootstrap.Option, error) {
		divergent, derr := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, shared.Clock)
		if derr != nil {
			return nil, derr
		}
		svcTok, terr := kauth.NewAuthServiceToken(divergent, shared.InternalHMACRing)
		if terr != nil {
			return nil, terr
		}
		return []bootstrap.Option{
			bootstrap.WithListener(cell.InternalListener, "127.0.0.1:0",
				[]kauth.ListenerAuth{svcTok}),
		}, nil
	}

	app, err := New("mod1").
		With(&fakeCellModule{id: "mod1", cell: stubCell("mod1")}).
		Build(ctx, shared, runtimeOptsFn)
	require.NoError(t, err, "Build itself succeeds — the divergence is caught at Run/phase0")
	require.NotNil(t, app)

	// App.Run reaches bootstrap phase0, which now validates the ACTUAL in-memory
	// auth-plan store against the injected multi-pod topology and rejects it. Run
	// in a goroutine with a timeout so a hang (rather than the expected fast
	// phase0 failure) is caught.
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	select {
	case runErr := <-done:
		require.Error(t, runErr, "phase0 must reject the divergent in-memory internal store")
		var ecErr *errcode.Error
		require.True(t, errors.As(runErr, &ecErr), "want *errcode.Error from phase0")
		assert.Equal(t, errcode.ErrControlplaneNonceStoreMissing, ecErr.Code,
			"the rejection must be the topology-dependent replay-safe check (F1 loop closed)")
		assert.Contains(t, ecErr.Message, "not replay-safe",
			"message must name the replay-safe failure")
	case <-time.After(appRunCancelTimeout):
		t.Fatal("App.Run did not return; phase0 rejection should be immediate")
	}
}
