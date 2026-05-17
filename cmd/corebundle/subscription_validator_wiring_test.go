package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// configSubscriberWithoutOwner is a minimal test cell that registers a
// config-event subscription intentionally missing cell.WithSubscriptionSliceID.
// It exercises the ConfigEventOwnerValidator path end-to-end through the
// bootstrap phase6 subscription-registration walker.
type configSubscriberWithoutOwner struct {
	*cell.BaseCell
}

func newConfigSubscriberWithoutOwner() *configSubscriberWithoutOwner {
	return &configSubscriberWithoutOwner{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{
			ID:             "testcellnoowner",
			Type:           "core",
			DurabilityMode: "demo",
		}),
	}
}

// Init registers a config-event subscription without SliceID — intentionally
// missing owner metadata — so the ConfigEventOwnerValidator injected via
// runtimeBaseOptions rejects it at phase6.
func (c *configSubscriberWithoutOwner) Init(ctx context.Context, reg cell.Registry) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	spec := contractspec.ContractSpec{
		ID:        "event.config.entry-upserted.v1",
		Kind:      "event",
		Transport: "amqp",
		Topic:     "event.config.entry-upserted.v1",
	}
	// Intentionally omit cell.WithSubscriptionSliceID to trigger validator.
	// K#07: cellID is the HARD positional parameter (4th) — passed explicitly here.
	return reg.Subscribe(spec,
		func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			return outbox.Ack()
		},
		"testcellnoowner", // consumerGroup
		"testcellnoowner", // cellID (HARD positional)
	)
}

// TestSubscriptionValidatorInjectedViaRuntimeBaseOptions is the E2E wiring
// guard for Finding 3 (PR #334 L4 review round-2): verifies that
// bootstrap.WithSubscriptionValidator(obmetrics.ConfigEventOwnerValidator) is
// wired by runtimeBaseOptions and that a config-topic subscription missing
// owner metadata (CellID+SliceID) causes bootstrap.Run to fail at phase6 with
// an error containing "owner metadata".
//
// Approach B (E2E reject): actual bootstrap.Run with a cell that registers a
// disqualified subscription; no private-field access or reflection required.
func TestSubscriptionValidatorInjectedViaRuntimeBaseOptions(t *testing.T) {
	shared := buildTestSharedDeps(t)

	// Build an assembly that contains our misbehaving test cell.
	asm := assembly.New(assembly.Config{
		ID:             "test-validator-wiring",
		DurabilityMode: cell.DurabilityDemo,
		Clock:          clock.Real(),
	})
	require.NoError(t, asm.Register(newConfigSubscriberWithoutOwner()))

	consumerBase, err := buildConsumerBase(shared)
	require.NoError(t, err)

	opts := runtimeBaseOptions(shared, asm, consumerBase, http.NewServeMux(), adapterInfoForSharedDeps(shared))
	// Wire the minimum required listeners so phase0–phase5 do not fail before
	// phase6. PrimaryListener uses the pre-built verifier from shared.JWTDeps
	// (no authProvider cell needed in the assembly).
	// HealthListener is required because runtimeBaseOptions adds
	// WithMetricsHandler, which demands a dedicated HealthListener.
	// InternalListener satisfies the internal guard requirement.
	opts = append(opts,
		bootstrap.WithListener(
			cell.PrimaryListener, "127.0.0.1:0",
			[]cell.ListenerAuth{cell.MustNewAuthJWT(shared.JWTDeps.verifier)},
		),
		bootstrap.WithListener(
			cell.HealthListener, "127.0.0.1:0",
			[]cell.ListenerAuth{cell.AuthNone{}},
		),
		bootstrap.WithListener(
			cell.InternalListener, shared.InternalHTTPAddr,
			buildInternalAuthChain(shared.InternalGuard),
		),
	)
	b := newBootstrapFromOptions(opts)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
	defer cancel()

	runErr := b.Run(ctx)

	require.Error(t, runErr, "bootstrap.Run must fail when a config subscription lacks owner metadata")
	assert.Contains(t, runErr.Error(), "owner metadata",
		"error must mention 'owner metadata' so operators can diagnose the registration-time failure")
}
