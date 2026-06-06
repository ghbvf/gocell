package metrics_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

// newConfigEventEntry builds a valid outbox.Entry for the config-event
// middleware tests. The middleware only forwards the entry, so eventType and
// payload merely satisfy the sealed-constructor validation; the ID is the only
// field the tests pin.
func newConfigEventEntry(t *testing.T, id string) outbox.Entry {
	t.Helper()
	entry, err := outbox.NewEntry(clock.Real(), context.Background(),
		"event.config.entry-upserted.v1", []byte("{}"), outbox.WithID(id))
	require.NoError(t, err)
	return entry
}

func TestProviderConfigEventCollector_RejectsNilProvider(t *testing.T) {
	collector, err := obmetrics.NewProviderConfigEventCollector(nil)
	require.Error(t, err)
	assert.Nil(t, collector)
}

func TestProviderConfigEventCollector_NopProviderNoPanic(t *testing.T) {
	ctx := context.Background()
	collector, err := obmetrics.NewProviderConfigEventCollector(kernelmetrics.NopProvider{})
	require.NoError(t, err)

	collector.RecordEventProcess(ctx, "accesscore", "configreceive", obmetrics.ConfigEventProcessReasonAck)
	collector.RecordEventSettlement(ctx, "configcore", "configsubscribe", "requeue", outbox.SettlementResultCommitFailed)
}

func TestProviderConfigEventCollector_ReturnsRegistrationError(t *testing.T) {
	collector, err := obmetrics.NewProviderConfigEventCollector(failingCounterProvider{})
	require.Error(t, err)
	assert.Nil(t, collector)
	assert.Contains(t, err.Error(), "register config_event_process_total")
}

func TestProviderConfigEventCollector_EmitsExpectedMetricsAndLabels(t *testing.T) {
	ctx := context.Background()
	p := newSpyProvider()
	collector, err := obmetrics.NewProviderConfigEventCollector(p)
	require.NoError(t, err)

	collector.RecordEventProcess(ctx, "accesscore", "configreceive", obmetrics.ConfigEventProcessReasonStale)
	collector.RecordEventSettlement(ctx, "accesscore", "configreceive", "ack", outbox.SettlementResultSuccess)

	processOps := p.counterOps["config_event_process_total"]
	require.Len(t, processOps, 1)
	assert.Equal(t, kernelmetrics.Labels{
		"cell":   "accesscore",
		"slice":  "configreceive",
		"reason": "stale",
	}, processOps[0].labels)
	assert.Equal(t, 1.0, processOps[0].value)

	settlementOps := p.counterOps["config_event_settlement_total"]
	require.Len(t, settlementOps, 1)
	assert.Equal(t, kernelmetrics.Labels{
		"cell":        "accesscore",
		"slice":       "configreceive",
		"disposition": "ack",
		"result":      "success",
	}, settlementOps[0].labels)
	assert.Equal(t, 1.0, settlementOps[0].value)
}

func TestConfigEventMiddleware_RecordsProcessReasonFromSubscriptionOwner(t *testing.T) {
	collector := &recordingConfigEventCollector{}
	mw := obmetrics.ConfigEventMiddleware(collector)
	wrapped := mw(
		outbox.Subscription{Topic: "event.config.entry-upserted.v1", ConsumerGroup: "accesscore", CellID: "accesscore", SliceID: "configreceive"},
		func(ctx context.Context, _ outbox.Entry) outbox.HandleResult {
			obmetrics.RecordConfigEventProcess(ctx, collector, obmetrics.ConfigEventProcessReasonAck)
			return outbox.Ack()
		},
	)

	result := wrapped(context.Background(), newConfigEventEntry(t, "evt-1"))

	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	require.Equal(t, []configEventProcessRecord{{
		cell: "accesscore", slice: "configreceive", reason: obmetrics.ConfigEventProcessReasonAck,
	}}, collector.processRecords)
	// Note: SettlementObservers is on DeliveryOutcome (SubscriberHandler layer),
	// not on HandleResult (EntryHandler layer). ConfigEventMiddleware only injects
	// owner ctx; settlement observer is added by WrapConfigEventSubscriber.
}

