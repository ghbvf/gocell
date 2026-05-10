package metrics_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

func TestProviderConfigEventCollector_RejectsNilProvider(t *testing.T) {
	collector, err := obmetrics.NewProviderConfigEventCollector(nil)
	require.Error(t, err)
	assert.Nil(t, collector)
}

func TestProviderConfigEventCollector_NopProviderNoPanic(t *testing.T) {
	collector, err := obmetrics.NewProviderConfigEventCollector(kernelmetrics.NopProvider{})
	require.NoError(t, err)

	collector.RecordEventProcess("accesscore", "configreceive", obmetrics.ConfigEventProcessReasonAck)
	collector.RecordEventSettlement("configcore", "configsubscribe", "requeue", outbox.SettlementResultCommitFailed)
}

func TestProviderConfigEventCollector_ReturnsRegistrationError(t *testing.T) {
	collector, err := obmetrics.NewProviderConfigEventCollector(failingCounterProvider{})
	require.Error(t, err)
	assert.Nil(t, collector)
	assert.Contains(t, err.Error(), "register config_event_process_total")
}

func TestProviderConfigEventCollector_EmitsExpectedMetricsAndLabels(t *testing.T) {
	p := newSpyProvider()
	collector, err := obmetrics.NewProviderConfigEventCollector(p)
	require.NoError(t, err)

	collector.RecordEventProcess("accesscore", "configreceive", obmetrics.ConfigEventProcessReasonStale)
	collector.RecordEventSettlement("accesscore", "configreceive", "ack", outbox.SettlementResultSuccess)

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

	result := wrapped(context.Background(), outbox.Entry{ID: "evt-1"})

	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	require.Equal(t, []configEventProcessRecord{{
		cell: "accesscore", slice: "configreceive", reason: obmetrics.ConfigEventProcessReasonAck,
	}}, collector.processRecords)
	require.Len(t, result.SettlementObservers, 1)
}

func TestConfigEventMiddleware_RecordsSettlementOnlyAfterNotification(t *testing.T) {
	collector := &recordingConfigEventCollector{}
	mw := obmetrics.ConfigEventMiddleware(collector)
	entry := outbox.Entry{ID: "evt-1"}
	wrapped := mw(
		outbox.Subscription{Topic: "event.config.entry-upserted.v1", ConsumerGroup: "accesscore", CellID: "accesscore", SliceID: "configreceive"},
		func(context.Context, outbox.Entry) outbox.HandleResult {
			return outbox.Requeue(nil)
		},
	)

	result := wrapped(context.Background(), entry)
	assert.Empty(t, collector.settlementRecords)

	outbox.NotifySettlement(context.Background(), result, entry, outbox.DispositionRequeue, outbox.SettlementResultSuccess, nil)

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
		wrapped := mw(sub, func(ctx context.Context, _ outbox.Entry) outbox.HandleResult {
			obmetrics.RecordConfigEventProcess(ctx, collector, obmetrics.ConfigEventProcessReasonAck)
			return outbox.Ack()
		})
		result := wrapped(context.Background(), outbox.Entry{ID: "evt-1"})
		outbox.NotifySettlement(context.Background(), result,
			outbox.Entry{ID: "evt-1"}, outbox.DispositionAck, outbox.SettlementResultSuccess, nil)
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

func (c *recordingConfigEventCollector) RecordEventProcess(cellID, sliceID string, reason obmetrics.ConfigEventProcessReason) {
	c.processRecords = append(c.processRecords, configEventProcessRecord{cell: cellID, slice: sliceID, reason: reason})
}

func (c *recordingConfigEventCollector) RecordEventSettlement(cellID, sliceID, disposition string, result outbox.SettlementResult) {
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

// TestConfigEventMiddleware_RetryExhaustedSettlement verifies that when
// NotifySettlement is called with SettlementResultRetryExhausted, the
// settlement observer records the correct result label.
func TestConfigEventMiddleware_RetryExhaustedSettlement(t *testing.T) {
	t.Parallel()
	collector := &recordingConfigEventCollector{}
	mw := obmetrics.ConfigEventMiddleware(collector)
	entry := outbox.Entry{ID: "evt-retry-exhausted"}
	wrapped := mw(
		outbox.Subscription{Topic: "event.config.entry-upserted.v1", ConsumerGroup: "accesscore", CellID: "accesscore", SliceID: "configreceive"},
		func(context.Context, outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionReject, ProcessReason: "retry_exhausted"}
		},
	)

	result := wrapped(context.Background(), entry)
	outbox.NotifySettlement(context.Background(), result, entry, outbox.DispositionReject, outbox.SettlementResultRetryExhausted, nil)

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
