package certlifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/reconcile"
	"github.com/ghbvf/gocell/framework/kernel/reconcile/reconciletest"
	cl "github.com/ghbvf/gocell/framework/runtime/certlifecycle"
	cs "github.com/ghbvf/gocell/framework/runtime/certsigning"
)

const (
	testMaxLookahead = 30 * 24 * time.Hour
	testSignTTL      = 24 * time.Hour
	pollTimeout      = 3 * time.Second
	pollTick         = 5 * time.Millisecond
	// testReconcilerID is the lease key the test Loop acquires under; the
	// multi-replica test pre-acquires the same key to simulate a handoff. Must
	// satisfy validateReconcilerID (lowercase [a-z0-9_], leading [a-z_]).
	testReconcilerID = "certlifecycle_test"
)

// dueWindow returns a validity window for which now is past the 70–90% jitter
// instant (so the cert is due) regardless of the jitter fraction.
func dueWindow(now time.Time) (notBefore, notAfter time.Time) {
	return now.Add(-95 * time.Hour), now.Add(5 * time.Hour)
}

// notDueWindow returns a window whose 70–90% instant is comfortably in the
// future (so the cert is not yet due) even within the scan cutoff.
func notDueWindow(now time.Time) (notBefore, notAfter time.Time) {
	return now.Add(-1 * time.Hour), now.Add(99 * time.Hour)
}

// newReconciler builds a Reconciler with the given fakes and default policy.
func newReconciler(t *testing.T, repo cl.DeviceCertRepository, signer *fakeSigner, authz *fakeAuthorizer) *cl.Reconciler {
	t.Helper()
	rec, err := cl.NewReconciler(clock.Real(), repo, signer, authz,
		cl.Policy{MaxLookahead: testMaxLookahead, SignTTL: testSignTTL}, nil)
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	return rec
}

// driveLoop builds a fenced+leader Loop around rec with a fresh single-holder
// elector, starts it, submits one resync-all pulse, and returns a stop func.
func driveLoop(t *testing.T, rec *cl.Reconciler, repo reconcile.FencedRepository) (stop func()) {
	t.Helper()
	leader := reconciletest.NewFakeLeaseBackend(clock.Real()).Elector("holder-1")
	return driveLoopWithLeader(t, rec, repo, leader)
}

// driveLoopWithLeader is driveLoop with a caller-supplied LeaderElector, so a
// multi-replica handoff scenario can control the lease epoch. The Loop is the
// only minter of the FencedWriter the Reconciler writes through, so all behavior
// is exercised end-to-end here.
func driveLoopWithLeader(t *testing.T, rec *cl.Reconciler, repo reconcile.FencedRepository, leader reconcile.LeaderElector) (stop func()) {
	t.Helper()
	trigger, submit := reconciletest.NewFakeTrigger()
	loop, err := reconcile.New(rec, reconcile.SingleTenant()).
		WithTrigger(trigger).
		WithLeader(leader).
		WithFencedRepo(repo).
		WithReconcilerID(testReconcilerID).
		WithoutDefaultRequeue().
		Build()
	if err != nil {
		t.Fatalf("build loop: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Start is non-blocking: it returns once the worker pool is confirmed running
	// (or an error from preStartValidate). The loop then runs in background
	// goroutines until Stop / ctx cancel.
	if err := loop.Start(ctx); err != nil {
		cancel()
		t.Fatalf("loop.Start: %v", err)
	}
	submit(reconcile.Request{})
	return func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), pollTimeout)
		defer stopCancel()
		if err := loop.Stop(stopCtx); err != nil {
			t.Errorf("loop.Stop: %v", err)
		}
		cancel()
	}
}

