package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/assembly"
	"github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	runtimeoutbox "github.com/ghbvf/gocell/framework/runtime/outbox"
	"github.com/ghbvf/gocell/framework/runtime/outbox/outboxtest"
)

// testRelayConfig is the shared fast-poll RelayConfig used by the bootstrap
// relay lifecycle/fan-out tests. Extracted so newEventsTestRelay and
// newDrainingRelay do not duplicate the 13-field literal.
func testRelayConfig() runtimeoutbox.RelayConfig {
	return runtimeoutbox.RelayConfig{
		PollInterval:         testtime.FastPoll,
		ReclaimInterval:      testtime.D10ms,
		BatchSize:            10,
		MaxAttempts:          3,
		BaseRetryDelay:       testtime.D1ms,
		MaxRetryDelay:        testtime.D10ms,
		ClaimTTL:             testtime.D100ms,
		RetentionPeriod:      testtime.D1h,
		DeadRetentionPeriod:  testtime.D24h,
		CleanupWaitFloor:     testtime.FastPoll,
		PollFailureBudget:    3,
		ReclaimFailureBudget: 3,
		CleanupFailureBudget: 3,
	}
}

// newDrainingRelay builds a relay bound to the supplied store so a test can
// seed the store and observe that relay drain it. The DiscardPublisher always
// succeeds, so claimed entries transition to StatePublished.
func newDrainingRelay(store *outboxtest.FakeStore) *runtimeoutbox.Relay {
	return runtimeoutbox.NewRelay(clock.Real(), store, &outbox.DiscardPublisher{}, testRelayConfig())
}

// seedPendingEntry writes a single pending outbox entry into store.
func seedPendingEntry(t *testing.T, ctx context.Context, store *outboxtest.FakeStore, eventType string) {
	t.Helper()
	e, err := outbox.NewEntry(clock.Real(), ctx, eventType, []byte("{}"))
	require.NoError(t, err)
	require.NoError(t, store.Write(ctx, e))
}

// TestWithRelay_SameInstanceKey_Rebind_Panics verifies the keyed rebind guard:
// registering two relays under the SAME InfraInstanceKey is a programmer error
// (the second would overwrite the first while leaving its adapter in
// managedResources — a double-managed relay). It panics through the
// panic-taxonomy funnel, identical to the pre-fan-out single-relay guard but
// now scoped per instance key (#2152 PR-1, RELAY-SOLE-HOLDER-01 keyed rewrite).
func TestWithRelay_SameInstanceKey_Rebind_Panics(t *testing.T) {
	t.Parallel()

	r1 := newEventsTestRelay()
	r2 := newEventsTestRelay()

	assert.Panics(t, func() {
		_ = New(
			clock.Real(),
			WithRelay(DefaultInstanceKey(), r1),
			WithRelay(DefaultInstanceKey(), r2),
		)
	}, "WithRelay called twice with the same instance key must panic via panicregister.Approved + errcode.Assertion")
}

