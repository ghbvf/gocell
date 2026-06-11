package reconcile

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/tenant"
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
	tenant, tenantOK := ctxkeys.TenantIDFrom(ctx)
	session, sessionOK := ctxkeys.SessionIDFrom(ctx)
	assert.Equal(t, "system", actor, "reconcile ctx actor must be the system sentinel")
	assert.Equal(t, "system", subject, "reconcile ctx subject must be the system sentinel")
	// tenant/session are positively written present-but-empty, NOT left absent: the
	// install asserts a tenantless identity rather than relying on a missing key, so
	// deleting the WithTenantID/WithSessionID("") write would surface here (ok=false)
	// — not only via the overwrite test.
	assert.True(t, tenantOK, "tenant key must be present (installed-empty, not absent)")
	assert.Equal(t, "", tenant, "reconcile ctx tenant must be cleared (→ _notenant dedup namespace)")
	assert.True(t, sessionOK, "session key must be present (installed-empty, not absent)")
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

// TestInstallSystemProducerIdentity_YieldsTenantlessPrincipal ties the install to
// the outbox producer principal it ultimately stamps: outbox.ContextPrincipal (what
// NewEntry injects at emit time) reads the installed ctx as actor/subject="system",
// tenant/session="" — so the emitted entry's Claimer key lands under "_notenant".
// This pins the dedup-key consequence at the kernel layer (the cert-renewal Loop
// test proves the same end-to-end). The scoped-ctx variant —
// TestInstallSystemProducerIdentity_SuppressesTenantScopeFallback — proves the
// outcome holds even when a tenant.WithScope IS present (#1824 F1).
func TestInstallSystemProducerIdentity_YieldsTenantlessPrincipal(t *testing.T) {
	p := outbox.ContextPrincipal(installSystemProducerIdentity(context.Background()))
	assert.Equal(t, "system", string(p.ActorID))
	assert.Equal(t, "system", string(p.SubjectID))
	assert.Equal(t, "", string(p.TenantID), "tenantless principal → _notenant dedup namespace")
	assert.Equal(t, "", string(p.SessionID))
}

// TestInstallSystemProducerIdentity_SuppressesTenantScopeFallback is the F1
// (#1824 review) regression: the install must make the tenantless system identity
// a CODE FACT even when the reconcile ctx ALSO carries a tenant.WithScope. The PR
// promises "tenantless → _notenant dedup namespace" as a construction guarantee
// robust against ambient leaks; tenant scope is exactly such an ambient channel.
// Without the fix, outbox.ContextPrincipal falls back to the scope tenant whenever
// the ctxkeys tenant is present-but-empty (the `ok && id != ""` branch is false),
// so the install's cleared tenant does NOT win and the emitted entry's principal
// tenant becomes the scope tenant — breaking the invariant.
func TestInstallSystemProducerIdentity_SuppressesTenantScopeFallback(t *testing.T) {
	scoped := tenant.WithScope(context.Background(),
		tenant.TenantID("11111111-1111-1111-1111-111111111111"))
	p := outbox.ContextPrincipal(installSystemProducerIdentity(scoped))
	assert.Equal(t, "system", string(p.ActorID))
	assert.Equal(t, "system", string(p.SubjectID))
	assert.Equal(t, "", string(p.TenantID),
		"installed system identity must suppress the ambient tenant.WithScope fallback (→ _notenant)")
	assert.Equal(t, "", string(p.SessionID))
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
	// Bare &Loop{} is the package-internal test idiom (matches loop_test.go); the
	// public construction API is reconcile.New(r).With*().Build() — a populated
	// &reconcile.Loop{} literal does not compile outside this package.
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
