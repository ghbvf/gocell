package main

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/kernel/idempotency"
)

// fakeDistributedClaimer is a test fake that reports ClaimerKindDistributed.
// Used by bundle_test.go to drive the distributed-claimer reporting path in
// adapterInfoForSharedDeps without a real Redis connection.
type fakeDistributedClaimer struct{}

func (fakeDistributedClaimer) Claim(
	context.Context, string, time.Duration, time.Duration,
) (idempotency.ClaimState, idempotency.Receipt, error) {
	return idempotency.ClaimDone, idempotency.NonAcquiredReceipt(), nil
}

func (fakeDistributedClaimer) Kind() idempotency.ClaimerKind {
	return idempotency.ClaimerKindDistributed
}