// TestWithRelay_DistinctInstanceKeys_BothRegistered verifies the core fan-out
// seam: two relays under DISTINCT instance keys both register — each stored
// under its key and each wrapped in its own relayAdapter in managedResources —
// and both expand into the LIFO teardown pipeline. This is the N>1 relay
// behavior that lifts the old single-relay invariant.
func TestWithRelay_DistinctInstanceKeys_BothRegistered(t *testing.T) {
	t.Parallel()

	r1 := newEventsTestRelay()
	r2 := newEventsTestRelay()
	k1 := NewInfraInstanceKey("poola")
	k2 := NewInfraInstanceKey("poolb")

	b := New(
		clock.Real(),
		WithRelay(k1, r1),
		WithRelay(k2, r2),
	)

	require.Same(t, r1, b.relaysByInstance[k1], "relay r1 must be stored under key poola")
	require.Same(t, r2, b.relaysByInstance[k2], "relay r2 must be stored under key poolb")
	require.Len(t, b.relaysByInstance, 2, "two distinct instance keys must yield two relay entries")

	idx1, idx2, adapters := -1, -1, 0
	for i, mr := range b.managedResources {
		if ad, ok := mr.(*relayAdapter); ok && (ad.relay == r1 || ad.relay == r2) {
			adapters++
			if ad.relay == r1 {
				idx1 = i
			} else {
				idx2 = i
			}
		}
	}
	require.Equal(t, 2, adapters, "each distinct-key relay must get its own relayAdapter in managedResources")

	// LIFO teardown: the later-registered instance (k2) must close FIRST. The adapters
	// are appended in registration order (k1 before k2); expandManagedResources preserves
	// that order into managedResourceTeardowns, and Run() iterates them in reverse (the
	// LIFO reversal itself is covered by TestRunState_Rollback_ExecutesTeardownsLIFO). So
	// k1-before-k2 registration order is exactly what makes k2 tear down first.
	require.Less(t, idx1, idx2, "k1 relay adapter must be registered before k2 (so LIFO teardown closes k2 first)")

	// Both relays must flow through the managed-resource teardown pipeline, and
	// each non-default instance's relay must expose INSTANCE-SCOPED probe names on
	// the readyz health-checker set (the per-instance readyz wire contract, F3
	// #2338) — proving the namespacing reaches bootstrap registration, not just
	// the relayAdapter helper.
	require.NoError(t, b.expandManagedResources())
	require.GreaterOrEqual(t, len(b.managedResourceTeardowns), 2,
		"both relays must register a LIFO teardown")

	probeNames := make(map[string]bool, len(b.healthCheckers))
	for _, hc := range b.healthCheckers {
		probeNames[string(hc.name)] = true
	}
	for _, op := range []string{"outbox_relay_poll", "outbox_relay_reclaim", "outbox_relay_cleanup"} {
		require.True(t, probeNames[op+"_poola"], "non-default instance poola must expose suffixed probe %q", op+"_poola")
		require.True(t, probeNames[op+"_poolb"], "non-default instance poolb must expose suffixed probe %q", op+"_poolb")
		require.False(t, probeNames[op], "non-default instances must NOT expose the bare probe %q (it is reserved for DefaultInstanceKey)", op)
	}

	ctx := context.Background()
	for _, td := range b.managedResourceTeardowns {
		require.NoError(t, td.fn(ctx), "teardown %q must not fail", td.name)
	}
}

// TestWithRelay_TwoDistinctInstances_EachDrainsOwnStore is the end-to-end
// "真 2-池" proof: two relays bound to two DISTINCT outbox stores are registered
// under two distinct instance keys, the full Bootstrap is run, and EACH relay
// drains ITS OWN seeded store to completion. This坐实s "distinct DSN → 各自
// relay → 端到端可跑" at the bootstrap seam (#2152 PR-1; broker fan-out stays
// PR-2 — the shared in-proc bus is irrelevant here since each relay holds its
// own publisher). Deterministic via FakeStore.WaitFor (no timing coupling).
func TestWithRelay_TwoDistinctInstances_EachDrainsOwnStore(t *testing.T) {
	ctx := context.Background()
	clk := clock.Real()

	storeA := outboxtest.NewFakeStore()
	storeB := outboxtest.NewFakeStore()
	seedPendingEntry(t, ctx, storeA, "test.fanout.a")
	seedPendingEntry(t, ctx, storeB, "test.fanout.b")

	asm := assembly.New(clk, assembly.Config{ID: "test-relay-fanout", DurabilityMode: outbox.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	healthLn := newLocalListener(t)
	b := New(
		clk,
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}, WithListenerNet(newLocalListener(t))),
		WithListener(cell.InternalListener, "127.0.0.1:0", []auth.ListenerAuth{auth.AuthNone{}}, WithListenerNet(newLocalListener(t))),
		WithListener(cell.HealthListener, healthLn.Addr().String(), []auth.ListenerAuth{auth.AuthNone{}}, WithListenerNet(healthLn)),
		WithShutdownTimeout(testtime.D2s),
		WithRelay(NewInfraInstanceKey("poola"), newDrainingRelay(storeA)),
		WithRelay(NewInfraInstanceKey("poolb"), newDrainingRelay(storeB)),
	)

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- b.Run(runCtx) }()
	waitForHealthy(t, healthLn.Addr().String())

	waitCtx, waitCancel := context.WithTimeout(ctx, testtime.EventuallyDefault)
	defer waitCancel()
	published := func(rows []outboxtest.FakeRow) bool {
		return len(rows) == 1 && rows[0].Status == outbox.StatePublished
	}
	require.NoError(t, storeA.WaitFor(waitCtx, published), "relay under key poola must drain its own store")
	require.NoError(t, storeB.WaitFor(waitCtx, published), "relay under key poolb must drain its own store")

	cancel()
	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("bootstrap did not shut down in time")
	}
}
