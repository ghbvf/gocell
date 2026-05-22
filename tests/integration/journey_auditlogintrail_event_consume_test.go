//go:build integration

package integration

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/auditcore/auditcoretest"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// TestJAuditlogintrailEventConsume implements journeys/J-auditlogintrail.yaml
// passCriteria "session.created 事件被 auditcore 消费" — checkRef
// journey.J-auditlogintrail.event-consume. The verify runner resolves that
// ref to ^TestJAuditlogintrailEventConsume$ via verify.kebabToCamelCase and
// executes it under -tags=integration via `gocell verify journey
// --id=J-auditlogintrail`.
//
// J-auditlogintrail is lifecycle: active (lifted from experimental as part
// of this PR), so governance VERIFY-06
// (kernel/governance/rules_verify.go validateVERIFY06Journey) runs this
// test inside `gocell validate --strict`. The test MUST be Docker-free for
// that gate to stay green on machines without a container runtime, mirroring
// TestJSsologinSessionDb and the other C3 journey integration tests in this
// package.
//
// The criterion asserts that auditcore's auditappendsession slice consumes
// event.session.created.v1 and acks it (DispositionAck). Layer compromise:
// we drive the subscription seam (cell.RegistryRecorder.Snapshot().
// Subscriptions[event.session.created.v1].Handler) instead of going through
// broker delivery, because broker delivery is owned by adapters/rabbitmq's
// integration suite and is Docker-bound. The seam IS the contract for
// "auditcore consumes the event": the handler is the binding that
// SubscriberWithMiddleware forwards broker deliveries to in production.
//
// The full programmatic proof of accesscore -> outbox -> rabbitmq ->
// auditcore -> hash-chain commits in one transaction is owned by
// tests/integration/l2atomicity/ + cells/auditcore/slices/auditappendsession
// 's in-package conformance suite.
func TestJAuditlogintrailEventConsume(t *testing.T) {
	t.Parallel()
	handler, store, ctx := auditcoretest.BuildAuditcoreChain(t)
	entry := auditcoretest.NewSessionCreatedEntry("sess-j-auditlogintrail", "usr-j-auditlogintrail")
	result := handler(ctx, entry)
	require.Equalf(t, outbox.DispositionAck, result.Disposition,
		"auditcore.auditappendsession must Ack session.created; got disposition=%v error=%v",
		result.Disposition, result.Err)
	// Lock "consume = write": Ack alone does not prove the handler actually
	// persisted the event. The audit append path is consume + ledger.Append
	// inside the same RunInTx; without this assertion an emitter-only Ack
	// implementation would silently pass the criterion.
	tail, err := store.Tail(ctx)
	require.NoError(t, err, "store.Tail after Ack")
	require.GreaterOrEqualf(t, tail.SeqNo, int64(1),
		"audit ledger must have at least one entry after Ack; tail.SeqNo=%d", tail.SeqNo)
}
