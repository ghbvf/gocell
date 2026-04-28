package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/outbox"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSharedDepsValidateRequiresConfigEventCollector(t *testing.T) {
	deps := buildTestSharedDeps(t)
	deps.ConfigEventCollector = nil

	err := deps.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ConfigEventCollector")
}

func TestConfigEventConsumerMiddlewareTargetsCorebundleConfigConsumers(t *testing.T) {
	collector := &recordingCoreConfigEventCollector{}
	mw := configEventConsumerMiddleware(collector)

	for _, tc := range []struct {
		name string
		sub  outbox.Subscription
		want coreConfigEventRecord
	}{
		{
			name: "accesscore configreceive upsert",
			sub:  outbox.Subscription{Topic: "event.config.entry-upserted.v1", ConsumerGroup: "accesscore"},
			want: coreConfigEventRecord{cell: "accesscore", slice: "configreceive", outcome: obmetrics.ConfigEventOutcomeReject},
		},
		{
			name: "accesscore configreceive delete",
			sub:  outbox.Subscription{Topic: "event.config.entry-deleted.v1", ConsumerGroup: "accesscore"},
			want: coreConfigEventRecord{cell: "accesscore", slice: "configreceive", outcome: obmetrics.ConfigEventOutcomeReject},
		},
		{
			name: "configcore configsubscribe upsert",
			sub:  outbox.Subscription{Topic: "event.config.entry-upserted.v1", ConsumerGroup: "configcore"},
			want: coreConfigEventRecord{cell: "configcore", slice: "configsubscribe", outcome: obmetrics.ConfigEventOutcomeReject},
		},
		{
			name: "configcore configsubscribe delete",
			sub:  outbox.Subscription{Topic: "event.config.entry-deleted.v1", ConsumerGroup: "configcore"},
			want: coreConfigEventRecord{cell: "configcore", slice: "configsubscribe", outcome: obmetrics.ConfigEventOutcomeReject},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collector.records = nil
			wrapped := mw(tc.sub, func(context.Context, outbox.Entry) outbox.HandleResult {
				return outbox.HandleResult{Disposition: outbox.DispositionReject, Err: errors.New("retry exhausted")}
			})

			result := wrapped(context.Background(), outbox.Entry{ID: "evt-target"})

			assert.Equal(t, outbox.DispositionReject, result.Disposition)
			require.Equal(t, []coreConfigEventRecord{tc.want}, collector.records)
		})
	}
}

func TestConsumerMiddlewares_ConfigEventRejectRunsOutsideConsumerBase(t *testing.T) {
	collector := &recordingCoreConfigEventCollector{}
	shared := buildTestSharedDeps(t)
	shared.ConfigEventCollector = collector
	consumerBase, err := outbox.NewConsumerBase(idempotency.NewInMemClaimer(), outbox.ConsumerBaseConfig{
		RetryCount:     2,
		RetryBaseDelay: time.Millisecond,
	})
	require.NoError(t, err)

	attempts := 0
	wrapped := composeConsumerMiddleware(consumerMiddlewares(shared, consumerBase),
		outbox.Subscription{Topic: "event.config.entry-upserted.v1", ConsumerGroup: "accesscore"},
		func(context.Context, outbox.Entry) outbox.HandleResult {
			attempts++
			return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: errors.New("transient")}
		},
	)

	result := wrapped(context.Background(), outbox.Entry{ID: "evt-retry-exhausted"})

	assert.Equal(t, 2, attempts)
	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	require.Equal(t, []coreConfigEventRecord{{
		cell: "accesscore", slice: "configreceive", outcome: obmetrics.ConfigEventOutcomeReject,
	}}, collector.records)
}

func TestConsumerMiddlewares_PermanentErrorNotCountedAsReject(t *testing.T) {
	collector := &recordingCoreConfigEventCollector{}
	shared := buildTestSharedDeps(t)
	shared.ConfigEventCollector = collector
	consumerBase, err := outbox.NewConsumerBase(idempotency.NewInMemClaimer(), outbox.ConsumerBaseConfig{
		RetryCount:     2,
		RetryBaseDelay: time.Millisecond,
	})
	require.NoError(t, err)

	wrapped := composeConsumerMiddleware(consumerMiddlewares(shared, consumerBase),
		outbox.Subscription{Topic: "event.config.entry-upserted.v1", ConsumerGroup: "accesscore"},
		func(context.Context, outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{
				Disposition: outbox.DispositionReject,
				Err:         outbox.NewPermanentError(errors.New("bad payload")),
			}
		},
	)

	result := wrapped(context.Background(), outbox.Entry{ID: "evt-permanent"})

	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	assert.Empty(t, collector.records)
}

func composeConsumerMiddleware(
	mws []outbox.SubscriptionMiddleware,
	sub outbox.Subscription,
	handler outbox.EntryHandler,
) outbox.EntryHandler {
	wrapped := handler
	for i := len(mws) - 1; i >= 0; i-- {
		wrapped = mws[i](sub, wrapped)
	}
	return wrapped
}

type recordingCoreConfigEventCollector struct {
	records []coreConfigEventRecord
}

type coreConfigEventRecord struct {
	cell    string
	slice   string
	outcome obmetrics.ConfigEventOutcome
}

func (c *recordingCoreConfigEventCollector) RecordEventProcessed(cellID, sliceID string, outcome obmetrics.ConfigEventOutcome) {
	c.records = append(c.records, coreConfigEventRecord{cell: cellID, slice: sliceID, outcome: outcome})
}
