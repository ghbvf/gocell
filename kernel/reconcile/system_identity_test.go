package reconcile

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// assertSystemProducerIdentity asserts ctx carries the system producer identity
// the Loop installs at the reconcile chokepoint: actor and subject are the
// "system" sentinel, tenant and session are cleared. This is the exact ctxkeys
// set outbox.ContextPrincipal reads at NewEntry time, so an emitted entry's
// Principal() resolves to actor/subject=system, tenant/session="" (dedup key
// tenant dimension → "_notenant").
func assertSystemProducerIdentity(t *testing.T, ctx context.Context) {
	t.Helper()
	actor, _ := ctxkeys.ActorIDFrom(ctx)
	subject, _ := ctxkeys.SubjectIDFrom(ctx)
	tenant, _ := ctxkeys.TenantIDFrom(ctx)
	session, _ := ctxkeys.SessionIDFrom(ctx)
	assert.Equal(t, "system", actor, "reconcile ctx actor must be the system sentinel")
	assert.Equal(t, "system", subject, "reconcile ctx subject must be the system sentinel")
	assert.Equal(t, "", tenant, "reconcile ctx tenant must be cleared (→ _notenant dedup namespace)")
	assert.Equal(t, "", session, "reconcile ctx session must be cleared")
}

// TestInstallSystemProducerIdentity_OnEmptyCtx asserts the installer stamps the
// system sentinel on a principal-free ctx (the normal lifecycle case).
func TestInstallSystemProducerIdentity_OnEmptyCtx(t *testing.T) {
	ctx := installSystemProducerIdentity(context.Background())
	assertSystemProducerIdentity(t, ctx)
}

// TestInstallSystemProducerIdentity_Overwrites asserts the installer is an
// overwrite: a pre-populated ambient principal is replaced wholesale with the
// system identity. This is the unit-level proof of the leak-defense the Loop
// chokepoint provides.
func TestInstallSystemProducerIdentity_Overwrites(t *testing.T) {
	ctx := ctxkeys.WithActorID(context.Background(), "user-x")
	ctx = ctxkeys.WithSubjectID(ctx, "user-x")
	ctx = ctxkeys.WithTenantID(ctx, "tenant-y")
	ctx = ctxkeys.WithSessionID(ctx, "sess-z")
	ctx = installSystemProducerIdentity(ctx)
	assertSystemProducerIdentity(t, ctx)
}

// TestLoop_InstallsSystemProducerIdentity is the framework-guarantee proof
// (#1821 / #1808 F5): every reconcile invocation runs under a positively
// installed system producer identity, so any emit a reconciler performs carries
// a tenantless system principal by construction — not by relying on an empty
// ambient lifecycle ctx. The reconciler captures the ctx the Loop hands it; the
// install happens at the Loop.process chokepoint, ahead of Reconcile.
func TestLoop_InstallsSystemProducerIdentity(t *testing.T) {
	var gotCtx context.Context
	called := make(chan struct{})
	rec := funcReconciler(func(ctx context.Context, _ Request) (Result, error) {
		gotCtx = ctx
		close(called)
		// Requeue far out so the entity does not re-fire mid-test.
		return Result{RequeueAfter: testtime.D1h}, nil
	})
	src := make(chan Request)
	l := &Loop{reconcilerID: "rc", reconciler: rec, source: src, interval: testtime.D1h}

	ownerCtx, ownerCancel := startCtxs(t)
	defer ownerCancel()
	require.NoError(t, l.Start(ownerCtx))

	src <- Request{EntityID: "e1"}
	testwait.Deterministic(t, called, "reconcile-called")
	// close(called) happens-before this receive, and gotCtx was assigned before
	// the close — safe to read without a data race.
	assertSystemProducerIdentity(t, gotCtx)

	sc, cancel := stopCtx(t)
	defer cancel()
	require.NoError(t, l.Stop(sc))
}

// TestLoop_SystemProducerIdentityOverridesAmbient proves the install is an
// OVERWRITE: even when the owner (lifecycle) ctx carries an ambient principal,
// the reconciler still observes the system identity — closing the issue's
// "producer inherits outer ctx → dedup key changes" leak as a code fact.
func TestLoop_SystemProducerIdentityOverridesAmbient(t *testing.T) {
	var gotCtx context.Context
	called := make(chan struct{})
	rec := funcReconciler(func(ctx context.Context, _ Request) (Result, error) {
		gotCtx = ctx
		close(called)
		return Result{RequeueAfter: testtime.D1h}, nil
	})
	src := make(chan Request)
	l := &Loop{reconcilerID: "rc", reconciler: rec, source: src, interval: testtime.D1h}

	// Seed an ambient authenticated principal on the owner ctx. Test files are
	// not scanned by CTXKEYS-PRINCIPAL-WRITE-CALLER-01, so writing ctxkeys here
	// to simulate a leaked request identity is legitimate.
	base := ctxkeys.WithActorID(context.Background(), "user-x")
	base = ctxkeys.WithSubjectID(base, "user-x")
	base = ctxkeys.WithTenantID(base, "tenant-y")
	base = ctxkeys.WithSessionID(base, "sess-z")
	ownerCtx, ownerCancel := context.WithCancel(base)
	defer ownerCancel()
	require.NoError(t, l.Start(ownerCtx))

	src <- Request{EntityID: "e1"}
	testwait.Deterministic(t, called, "reconcile-called")
	assertSystemProducerIdentity(t, gotCtx)

	sc, cancel := stopCtx(t)
	defer cancel()
	require.NoError(t, l.Stop(sc))
}