// TestWrapConfigEventSubscriber_RecordsSettlementOnlyAfterNotification verifies
// that the settlement observer added by WrapConfigEventSubscriber records the
// metric only after NotifySettlement is called, not during handler execution.
func TestWrapConfigEventSubscriber_RecordsSettlementOnlyAfterNotification(t *testing.T) {
	collector := &recordingConfigEventCollector{}
	sub := outbox.Subscription{
		Topic: "event.config.entry-upserted.v1", ConsumerGroup: "accesscore",
		CellID: "accesscore", SliceID: "configreceive",
	}
	entry := newConfigEventEntry(t, "evt-1")

	// WrapConfigEventSubscriber wraps a SubscriberHandler and appends the
	// settlement observer to DeliveryOutcome.SettlementObservers.
	inner := func(context.Context, outbox.Entry) (outbox.DeliveryOutcome, outbox.Settlement) {
		return outbox.DeliveryOutcome{Disposition: outbox.DispositionRequeue}, nil
	}
	wrapped := obmetrics.WrapConfigEventSubscriber(collector, sub, inner)

	outcome, _ := wrapped(context.Background(), entry)
	assert.Empty(t, collector.settlementRecords)

	outbox.NotifySettlement(context.Background(), outcome, entry, outbox.DispositionRequeue, outbox.SettlementResultSuccess, nil)

	require.Equal(t, []configEventSettlementRecord{{
		cell: "accesscore", slice: "configreceive", disposition: "requeue", result: outbox.SettlementResultSuccess,
	}}, collector.settlementRecords)
}

func TestConfigEventMiddleware_SkipsSubscriptionsWithoutOwnerOrConfigTopic(t *testing.T) {
	collector := &recordingConfigEventCollector{}
	mw := obmetrics.ConfigEventMiddleware(collector)

	for _, sub := range []outbox.Subscription{
		{Topic: "event.config.entry-upserted.v1", ConsumerGroup: "accesscore", CellID: "accesscore"},
		{Topic: "event.audit.appended.v1", ConsumerGroup: "auditcore", CellID: "auditcore", SliceID: "auditappend"},
	} {
		entry := newConfigEventEntry(t, "evt-1")
		// EntryHandler layer (middleware): process reason must not be recorded.
		entryHandler := mw(sub, func(ctx context.Context, _ outbox.Entry) outbox.HandleResult {
			obmetrics.RecordConfigEventProcess(ctx, collector, obmetrics.ConfigEventProcessReasonAck)
			return outbox.Ack()
		})
		entryHandler(context.Background(), entry)

		// SubscriberHandler layer: settlement observer must not be appended for
		// non-qualifying subscriptions (missing owner or non-config topic).
		inner := func(context.Context, outbox.Entry) (outbox.DeliveryOutcome, outbox.Settlement) {
			return outbox.DeliveryOutcome{Disposition: outbox.DispositionAck}, nil
		}
		outcome, _ := obmetrics.WrapConfigEventSubscriber(collector, sub, inner)(context.Background(), entry)
		outbox.NotifySettlement(context.Background(), outcome,
			entry, outbox.DispositionAck, outbox.SettlementResultSuccess, nil)
	}

	assert.Empty(t, collector.processRecords)
	assert.Empty(t, collector.settlementRecords)
}

type recordingConfigEventCollector struct {
	processRecords    []configEventProcessRecord
	settlementRecords []configEventSettlementRecord
}

type configEventProcessRecord struct {
	cell   string
	slice  string
	reason obmetrics.ConfigEventProcessReason
}

type configEventSettlementRecord struct {
	cell        string
	slice       string
	disposition string
	result      outbox.SettlementResult
}

