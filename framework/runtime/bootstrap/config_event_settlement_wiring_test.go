package bootstrap

// config_event_settlement_wiring_test.go — faithful wiring test for the
// config-event settlement observer (#1684 / codex F3).
//
// The corebundle unit tests (cmd/corebundle/config_event_metrics_wiring_test.go)
// install obmetrics.WrapConfigEventSubscriber by hand, so they would stay green
// even if production stopped wiring it. This test instead drives the REAL
// production assembly path — phase6StartEventRouter → buildEventRouter →
// eventrouter.WithSubscriberWrapper(WrapConfigEventSubscriber) — over a real
// in-mem eventbus, so a regression that drops the wrapper (or stops honoring
// WithConfigEventCollector) makes the settlement record disappear and this test
// red.
//
// It also exercises the retry-exhausted settlement classification end-to-end:
// the requeue-forever handler exhausts ConsumerBase's budget, ConsumerBase
// returns a terminal Reject tagged ProcessReason=retry_exhausted, and the
// eventbus settle loop must classify it (via outbox.RejectSettlementResult) as
// SettlementResultRetryExhausted rather than Success.

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/assembly"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cellvocab"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/contractspec"
	"github.com/ghbvf/gocell/framework/kernel/idempotency"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
	"github.com/ghbvf/gocell/framework/runtime/eventbus"
	"github.com/ghbvf/gocell/framework/runtime/http/health"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
)

// configSettlementRecord captures one RecordEventSettlement call.
type configSettlementRecord struct {
	cell        string
	slice       string
	disposition string
	result      outbox.SettlementResult
}

// recordingConfigEventCollector is a thread-safe obmetrics.ConfigEventCollector
// that records settlement observations for assertions. The settlement observer
// fires on the eventbus delivery goroutine, hence the mutex.
type recordingConfigEventCollector struct {
	mu          sync.Mutex
	settlements []configSettlementRecord
}

func (c *recordingConfigEventCollector) RecordEventProcess(
	_ context.Context, _, _ string, _ obmetrics.ConfigEventProcessReason,
) {
}

func (c *recordingConfigEventCollector) RecordEventSettlement(
	_ context.Context, cellID, sliceID, disposition string, result outbox.SettlementResult,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settlements = append(c.settlements, configSettlementRecord{
		cell: cellID, slice: sliceID, disposition: disposition, result: result,
	})
}

func (c *recordingConfigEventCollector) last() (configSettlementRecord, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.settlements) == 0 {
		return configSettlementRecord{}, false
	}
	return c.settlements[len(c.settlements)-1], true
}

// configEventStubCell subscribes to a config-event topic with owner metadata
// (CellID + SliceID) and a requeue-forever handler so ConsumerBase exhausts its
// retry budget and emits a terminal Reject tagged retry_exhausted.
type configEventStubCell struct {
	*cell.BaseCell
	spec contractspec.ContractSpec
}

func newConfigEventStubCell(topic string) *configEventStubCell {
	return &configEventStubCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: "accesscore", Type: "core"}),
		spec: contractspec.ContractSpec{
			ID:        topic,
			Kind:      cellvocab.ContractEvent,
			Transport: "inmem",
			Topic:     topic,
		},
	}
}

func (c *configEventStubCell) Init(ctx context.Context, reg cell.Registrar) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	requeueForever := outbox.EntryHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.Requeue(errors.New("transient"))
	})
	return reg.Subscribe(c.spec, requeueForever, c.ID(), c.ID(),
		cell.WithSubscriptionSliceID("configreceive"))
}

var _ cell.Cell = (*configEventStubCell)(nil)

// TestPhase6_ConfigEventSettlementWrapper_RecordsRetryExhausted proves the
// production config-event settlement observer is wired through buildEventRouter
// and that a retry-exhausted reject is recorded as RetryExhausted (not Success).
func TestPhase6_ConfigEventSettlementWrapper_RecordsRetryExhausted(t *testing.T) {
	t.Parallel()

	const topic = "event.config.entry-upserted.v1"
	bus := eventbus.New(clock.Real())
	collector := &recordingConfigEventCollector{}

	asm := assembly.New(clock.Real(), assembly.Config{ID: "phase6-cfgsettle-test", DurabilityMode: outbox.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newConfigEventStubCell(topic)))
	require.NoError(t, asm.Start(context.Background()))

	cb, err := outbox.NewConsumerBase(
		idempotency.NewInMemClaimer(clock.Real()),
		outbox.ConsumerBaseConfig{RetryCount: 1, RetryBaseDelay: time.Millisecond},
		clock.Real(),
	)
	require.NoError(t, err)

	b := New(
		clock.Real(),
		WithAssembly(asm),
		WithPublisher(bus),
		WithSubscriber(bus),
		WithConsumerBase(cb),
		WithConfigEventCollector(collector),
		WithSubscriptionValidator(obmetrics.ConfigEventOwnerValidator),
	)
	b.healthAggregator = newEventsTestAggregator() // phase0 normally sets this; test bypasses phase0.

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	s.sub = bus
	s.hh = health.New(asm, newEventsTestAggregator(), clock.Real()) // phase5 normally populates this.

	require.NoError(t, b.phase6StartEventRouter(runCtx, s),
		"phase6 must start the config-event subscription")

	// Publish one config event. requeue-forever handler → ConsumerBase exhausts →
	// terminal Reject(retry_exhausted) → eventbus settle loop classifies it via
	// outbox.RejectSettlementResult → the production-wired settlement observer
	// records reject/retry_exhausted.
	pub, err := outbox.NewEntry(clock.Real(), context.Background(), "config.entry-upserted", []byte("{}"),
		outbox.WithID("cfg-evt-1"), outbox.WithTopic(topic))
	require.NoError(t, err)
	env, err := outbox.MarshalEnvelope(pub)
	require.NoError(t, err)
	require.NoError(t, bus.Publish(context.Background(), topic, env))

	testwait.External(t, "config-event-settlement-observer-retry-exhausted", func() bool {
		rec, ok := collector.last()
		return ok && rec.disposition == "reject" && rec.result == outbox.SettlementResultRetryExhausted
	}, testtime.D2s, testtime.D10ms,
		"config-event settlement observer (wired via buildEventRouter→WithSubscriberWrapper) "+
			"must record reject/retry_exhausted")

	rec, _ := collector.last()
	assert.Equal(t, "accesscore", rec.cell, "settlement must carry the subscription owner cell")
	assert.Equal(t, "configreceive", rec.slice, "settlement must carry the subscription owner slice")

	// Release the eventrouter goroutine before the test exits (goleak TestMain).
	for _, v := range slices.Backward(s.teardowns) {
		_ = v.fn(context.Background())
	}
}
