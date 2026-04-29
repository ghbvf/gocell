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

func TestConfigEventConsumerMiddlewareUsesSubscriptionOwnerMetadata(t *testing.T) {
	collector := &recordingCoreConfigEventCollector{}
	mw := configEventConsumerMiddleware(collector)
	sub := outbox.Subscription{
		Topic:         "event.config.entry-upserted.v1",
		ConsumerGroup: "accesscore",
		CellID:        "accesscore",
		SliceID:       "configreceive",
	}
	entry := outbox.Entry{ID: "evt-target"}
	wrapped := mw(sub, func(context.Context, outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	result := wrapped(context.Background(), entry)
	outbox.NotifySettlement(context.Background(), result, entry, outbox.DispositionAck, outbox.SettlementResultSuccess, nil)

	require.Equal(t, []coreConfigEventSettlementRecord{{
		cell: "accesscore", slice: "configreceive", disposition: "ack", result: outbox.SettlementResultSuccess,
	}}, collector.settlementRecords)
}

func TestConfigEventConsumerMiddlewareSkipsSubscriptionsWithoutOwnerMetadata(t *testing.T) {
	collector := &recordingCoreConfigEventCollector{}
	mw := configEventConsumerMiddleware(collector)
	entry := outbox.Entry{ID: "evt-target"}
	wrapped := mw(
		outbox.Subscription{Topic: "event.config.entry-upserted.v1", ConsumerGroup: "accesscore"},
		func(context.Context, outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		},
	)

	result := wrapped(context.Background(), entry)
	outbox.NotifySettlement(context.Background(), result, entry, outbox.DispositionAck, outbox.SettlementResultSuccess, nil)

	assert.Empty(t, collector.settlementRecords)
}

func TestConsumerMiddlewares_ConfigEventSettlementRunsOutsideConsumerBase(t *testing.T) {
	collector := &recordingCoreConfigEventCollector{}
	shared := buildTestSharedDeps(t)
	shared.ConfigEventCollector = collector
	consumerBase, err := outbox.NewConsumerBase(idempotency.NewInMemClaimer(), outbox.ConsumerBaseConfig{
		RetryCount:     2,
		RetryBaseDelay: time.Millisecond,
	})
	require.NoError(t, err)

	attempts := 0
	entry := outbox.Entry{ID: "evt-retry-exhausted"}
	wrapped := composeConsumerMiddleware(consumerMiddlewares(shared, consumerBase),
		outbox.Subscription{Topic: "event.config.entry-upserted.v1", ConsumerGroup: "accesscore", CellID: "accesscore", SliceID: "configreceive"},
		func(context.Context, outbox.Entry) outbox.HandleResult {
			attempts++
			return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: errors.New("transient")}
		},
	)

	result := wrapped(context.Background(), entry)
	outbox.NotifySettlement(context.Background(), result, entry, result.Disposition, outbox.SettlementResultSuccess, nil)

	assert.Equal(t, 2, attempts)
	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	require.Equal(t, []coreConfigEventSettlementRecord{{
		cell: "accesscore", slice: "configreceive", disposition: "reject", result: outbox.SettlementResultSuccess,
	}}, collector.settlementRecords)
}

func TestConsumerMiddlewares_PermanentErrorRecordedAsFinalRejectSettlement(t *testing.T) {
	collector := &recordingCoreConfigEventCollector{}
	shared := buildTestSharedDeps(t)
	shared.ConfigEventCollector = collector
	consumerBase, err := outbox.NewConsumerBase(idempotency.NewInMemClaimer(), outbox.ConsumerBaseConfig{
		RetryCount:     2,
		RetryBaseDelay: time.Millisecond,
	})
	require.NoError(t, err)

	entry := outbox.Entry{ID: "evt-permanent"}
	wrapped := composeConsumerMiddleware(consumerMiddlewares(shared, consumerBase),
		outbox.Subscription{Topic: "event.config.entry-upserted.v1", ConsumerGroup: "accesscore", CellID: "accesscore", SliceID: "configreceive"},
		func(context.Context, outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{
				Disposition: outbox.DispositionReject,
				Err:         outbox.NewPermanentError(errors.New("bad payload")),
			}
		},
	)

	result := wrapped(context.Background(), entry)
	outbox.NotifySettlement(context.Background(), result, entry, result.Disposition, outbox.SettlementResultSuccess, nil)

	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	require.Equal(t, []coreConfigEventSettlementRecord{{
		cell: "accesscore", slice: "configreceive", disposition: "reject", result: outbox.SettlementResultSuccess,
	}}, collector.settlementRecords)
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
	processRecords    []coreConfigEventProcessRecord
	settlementRecords []coreConfigEventSettlementRecord
}

type coreConfigEventProcessRecord struct {
	cell   string
	slice  string
	reason obmetrics.ConfigEventProcessReason
}

type coreConfigEventSettlementRecord struct {
	cell        string
	slice       string
	disposition string
	result      outbox.SettlementResult
}

func (c *recordingCoreConfigEventCollector) RecordEventProcess(cellID, sliceID string, reason obmetrics.ConfigEventProcessReason) {
	c.processRecords = append(c.processRecords, coreConfigEventProcessRecord{cell: cellID, slice: sliceID, reason: reason})
}

func (c *recordingCoreConfigEventCollector) RecordEventSettlement(cellID, sliceID, disposition string, result outbox.SettlementResult) {
	c.settlementRecords = append(c.settlementRecords, coreConfigEventSettlementRecord{cell: cellID, slice: sliceID, disposition: disposition, result: result})
}
