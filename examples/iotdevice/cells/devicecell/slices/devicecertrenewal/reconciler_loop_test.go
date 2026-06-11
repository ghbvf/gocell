package devicecertrenewal

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecert"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/kernel/reconcile"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
	rtcommand "github.com/ghbvf/gocell/runtime/command"
)

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
	store := devicecert.NewStore()
	_, err := store.Issue(ctx, "dev-near", certTestBase.Add(24*time.Hour))
	require.NoError(t, err)

	rec := outboxtest.NewRecorder()
	r := newTestReconciler(t, clockmock.New(certTestBase), store, rec)

	reqCh := make(chan reconcile.Request)
	loop, err := reconcile.New(r).
		WithReconcilerID("certrenewal_systemid_test").
		WithTrigger(reconcile.ChannelTrigger(reqCh)).
		Build()
	require.NoError(t, err)

	require.NoError(t, loop.Start(ctx))
	t.Cleanup(func() {
		sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, loop.Stop(sc), "loop should stop cleanly within the cleanup budget")
	})

	reqCh <- reconcile.Request{} // resync-all sweep (empty EntityID)

	// testwait.External (with a reason literal) is the sanctioned polling funnel —
	// no bare require.Eventually. The Recorder emits synchronously inside Reconcile,
	// so one Loop dispatch yields exactly one entry.
	testwait.External(t, "cert-renewal-loop-emit",
		func() bool { return len(rec.Entries()) == 1 },
		3*time.Second, 5*time.Millisecond,
		"the Loop-driven reconcile must emit exactly one rotate-cert command")

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