func (c *recordingConfigEventCollector) RecordEventProcess(
	_ context.Context, cellID, sliceID string, reason obmetrics.ConfigEventProcessReason,
) {
	c.processRecords = append(c.processRecords, configEventProcessRecord{cell: cellID, slice: sliceID, reason: reason})
}

func (c *recordingConfigEventCollector) RecordEventSettlement(
	_ context.Context, cellID, sliceID, disposition string, result outbox.SettlementResult,
) {
	c.settlementRecords = append(c.settlementRecords,
		configEventSettlementRecord{cell: cellID, slice: sliceID, disposition: disposition, result: result})
}

// ---------------------------------------------------------------------------
// ConfigEventOwnerValidator tests (Finding 2 — registration-time validation)
// ---------------------------------------------------------------------------

func TestConfigEventOwnerValidator(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		sub     outbox.Subscription
		wantErr bool
		errMsg  string
	}{
		{
			name:    "non-config topic passes",
			sub:     outbox.Subscription{Topic: "event.audit.appended.v1", ConsumerGroup: "auditcore", CellID: "auditcore", SliceID: "auditappend"},
			wantErr: false,
		},
		{
			name: "config topic with owner passes",
			sub: outbox.Subscription{
				Topic: "event.config.entry-upserted.v1", ConsumerGroup: "accesscore",
				CellID: "accesscore", SliceID: "configreceive",
			},
			wantErr: false,
		},
		{
			name:    "config topic missing CellID fails",
			sub:     outbox.Subscription{Topic: "event.config.entry-upserted.v1", ConsumerGroup: "accesscore", SliceID: "configreceive"},
			wantErr: true,
			errMsg:  "owner metadata",
		},
		{
			name:    "config topic missing SliceID fails",
			sub:     outbox.Subscription{Topic: "event.config.entry-upserted.v1", ConsumerGroup: "accesscore", CellID: "accesscore"},
			wantErr: true,
			errMsg:  "owner metadata",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := obmetrics.ConfigEventOwnerValidator(tc.sub)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestWrapConfigEventSubscriber_RetryExhaustedSettlement verifies that when
// NotifySettlement is called with SettlementResultRetryExhausted on a
// WrapConfigEventSubscriber-wrapped handler, the settlement observer records
// the correct result label.
func TestWrapConfigEventSubscriber_RetryExhaustedSettlement(t *testing.T) {
	t.Parallel()
	collector := &recordingConfigEventCollector{}
	sub := outbox.Subscription{
		Topic: "event.config.entry-upserted.v1", ConsumerGroup: "accesscore",
		CellID: "accesscore", SliceID: "configreceive",
	}
	entry := newConfigEventEntry(t, "evt-retry-exhausted")

	// ProcessReason is carried on DeliveryOutcome (set by ConsumerBase.Wrap when
	// retry budget is exhausted). Use it here to simulate the exhausted path.
	inner := func(context.Context, outbox.Entry) (outbox.DeliveryOutcome, outbox.Settlement) {
		return outbox.DeliveryOutcome{
			Disposition:   outbox.DispositionReject,
			ProcessReason: "retry_exhausted",
		}, nil
	}
	wrapped := obmetrics.WrapConfigEventSubscriber(collector, sub, inner)

	outcome, _ := wrapped(context.Background(), entry)
	outbox.NotifySettlement(context.Background(), outcome, entry, outbox.DispositionReject, outbox.SettlementResultRetryExhausted, nil)

	require.Equal(t, []configEventSettlementRecord{{
		cell: "accesscore", slice: "configreceive", disposition: "reject", result: outbox.SettlementResultRetryExhausted,
	}}, collector.settlementRecords)
}

type failingCounterProvider struct {
	kernelmetrics.NopProvider
}

func (failingCounterProvider) CounterVec(kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	return nil, errors.New("counter registration failed")
}
