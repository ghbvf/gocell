package metrics_test

import (
	"context"
	"errors"
	"testing"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderConfigEventCollector_RejectsNilProvider(t *testing.T) {
	collector, err := obmetrics.NewProviderConfigEventCollector(nil)
	require.Error(t, err)
	assert.Nil(t, collector)
}

func TestProviderConfigEventCollector_NopProviderNoPanic(t *testing.T) {
	collector, err := obmetrics.NewProviderConfigEventCollector(kernelmetrics.NopProvider{})
	require.NoError(t, err)

	collector.RecordEventProcessed("accesscore", "configreceive", obmetrics.ConfigEventOutcomeAck)
	collector.RecordEventProcessed("configcore", "configsubscribe", obmetrics.ConfigEventOutcomePermanentError)
}

func TestProviderConfigEventCollector_ReturnsRegistrationError(t *testing.T) {
	collector, err := obmetrics.NewProviderConfigEventCollector(failingCounterProvider{})
	require.Error(t, err)
	assert.Nil(t, collector)
	assert.Contains(t, err.Error(), "register config_event_processed_total")
}

func TestProviderConfigEventCollector_EmitsExpectedMetricAndLabels(t *testing.T) {
	p := newSpyProvider()
	collector, err := obmetrics.NewProviderConfigEventCollector(p)
	require.NoError(t, err)

	collector.RecordEventProcessed("accesscore", "configreceive", obmetrics.ConfigEventOutcomeStale)

	ops := p.counterOps["config_event_processed_total"]
	require.Len(t, ops, 1)
	assert.Equal(t, kernelmetrics.Labels{
		"cell":    "accesscore",
		"slice":   "configreceive",
		"outcome": "stale",
	}, ops[0].labels)
	assert.Equal(t, 1.0, ops[0].value)
}

func TestConfigEventRejectMiddleware_RecordsFinalNonPermanentRejectForTargets(t *testing.T) {
	collector := &recordingConfigEventCollector{}
	mw := obmetrics.ConfigEventRejectMiddleware(collector,
		obmetrics.ConfigEventSubscription{
			CellID:        "accesscore",
			SliceID:       "configreceive",
			Topic:         "event.config.entry-upserted.v1",
			ConsumerGroup: "accesscore",
		},
		obmetrics.ConfigEventSubscription{
			CellID:        "configcore",
			SliceID:       "configsubscribe",
			Topic:         "event.config.entry-deleted.v1",
			ConsumerGroup: "configcore",
		},
	)

	for _, sub := range []outbox.Subscription{
		{Topic: "event.config.entry-upserted.v1", ConsumerGroup: "accesscore"},
		{Topic: "event.config.entry-deleted.v1", ConsumerGroup: "configcore"},
	} {
		wrapped := mw(sub, func(context.Context, outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionReject, Err: errors.New("retry exhausted")}
		})
		result := wrapped(context.Background(), outbox.Entry{ID: "evt-1"})
		assert.Equal(t, outbox.DispositionReject, result.Disposition)
	}

	require.Equal(t, []configEventRecord{
		{cell: "accesscore", slice: "configreceive", outcome: obmetrics.ConfigEventOutcomeReject},
		{cell: "configcore", slice: "configsubscribe", outcome: obmetrics.ConfigEventOutcomeReject},
	}, collector.records)
}

func TestConfigEventRejectMiddleware_SkipsPermanentRejectAndNonTargets(t *testing.T) {
	collector := &recordingConfigEventCollector{}
	mw := obmetrics.ConfigEventRejectMiddleware(collector, obmetrics.ConfigEventSubscription{
		CellID:        "accesscore",
		SliceID:       "configreceive",
		Topic:         "event.config.entry-upserted.v1",
		ConsumerGroup: "accesscore",
	})

	target := outbox.Subscription{Topic: "event.config.entry-upserted.v1", ConsumerGroup: "accesscore"}
	permanent := mw(target, func(context.Context, outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{
			Disposition: outbox.DispositionReject,
			Err:         outbox.NewPermanentError(errors.New("bad payload")),
		}
	})
	result := permanent(context.Background(), outbox.Entry{ID: "evt-permanent"})
	assert.Equal(t, outbox.DispositionReject, result.Disposition)

	other := mw(outbox.Subscription{Topic: "event.config.entry-upserted.v1", ConsumerGroup: "auditcore"}, func(context.Context, outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionReject, Err: errors.New("retry exhausted")}
	})
	result = other(context.Background(), outbox.Entry{ID: "evt-other"})
	assert.Equal(t, outbox.DispositionReject, result.Disposition)

	assert.Empty(t, collector.records)
}

type recordingConfigEventCollector struct {
	records []configEventRecord
}

type configEventRecord struct {
	cell    string
	slice   string
	outcome obmetrics.ConfigEventOutcome
}

func (c *recordingConfigEventCollector) RecordEventProcessed(cellID, sliceID string, outcome obmetrics.ConfigEventOutcome) {
	c.records = append(c.records, configEventRecord{cell: cellID, slice: sliceID, outcome: outcome})
}

type failingCounterProvider struct {
	kernelmetrics.NopProvider
}

func (failingCounterProvider) CounterVec(kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	return nil, errors.New("counter registration failed")
}
