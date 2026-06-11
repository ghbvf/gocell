package devicecertrenewal

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/kernel/reconcile"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
	rtcommand "github.com/ghbvf/gocell/runtime/command"
)

// stopLoop stops a reconcile.Loop and fails the test if it does not stop within
// the budget. Must be called BEFORE goleak.VerifyNone so all loop goroutines are
// joined before the leak check runs.
func stopLoop(t *testing.T, loop *reconcile.Loop) {
	t.Helper()
	sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, loop.Stop(sc), "loop must stop cleanly within the budget")
}

// TestReconciler_EmitsSystemTenantlessPrincipalViaLoop is the #1821 / #1808 F5
// end-to-end proof ON THE ACTUAL PRODUCER. When the cert-renewal reconciler runs
// through a real reconcile.Loop — as it does in production via
// buildCertRenewalSweeper — the framework installs a system producer identity at
// the reconcile chokepoint, so the emitted rotate-cert command carries
// actor/subject="system", tenant/session="", and its Claimer dedup key lands under
// the "_notenant" namespace. (A direct r.Reconcile call does NOT install the
// identity — that is the Loop's job — so this test drives the Loop.)
func TestReconciler_EmitsSystemTenantlessPrincipalViaLoop(t *testing.T) {
	ctx := context.Background()
	repo := mem.NewDeviceRepository()
	seedCert(t, ctx, repo, "dev-near", certTestBase.Add(24*time.Hour))

	rec := outboxtest.NewRecorder()
	r := newTestReconciler(t, clockmock.New(certTestBase), repo, rec)

	reqCh := make(chan reconcile.Request)
	loop, err := reconcile.New(r).
		WithReconcilerID("certrenewal_systemid_test").
		WithTrigger(reconcile.ChannelTrigger(reqCh)).
		Build()
	require.NoError(t, err)

	require.NoError(t, loop.Start(ctx))

	reqCh <- reconcile.Request{} // resync-all sweep (empty EntityID)

	// testwait.External (with a reason literal) is the sanctioned polling funnel —
	// no bare require.Eventually. The Recorder emits synchronously inside Reconcile,
	// so one Loop dispatch yields exactly one entry.
	testwait.External(t, "cert-renewal-loop-emit",
		func() bool { return len(rec.Entries()) == 1 },
		3*time.Second, 5*time.Millisecond,
		"the Loop-driven reconcile must emit exactly one rotate-cert command")

	// Stop the loop before goleak runs — loop goroutines must be joined first.
	stopLoop(t, loop)
	defer goleak.VerifyNone(t)

	got := rec.Entries()[0]
	p := got.Principal()
	assert.Equal(t, "system", string(p.ActorID),
		"the reconcile framework installs a system actor at the chokepoint")
	assert.Equal(t, "system", string(p.SubjectID), "system subject")
	assert.Equal(t, "", string(p.TenantID),
		"the system producer identity is tenantless → _notenant dedup namespace")
	assert.Equal(t, "", string(p.SessionID), "the system producer identity carries no session")

	key, ok := rtcommand.ClaimKeyFromEntry(got)
	require.True(t, ok, "a command entry with subject+command_id yields a Claimer key")
	assert.True(t, strings.HasPrefix(key, "_notenant\x00"),
		"the dedup key tenant dimension must be the _notenant sentinel; got %q", key)
}

// TestReconciler_LoopDrainsMultiBatchBacklog is the loop-level proof that
// RequeueAfter-driven batch continuation actually drains a multi-batch backlog.
//
// Setup: 4 near-expiry certs, BatchSize=2 → first Reconcile emits 2 and returns
// RequeueAfter=BatchRequeue; the Loop re-dispatches; second Reconcile emits the
// remaining 2 and returns RequeueAfter=0.
//
// Asserts that all 4 certs are drained (all 4 entries appear in the recorder)
// within a bounded wait — proving the RequeueAfter continuation works end-to-end
// through the real reconcile.Loop.
func TestReconciler_LoopDrainsMultiBatchBacklog(t *testing.T) {
	ctx := context.Background()
	repo := mem.NewDeviceRepository()

	const totalCerts = 4
	const batchSize = 2

	drainPolicy := Policy{
		Threshold:     certRenewalTestThreshold,
		RetryInterval: 24 * time.Hour,
		BatchSize:     batchSize,
		BatchRequeue:  5 * time.Millisecond, // small so the drain is fast in tests
	}

	// Seed 4 near-expiry certs with distinct expiry times.
	for i := 0; i < totalCerts; i++ {
		id := "dev-drain-" + string(rune('a'+i))
		require.NoError(t, repo.Create(ctx, &domain.Device{
			ID: id, Name: id, Status: "online", LastSeen: certTestBase,
			CertEpoch:     domain.DefaultCertEpoch,
			CertExpiresAt: certTestBase.Add(time.Duration(i+1) * time.Hour),
		}))
	}

	rec := outboxtest.NewRecorder()
	fc := clockmock.New(certTestBase)
	r, err := NewReconciler(fc, repo, rec.CellEmitter(), outbox.DemoCellTxManager(), drainPolicy, nil)
	require.NoError(t, err)

	reqCh := make(chan reconcile.Request, 1)
	loop, err := reconcile.New(r).
		WithReconcilerID("certrenewal_drain_test").
		WithTrigger(reconcile.ChannelTrigger(reqCh)).
		WithoutDefaultRequeue(). // the RequeueAfter continuation is the sole re-dispatch mechanism
		Build()
	require.NoError(t, err)

	require.NoError(t, loop.Start(ctx))

	// Send the initial resync-all pulse; the RequeueAfter continuation drives the rest.
	reqCh <- reconcile.Request{}

	// All 4 certs must be drained within a short bounded wait (the BatchRequeue of
	// 5ms ensures the Loop re-dispatches the continuation quickly).
	testwait.External(t, "cert-renewal-drain",
		func() bool { return len(rec.Entries()) == totalCerts },
		3*time.Second, 10*time.Millisecond,
		"all %d near-expiry certs must be drained across the RequeueAfter batches", totalCerts)

	// Stop the loop before goleak runs — loop goroutines must be joined first.
	stopLoop(t, loop)
	defer goleak.VerifyNone(t)

	assert.Len(t, rec.Entries(), totalCerts, "exactly all certs drained — no duplicates, no misses")
}
