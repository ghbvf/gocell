package bootstrap

// phases_dispatch_test.go — unit tests for the phase6 drainWebhookDispatchers drain.
//
// Coverage:
//   - empty dispatchers → no-op (no error, router not required)
//   - dispatchers present + nil webhookSourceStore → fail-fast ErrWebhookConfigInvalid
//   - dispatchers present + nil Subscriber → phase6 fail-fast (no silent drop)
//   - CellID drift (req.Spec.CellID != snapshot owner) → drift error
//   - happy path: a snapshot with one WebhookDispatchRequest + a seeded SourceStore
//     + a non-nil Subscriber + a ConsumerBase → drainWebhookDispatchers registers
//     the consumer on the event router (HandlerCount == 1, no error).
//
// Mirrors the phase5 receiver drain tests in phases_webhook_test.go (same
// buildPhaseStateWithWebhookCells / whTestCellID / whTestSourceID fixtures).

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/metadata"
	kwh "github.com/ghbvf/gocell/kernel/webhook"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/eventrouter"
)

// ---------------------------------------------------------------------------
// webhookDispatchTestCell — registers one outbound dispatcher via Init.
// ---------------------------------------------------------------------------

// webhookDispatchTestCell registers one WebhookDispatchRequest via
// reg.RegisterWebhookDispatch in its Init. overrideCellID is used to simulate
// codegen drift (spec.CellID != snapshot owner).
type webhookDispatchTestCell struct {
	*cell.BaseCell
	spec           kwh.DispatchSpec
	selector       kwh.WebhookDispatchSelector
	overrideCellID string
}

func (c *webhookDispatchTestCell) Init(ctx context.Context, reg cell.Registrar) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	spec := c.spec
	if c.overrideCellID != "" {
		spec.CellID = c.overrideCellID
	}
	return reg.RegisterWebhookDispatch(spec, c.selector)
}

// newWebhookDispatchCell creates a webhookDispatchTestCell whose spec.CellID
// equals the BaseCell ID (matching the snapshot key produced by assembly.Snapshots).
func newWebhookDispatchCell(cellID, contractID, sourceID string) *webhookDispatchTestCell {
	spec := kwh.DispatchSpec{
		ContractID: contractID,
		SourceID:   sourceID,
		CellID:     cellID,
	}
	sel := func(_ context.Context, _ []byte) (string, error) {
		return "http://example.test/hook", nil
	}
	return &webhookDispatchTestCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{
			ID:   cellID,
			Type: "core",
		}),
		spec:     spec,
		selector: sel,
	}
}

// Compile-time check.
var _ cell.Cell = (*webhookDispatchTestCell)(nil)

// dispatchTestContractID is the contract ID for dispatch drain tests.
// Using a unique value avoids collision with the receiver contract.
const dispatchTestContractID = "webhook.dispatch-test.v1"

// ---------------------------------------------------------------------------
// newDispatchEvtRouter builds a minimal eventrouter.Router with a real
// subscriber (eventbus) and consumerBase for the happy-path test.
// ---------------------------------------------------------------------------

func newDispatchEvtRouter(t *testing.T, b *Bootstrap) *eventrouter.Router {
	t.Helper()
	bus := eventbus.New(clockmock.New(whFixedNow))
	swm, err := b.buildEventRouter(bus)
	require.NoError(t, err, "buildEventRouter must succeed for dispatch happy path")
	return swm
}

// ---------------------------------------------------------------------------
// drainWebhookDispatchers — unit tests
// ---------------------------------------------------------------------------

// TestDrainWebhookDispatchers_EmptyNoOp verifies that a phase state with no
// webhook dispatchers returns nil without touching the router.
func TestDrainWebhookDispatchers_EmptyNoOp(t *testing.T) {
	t.Parallel()
	// Plain cell with no webhook dispatchers.
	tc := &testCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: "plain-cell", Type: "core"}),
	}
	s := buildPhaseStateWithWebhookCells(t, tc)

	b := New(clockmock.New(whFixedNow))
	// Pass a nil router: empty dispatch must not touch it.
	err := b.drainWebhookDispatchers(s, nil)

	require.NoError(t, err, "no dispatchers must be a no-op (nil router is fine)")
}