// waitUntil polls cond until true or fails after pollTimeout.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(pollTimeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(pollTick)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func TestReconcileHappyPathRenews(t *testing.T) {
	t.Parallel()
	now := time.Now()
	nb, na := dueWindow(now)
	repo := newFakeRepo(activeCandidate(t, "device-1", nb, na))
	signer := newFakeSigner(now, now.Add(testSignTTL))
	authz := &fakeAuthorizer{grant: grantAll(t, testSignTTL)}
	rec := newReconciler(t, repo, signer, authz)

	stop := driveLoop(t, rec, repo)
	defer stop()
	waitUntil(t, "one renewal mutation", func() bool { return len(repo.mutations()) == 1 })

	mut := firstMutation(t, repo)
	if mut.TargetEpoch != 2 {
		t.Errorf("TargetEpoch = %d, want 2 (cand.Epoch+1)", mut.TargetEpoch)
	}
	if mut.NewState != cl.StateActive() {
		t.Errorf("NewState = %q, want active", mut.NewState.String())
	}
	if mut.TenantID != testTenant {
		t.Errorf("mutation TenantID = %q, want %q (from row, not ctx)", mut.TenantID, testTenant)
	}
	if mut.IssuerID != testIssuer {
		t.Errorf("mutation IssuerID = %q, want %q", mut.IssuerID, testIssuer)
	}
	if mut.Serial == "" {
		t.Error("mutation Serial empty — must carry the issued cert serial")
	}
	// The signing request must carry the row tenant (not an ambient ctx tenant).
	req, ok := signer.lastRequest()
	if !ok {
		t.Fatal("signer received no request")
	}
	if got := req.Request().Scope().Tenant().String(); got != testTenant {
		t.Errorf("signed request scope tenant = %q, want %q (sourced from row)", got, testTenant)
	}
}

func TestReconcileSigningFailureDoesNotDamageCert(t *testing.T) {
	t.Parallel()
	now := time.Now()
	nb, na := dueWindow(now)
	repo := newFakeRepo(activeCandidate(t, "device-1", nb, na))
	signer := newFakeSigner(now, now.Add(testSignTTL))
	signer.err = errFake
	authz := &fakeAuthorizer{grant: grantAll(t, testSignTTL)}
	rec := newReconciler(t, repo, signer, authz)

	stop := driveLoop(t, rec, repo)
	defer stop()
	// Sign is attempted; assert no persisted effect ever (existing cert untouched).
	waitUntil(t, "signer attempted", func() bool { return signer.callCount() >= 1 })
	if got := len(repo.mutations()); got != 0 {
		t.Errorf("recorded %d mutations after signing failure, want 0 (existing cert untouched)", got)
	}
}

func TestReconcileFailClosedDenyDoesNotSign(t *testing.T) {
	t.Parallel()
	now := time.Now()
	nb, na := dueWindow(now)
	repo := newFakeRepo(activeCandidate(t, "device-1", nb, na))
	signer := newFakeSigner(now, now.Add(testSignTTL))
	authz := &fakeAuthorizer{} // zero SignConstraints == not granted (deny)
	rec := newReconciler(t, repo, signer, authz)

	stop := driveLoop(t, rec, repo)
	defer stop()
	waitUntil(t, "authorizer consulted", func() bool { return authz.callCount() >= 1 })
	if got := signer.callCount(); got != 0 {
		t.Errorf("signer called %d times under fail-closed deny, want 0", got)
	}
	if got := len(repo.mutations()); got != 0 {
		t.Errorf("recorded %d mutations under deny, want 0", got)
	}
}

func TestReconcileAuthorizeErrorDoesNotSign(t *testing.T) {
	t.Parallel()
	now := time.Now()
	nb, na := dueWindow(now)
	repo := newFakeRepo(activeCandidate(t, "device-1", nb, na))
	signer := newFakeSigner(now, now.Add(testSignTTL))
	authz := &fakeAuthorizer{err: errFake}
	rec := newReconciler(t, repo, signer, authz)

	stop := driveLoop(t, rec, repo)
	defer stop()
	waitUntil(t, "authorizer consulted", func() bool { return authz.callCount() >= 1 })
	if got := signer.callCount(); got != 0 {
		t.Errorf("signer called %d times after authorize error, want 0", got)
	}
}

func TestReconcileConstraintViolationDoesNotSign(t *testing.T) {
	t.Parallel()
	now := time.Now()
	nb, na := dueWindow(now)
	cand := activeCandidate(t, "device-1", nb, na)
	cand.DNSNames = []string{"device-1.example"} // requested SAN outside the empty grant allowance
	repo := newFakeRepo(cand)
	signer := newFakeSigner(now, now.Add(testSignTTL))
	authz := &fakeAuthorizer{grant: grantAll(t, testSignTTL)} // allows NO SANs
	rec := newReconciler(t, repo, signer, authz)

	stop := driveLoop(t, rec, repo)
	defer stop()
	waitUntil(t, "authorizer consulted", func() bool { return authz.callCount() >= 1 })
	if got := signer.callCount(); got != 0 {
		t.Errorf("signer called %d times on SAN constraint violation, want 0", got)
	}
	if got := len(repo.mutations()); got != 0 {
		t.Errorf("recorded %d mutations on constraint violation, want 0", got)
	}
}

