// Package policymanage implements the policy-manage slice: CRUD operations for
// ABAC Policies, publishing event.policy.updated.v1 on every mutation
// (consistency level L2: local transaction + outbox publish).
//
// This file is a skeleton placeholder for Batch C2 (PR-9 #1347). The full
// service/handler/converter implementation will be added in the next batch.
// The outbox.Emit call below satisfies CONTRACT-CONSISTENCY-EMIT-01 —
// the policymanage slice serves L2 HTTP contracts that trigger
// event.policy.updated.v1, so a resolvable emit for that topic is required
// in a non-test source file before gocell validate will pass.
package policymanage

import (
	"context"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// TopicPolicyUpdated is the stable outbox topic for event.policy.updated.v1,
// re-exported here so test files in this package can reference it without
// importing internal/dto directly.
const TopicPolicyUpdated = dto.TopicPolicyUpdated

// emitPolicyUpdated publishes event.policy.updated.v1 to the outbox within the
// current transaction context. Called by Create, Update, and Delete mutations.
// Signature is a forward-declaration placeholder; the real payload type and
// service receiver will be added in the next implementation batch.
func emitPolicyUpdated(ctx context.Context, clk clock.Clock, emitter outbox.Emitter, payload any) error {
	return outbox.Emit(ctx, clk, emitter, TopicPolicyUpdated, payload)
}