// TestDrainWebhookDispatchers_FailsWithoutSourceStore verifies that a snapshot
// with one dispatcher but no WithWebhookSourceStore causes drainWebhookDispatchers
// to return ErrWebhookConfigInvalid.
func TestDrainWebhookDispatchers_FailsWithoutSourceStore(t *testing.T) {
	t.Parallel()
	dc := newWebhookDispatchCell(whTestCellID, dispatchTestContractID, whTestSourceID)
	s := buildPhaseStateWithWebhookCells(t, dc)

	b := New(clockmock.New(whFixedNow))
	// webhookSourceStore is nil (not set via WithWebhookSourceStore).

	err := b.drainWebhookDispatchers(s, nil)

	require.Error(t, err, "missing source store must error")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr, "error must be *errcode.Error")
	assert.Equal(t, errcode.ErrWebhookConfigInvalid, ecErr.Code,
		"missing source store must yield ErrWebhookConfigInvalid")
	assert.Contains(t, ecErr.Message, "WithWebhookSourceStore",
		"error message must name the missing option")
}

// TestDrainWebhookDispatchers_FailsWhenSubscriberNil (F13) covers the phase6
// guard at phases_events.go checkNoEventConsumersWhenSubscriberNil: a cell that
// registers a webhook dispatcher but no Subscriber is configured must fail fast,
// not silently drop the dispatcher. Drives phase6StartEventRouter with s.sub ==
// nil (buildPhaseStateWithWebhookCells leaves it unset).
func TestDrainWebhookDispatchers_FailsWhenSubscriberNil(t *testing.T) {
	t.Parallel()
	dc := newWebhookDispatchCell(whTestCellID, dispatchTestContractID, whTestSourceID)
	s := buildPhaseStateWithWebhookCells(t, dc)

	b := New(clockmock.New(whFixedNow)) // no WithSubscriber → s.sub stays nil

	err := b.phase6StartEventRouter(context.Background(), s)

	require.Error(t, err, "a webhook dispatcher with no subscriber must fail fast")
	assert.Contains(t, err.Error(), "webhook dispatcher",
		"error must identify the webhook dispatcher as the unconsumed event consumer")
	assert.Contains(t, err.Error(), "no subscriber is configured",
		"error must tell the operator to add WithSubscriber")
}

// TestDrainWebhookDispatchers_FailsOnCellIDDrift verifies that a dispatcher
// whose spec.CellID differs from the snapshot owner causes a drift error.
func TestDrainWebhookDispatchers_FailsOnCellIDDrift(t *testing.T) {
	t.Parallel()
	dc := &webhookDispatchTestCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: whTestCellID, Type: "core"}),
		spec: kwh.DispatchSpec{
			ContractID: dispatchTestContractID,
			SourceID:   whTestSourceID,
			CellID:     whTestCellID,
		},
		selector: func(_ context.Context, _ []byte) (string, error) {
			return "http://example.test/hook", nil
		},
		overrideCellID: "wrongcell", // spec.CellID will be "wrongcell" != snapshot key "webhookcell"
	}
	s := buildPhaseStateWithWebhookCells(t, dc)

	b := New(clockmock.New(whFixedNow))
	b.webhookSourceStore = whTestStore(t)

	err := b.drainWebhookDispatchers(s, nil)

	require.Error(t, err, "CellID drift must error")
	assert.Contains(t, err.Error(), "drift", "drift error must mention 'drift'")
	assert.Contains(t, err.Error(), "wrongcell", "drift error must mention the mismatched CellID")
}

// TestDrainWebhookDispatchers_HappyPath_RegistersHandler verifies that a
// well-configured snapshot with one dispatcher and a seeded SourceStore
// successfully registers one handler on the event router (HandlerCount == 1).
func TestDrainWebhookDispatchers_HappyPath_RegistersHandler(t *testing.T) {
	t.Parallel()
	dc := newWebhookDispatchCell(whTestCellID, dispatchTestContractID, whTestSourceID)
	s := buildPhaseStateWithWebhookCells(t, dc)

	clk := clockmock.New(whFixedNow)
	b := New(clk, WithConsumerBase(newTestConsumerBase(t)))
	b.webhookSourceStore = whTestStore(t)

	evtRouter := newDispatchEvtRouter(t, b)

	err := b.drainWebhookDispatchers(s, evtRouter)

	require.NoError(t, err, "happy path must not error")
	assert.Equal(t, 1, evtRouter.HandlerCount(),
		"one dispatcher must register exactly one handler on the event router")
}