func TestReconcileTTLViolationDoesNotSign(t *testing.T) {
	t.Parallel()
	now := time.Now()
	nb, na := dueWindow(now)
	repo := newFakeRepo(activeCandidate(t, "device-1", nb, na))
	signer := newFakeSigner(now, now.Add(testSignTTL))
	authz := &fakeAuthorizer{grant: grantAll(t, testSignTTL-time.Hour)} // maxTTL < SignTTL
	rec := newReconciler(t, repo, signer, authz)

	stop := driveLoop(t, rec, repo)
	defer stop()
	waitUntil(t, "authorizer consulted", func() bool { return authz.callCount() >= 1 })
	if got := signer.callCount(); got != 0 {
		t.Errorf("signer called %d times when SignTTL exceeds grant MaxTTL, want 0", got)
	}
}

func TestReconcileNotDueSkips(t *testing.T) {
	t.Parallel()
	now := time.Now()
	nb, na := notDueWindow(now)
	repo := newFakeRepo(activeCandidate(t, "device-1", nb, na))
	signer := newFakeSigner(now, now.Add(testSignTTL))
	authz := &fakeAuthorizer{grant: grantAll(t, testSignTTL)}
	rec := newReconciler(t, repo, signer, authz)

	stop := driveLoop(t, rec, repo)
	defer stop()
	waitUntil(t, "sweep ran", func() bool { return repo.listCount() >= 1 })
	if got := authz.callCount(); got != 0 {
		t.Errorf("authorizer called %d times for not-due cert, want 0", got)
	}
	if got := len(repo.mutations()); got != 0 {
		t.Errorf("recorded %d mutations for not-due cert, want 0", got)
	}
}

func TestReconcileNonRenewableStateSkips(t *testing.T) {
	t.Parallel()
	now := time.Now()
	nb, na := dueWindow(now)
	cand := activeCandidate(t, "device-1", nb, na)
	cand.State = cl.StateRevoked() // operator terminal — never renewed
	repo := newFakeRepo(cand)
	signer := newFakeSigner(now, now.Add(testSignTTL))
	authz := &fakeAuthorizer{grant: grantAll(t, testSignTTL)}
	rec := newReconciler(t, repo, signer, authz)

	stop := driveLoop(t, rec, repo)
	defer stop()
	waitUntil(t, "sweep ran", func() bool { return repo.listCount() >= 1 })
	if got := authz.callCount(); got != 0 {
		t.Errorf("authorizer called %d times for revoked cert, want 0 (observe-and-skip)", got)
	}
	if got := signer.callCount(); got != 0 {
		t.Errorf("signer called %d times for revoked cert, want 0", got)
	}
}

func TestReconcileExpiredCertReSignsToRecover(t *testing.T) {
	t.Parallel()
	now := time.Now()
	// Already past notAfter, but active with a valid stored CSR → re-sign to recover.
	nb, na := now.Add(-100*time.Hour), now.Add(-1*time.Hour)
	repo := newFakeRepo(activeCandidate(t, "device-1", nb, na))
	signer := newFakeSigner(now, now.Add(testSignTTL))
	authz := &fakeAuthorizer{grant: grantAll(t, testSignTTL)}
	rec := newReconciler(t, repo, signer, authz)

	stop := driveLoop(t, rec, repo)
	defer stop()
	waitUntil(t, "expired cert recovered", func() bool { return len(repo.mutations()) == 1 })
	if got := firstMutation(t, repo).TargetEpoch; got != 2 {
		t.Errorf("recovered cert TargetEpoch = %d, want 2", got)
	}
}

func TestReconcileStaleFencedWriteSkipsNoDuplicate(t *testing.T) {
	t.Parallel()
	now := time.Now()
	nb, na := dueWindow(now)
	repo := newFakeRepo(activeCandidate(t, "device-1", nb, na))
	// Seed a higher lease epoch for the entity so the Loop's epoch-1 write is
	// stale-rejected by the monotonic CAS (zombie-leader scenario).
	if _, err := repo.ApplyFenced(context.Background(), "device-1", 99, "seed-high-epoch"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	signer := newFakeSigner(now, now.Add(testSignTTL))
	authz := &fakeAuthorizer{grant: grantAll(t, testSignTTL)}
	rec := newReconciler(t, repo, signer, authz)

	stop := driveLoop(t, rec, repo)
	defer stop()
	waitUntil(t, "signer attempted", func() bool { return signer.callCount() >= 1 })
	// The stale write is rejected → no IssuedMutation recorded (only the seed).
	if got := len(repo.mutations()); got != 0 {
		t.Errorf("recorded %d IssuedMutations after stale rejection, want 0", got)
	}
}

func TestReconcileInvalidRowTenantSkips(t *testing.T) {
	t.Parallel()
	now := time.Now()
	nb, na := dueWindow(now)
	cand := activeCandidate(t, "device-1", nb, na)
	cand.TenantID = "not-a-canonical-uuid" // malformed row → identity build fails
	repo := newFakeRepo(cand)
	signer := newFakeSigner(now, now.Add(testSignTTL))
	authz := &fakeAuthorizer{grant: grantAll(t, testSignTTL)}
	rec := newReconciler(t, repo, signer, authz)

	stop := driveLoop(t, rec, repo)
	defer stop()
	waitUntil(t, "sweep ran", func() bool { return repo.listCount() >= 1 })
	if got := authz.callCount(); got != 0 {
		t.Errorf("authorizer called %d times for malformed-tenant row, want 0 (skip before authorize)", got)
	}
	if got := signer.callCount(); got != 0 {
		t.Errorf("signer called %d times for malformed-tenant row, want 0", got)
	}
	if got := len(repo.mutations()); got != 0 {
		t.Errorf("recorded %d mutations for malformed-tenant row, want 0", got)
	}
}

func TestReconcileCorruptStoredCSRSkips(t *testing.T) {
	t.Parallel()
	now := time.Now()
	nb, na := dueWindow(now)
	cand := activeCandidate(t, "device-1", nb, na)
	cand.CSRDER = []byte("not-a-valid-csr") // unparseable stored CSR → request build fails
	repo := newFakeRepo(cand)
	signer := newFakeSigner(now, now.Add(testSignTTL))
	authz := &fakeAuthorizer{grant: grantAll(t, testSignTTL)}
	rec := newReconciler(t, repo, signer, authz)

	stop := driveLoop(t, rec, repo)
	defer stop()
	waitUntil(t, "authorizer consulted", func() bool { return authz.callCount() >= 1 })
	if got := signer.callCount(); got != 0 {
		t.Errorf("signer called %d times with corrupt stored CSR, want 0", got)
	}
	if got := len(repo.mutations()); got != 0 {
		t.Errorf("recorded %d mutations with corrupt stored CSR, want 0", got)
	}
}

func TestNewReconcilerValidatesDeps(t *testing.T) {
	t.Parallel()
	now := time.Now()
	okSigner := newFakeSigner(now, now.Add(testSignTTL))
	okAuthz := &fakeAuthorizer{}
	okRepo := newFakeRepo()
	okPolicy := cl.Policy{MaxLookahead: testMaxLookahead, SignTTL: testSignTTL}

	cases := []struct {
		name   string
		repo   cl.DeviceCertRepository
		signer cs.Signer
		authz  cs.Authorizer
		policy cl.Policy
	}{
		{"nil repo", nil, okSigner, okAuthz, okPolicy},
		{"nil signer", okRepo, nil, okAuthz, okPolicy},
		{"nil authorizer", okRepo, okSigner, nil, okPolicy},
		{"zero MaxLookahead", okRepo, okSigner, okAuthz, cl.Policy{SignTTL: testSignTTL}},
		{"zero SignTTL", okRepo, okSigner, okAuthz, cl.Policy{MaxLookahead: testMaxLookahead}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := cl.NewReconciler(clock.Real(), tc.repo, tc.signer, tc.authz, tc.policy, nil); err == nil {
				t.Errorf("NewReconciler(%s) = nil error, want validation failure", tc.name)
			}
		})
	}
}

func TestReconcileNoFencedWriterFailsClosed(t *testing.T) {
	t.Parallel()
	now := time.Now()
	nb, na := dueWindow(now)
	repo := newFakeRepo(activeCandidate(t, "device-1", nb, na))
	signer := newFakeSigner(now, now.Add(testSignTTL))
	authz := &fakeAuthorizer{grant: grantAll(t, testSignTTL)}
	rec := newReconciler(t, repo, signer, authz)

	// Direct call with a plain ctx (no Loop-injected writer) must fail closed and
	// never sign.
	_, err := rec.Reconcile(context.Background(), reconcile.Request{})
	if err == nil {
		t.Fatal("Reconcile with no fenced writer returned nil error, want fail-closed")
	}
	if signer.callCount() != 0 {
		t.Errorf("signer called %d times without a fenced writer, want 0", signer.callCount())
	}
}
