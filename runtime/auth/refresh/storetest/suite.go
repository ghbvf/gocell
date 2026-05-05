// Package storetest provides a reusable contract test suite for refresh.Store
// implementations. Backends (memstore, postgres) each run RunContractSuite
// to prove they honor the same append-only + single-sentinel semantics.
//
// Test identifiers T1-T18 map to Store invariants: T1-T2 Issue, T3 Rotate
// happy path, T4 grace window, T5 reuse-after-grace, T6-T8 fail-closed
// rejection paths, T9 RevokeSession cascade, T10 concurrent Rotate CAS,
// T11 ExpiresAt calculation, T12 errcode sentinel category, T13 GC cleanup,
// T14 concurrent goroutine race model, T15 reuse-after-grace cascade,
// T16 grace-inside-interval, T17 parse-failure uniformity, T18 RevokeUser,
// T19-T20 Peek preflight/rejection, T21 Peek does not consume grace budget.
package storetest

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

// FakeClock is a type alias for [clockmock.FakeClock]; storetest historically
// owned its own implementation but the canonical one is now in
// kernel/clock/clockmock. The alias is retained so existing test wiring keeps
// using `storetest.FakeClock` through this package's Factory return type.
type FakeClock = clockmock.FakeClock

// NewFakeClock constructs a FakeClock anchored at the supplied time. Delegates
// to [clockmock.New].
func NewFakeClock(t time.Time) *FakeClock {
	return clockmock.New(t)
}

// Factory constructs a fresh Store and its deterministic clock. Backends with
// per-test setup (e.g. PG schema reset) do it inside Factory.
type Factory func(t *testing.T, policy refresh.Policy) (store refresh.Store, clock *FakeClock)

// storeMaxAge7d is a typical 7-day MaxAge used across contract tests.
const storeMaxAge7d = 7 * 24 * time.Hour

// storeMaxAge48h is a 48-hour MaxAge used in ExpiresAt calculation tests.
const storeMaxAge48h = 48 * time.Hour

// defaultPolicy is the policy used by tests unless they need specific values.
// MaxIdle and GraceMaxReuses use the package defaults (30 days / 3 re-uses);
// tests complete in milliseconds so neither limit is reached in normal runs.
var defaultPolicy = refresh.Policy{
	ReuseInterval:  testtime.D2s,
	MaxAge:         storeMaxAge7d,
	MaxIdle:        refresh.DefaultMaxIdle,
	GraceMaxReuses: refresh.DefaultGraceMaxReuses,
}

const (
	t15TargetSubject = "user-A"
	t15OtherSubject  = "user-B"
	t18TargetSubject = "user-18A"
	t18OtherSubject  = "user-18B"
	t20Subject       = "user-20"
)

// mustIssue asserts Issue succeeds and returns (wire, tok).
func mustIssue(t *testing.T, store refresh.Store, sessionID, subjectID string) (string, *refresh.Token) {
	t.Helper()
	wire, tok, err := store.Issue(context.Background(), sessionID, subjectID)
	require.NoError(t, err, "Issue(%q,%q)", sessionID, subjectID)
	require.NotNil(t, tok, "Issue returned nil token")
	require.NotEmpty(t, wire, "Issue returned empty wire token")
	return wire, tok
}

// mustRotate asserts Rotate succeeds and returns (wire, tok).
func mustRotate(t *testing.T, store refresh.Store, wire string) (string, *refresh.Token) {
	t.Helper()
	newWire, tok, err := store.Rotate(context.Background(), wire)
	require.NoError(t, err, "Rotate(%q)", wire)
	require.NotNil(t, tok, "Rotate returned nil token")
	require.NotEmpty(t, newWire, "Rotate returned empty wire token")
	return newWire, tok
}

func mustPeek(t *testing.T, store refresh.Store, wire string) *refresh.Token {
	t.Helper()
	tok, err := store.Peek(context.Background(), wire)
	require.NoError(t, err, "Peek(%q)", wire)
	require.NotNil(t, tok, "Peek returned nil token")
	return tok
}

// RunContractSuite runs T1-T18 against factory. Each T is a t.Run sub-test.
func RunContractSuite(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("T1_Issue_Basic", func(t *testing.T) { t.Parallel(); runT1IssueBasic(t, factory) })
	t.Run("T2_Issue_TwoTokens_SameSession", func(t *testing.T) { t.Parallel(); runT2IssueTwoTokens(t, factory) })
	t.Run("T3_Rotate_HappyPath", func(t *testing.T) { t.Parallel(); runT3RotateHappyPath(t, factory) })
	t.Run("T4_Rotate_GracePeriod_IssuesChild", func(t *testing.T) { t.Parallel(); runT4GracePeriod(t, factory) })
	t.Run("T5_Rotate_ReuseDetection_CascadeRevoke", func(t *testing.T) { t.Parallel(); runT5ReuseDetection(t, factory) })
	t.Run("T6_Rotate_UnknownToken_Rejected", func(t *testing.T) { t.Parallel(); runT6UnknownToken(t, factory) })
	t.Run("T7_Rotate_RevokedSession_Rejected", func(t *testing.T) { t.Parallel(); runT7RevokedToken(t, factory) })
	t.Run("T8_Rotate_ExpiredToken_Rejected", func(t *testing.T) { t.Parallel(); runT8ExpiredToken(t, factory) })
	t.Run("T9_RevokeSession_Cascade", func(t *testing.T) { t.Parallel(); runT9RevokeCascade(t, factory) })
	t.Run("T10_Rotate_Concurrent_CAS", func(t *testing.T) { runT10ConcurrentCAS(t, factory) })
	t.Run("T11_Clock_ExpiresAt_Calc", func(t *testing.T) { t.Parallel(); runT11ExpiresAtCalc(t, factory) })
	t.Run("T12_Errcode_Category", func(t *testing.T) { t.Parallel(); runT12ErrcodeCategory(t) })
	t.Run("T13_GC_RemovesExpiredTokens", func(t *testing.T) { t.Parallel(); runT13GCRemovesExpired(t, factory) })
	t.Run("T14_Rotate_VerifierMismatch_Rejected", func(t *testing.T) { t.Parallel(); runT14VerifierMismatch(t, factory) })
	t.Run("T15_Rotate_AfterRevokedUser_Rejected", func(t *testing.T) { t.Parallel(); runT15AfterRevokeUser(t, factory) })
	t.Run("T16_Rotate_GraceInsideInterval_DistinctWire", func(t *testing.T) { t.Parallel(); runT16GraceInside(t, factory) })
	t.Run("T17_Rotate_ParseFailure_Uniform", func(t *testing.T) { t.Parallel(); runT17ParseFailure(t, factory) })
	t.Run("T18_RevokeUser_OnlyTargetSubject", func(t *testing.T) { t.Parallel(); runT18RevokeUser(t, factory) })
	t.Run("T19_Peek_DoesNotAdvanceLineage", func(t *testing.T) { t.Parallel(); runT19PeekDoesNotAdvance(t, factory) })
	t.Run("T20_Peek_RejectionParityAndReuseCascade", func(t *testing.T) {
		t.Parallel()
		runT20PeekRejectionParityAndReuseCascade(t, factory)
	})
	t.Run("T21_Peek_DoesNotConsumeGraceBudget", func(t *testing.T) {
		t.Parallel()
		runT21PeekDoesNotConsumeGraceBudget(t, factory)
	})
}

// runT1IssueBasic: Issue wire length 66, Token has ID/session/subject/times,
// CreatedAt within 1µs of ExpiresAt - MaxAge.
func runT1IssueBasic(t *testing.T, factory Factory) {
	store, _ := factory(t, defaultPolicy)
	wire, tok := mustIssue(t, store, "sess-1", "user-1")

	assert.Len(t, wire, refresh.WireLen, "wire length must be 66 chars")
	assert.Equal(t, 1, strings.Count(wire, "."), "wire must have exactly one dot")
	assert.Equal(t, "sess-1", tok.SessionID)
	assert.Equal(t, "user-1", tok.SubjectID)
	assert.NotZero(t, tok.ID, "Token.ID must be non-nil UUID")
	assert.True(t, tok.ExpiresAt.After(tok.CreatedAt), "ExpiresAt > CreatedAt")
}

// runT2IssueTwoTokens: two Issue calls for same session return distinct wires.
func runT2IssueTwoTokens(t *testing.T, factory Factory) {
	store, _ := factory(t, defaultPolicy)
	w1, t1 := mustIssue(t, store, "sess-2", "user-2")
	w2, t2 := mustIssue(t, store, "sess-2", "user-2")
	assert.NotEqual(t, w1, w2, "distinct wire tokens")
	assert.NotEqual(t, t1.ID, t2.ID, "distinct Token.IDs")
}

// runT3RotateHappyPath: Rotate returns new distinct wire; old wire becomes
// rotated-parent (flipped to grace mode); new Token has a fresh UUID.
func runT3RotateHappyPath(t *testing.T, factory Factory) {
	store, _ := factory(t, defaultPolicy)
	oldWire, oldTok := mustIssue(t, store, "sess-3", "user-3")
	newWire, newTok := mustRotate(t, store, oldWire)

	assert.NotEqual(t, oldWire, newWire)
	assert.NotEqual(t, oldTok.ID, newTok.ID)
	assert.Equal(t, "sess-3", newTok.SessionID)
	assert.Len(t, newWire, refresh.WireLen)
}

// runT4GracePeriod: within ReuseInterval, presenting the parent token again
// yields a distinct sibling child wire (not a replay of the first child).
// The append-only model preserves idempotency without leaking tokens.
func runT4GracePeriod(t *testing.T, factory Factory) {
	policy := refresh.Policy{
		ReuseInterval:  testtime.D5s,
		MaxAge:         storeMaxAge7d,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}
	store, clock := factory(t, policy)

	parentWire, _ := mustIssue(t, store, "sess-4", "user-4")
	firstChild, _ := mustRotate(t, store, parentWire)

	clock.Advance(testtime.D3s) // < 5s — within grace window

	secondChild, _, err := store.Rotate(context.Background(), parentWire)
	require.NoError(t, err, "grace-window retry must succeed")
	assert.NotEqual(t, firstChild, secondChild, "grace retry must produce a distinct new wire")
	assert.NotEqual(t, parentWire, secondChild)
}

// runT5ReuseDetection: parent presented beyond ReuseInterval triggers cascade
// revocation and ErrRejected; subsequent Rotates on the chain also fail.
func runT5ReuseDetection(t *testing.T, factory Factory) {
	policy := refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         storeMaxAge7d,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}
	store, clock := factory(t, policy)
	ctx := context.Background()

	parentWire, _ := mustIssue(t, store, "sess-5", "user-5")
	childWire, _ := mustRotate(t, store, parentWire)

	clock.Advance(testtime.D10s) // > 2s — beyond grace

	_, _, err := store.Rotate(ctx, parentWire)
	require.ErrorIs(t, err, refresh.ErrRejected, "reuse-after-grace must yield ErrRejected")

	// Lineage must be dead: current child also rejected.
	_, _, err = store.Rotate(ctx, childWire)
	require.ErrorIs(t, err, refresh.ErrRejected, "current child must be revoked after reuse detection")
}

// runT6UnknownToken: random but well-formed wire token → ErrRejected.
func runT6UnknownToken(t *testing.T, factory Factory) {
	store, _ := factory(t, defaultPolicy)

	// Generate a syntactically valid but DB-unknown wire.
	other, _ := factory(t, defaultPolicy)
	foreign, _, err := other.Issue(context.Background(), "other-sess", "other-user")
	require.NoError(t, err)

	_, _, err = store.Rotate(context.Background(), foreign)
	require.ErrorIs(t, err, refresh.ErrRejected, "unknown selector must yield ErrRejected")
}

// runT7RevokedToken: RevokeSession then Rotate → ErrRejected.
func runT7RevokedToken(t *testing.T, factory Factory) {
	store, _ := factory(t, defaultPolicy)
	ctx := context.Background()

	wire, _ := mustIssue(t, store, "sess-7", "user-7")
	require.NoError(t, store.RevokeSession(ctx, "sess-7"))

	_, _, err := store.Rotate(ctx, wire)
	require.ErrorIs(t, err, refresh.ErrRejected)
}

// runT8ExpiredToken: clock past MaxAge → ErrRejected.
func runT8ExpiredToken(t *testing.T, factory Factory) {
	policy := refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         testtime.D1h,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}
	store, clock := factory(t, policy)

	wire, _ := mustIssue(t, store, "sess-8", "user-8")
	clock.Advance(testtime.D2h)

	_, _, err := store.Rotate(context.Background(), wire)
	require.ErrorIs(t, err, refresh.ErrRejected)
}

// runT9RevokeCascade: 3 rotations then RevokeSession → current child rejected.
func runT9RevokeCascade(t *testing.T, factory Factory) {
	store, _ := factory(t, defaultPolicy)
	ctx := context.Background()

	wire, _ := mustIssue(t, store, "sess-9", "user-9")
	for range 3 {
		wire, _ = mustRotate(t, store, wire)
	}

	require.NoError(t, store.RevokeSession(ctx, "sess-9"))

	_, _, err := store.Rotate(ctx, wire)
	require.ErrorIs(t, err, refresh.ErrRejected)
}

// runT10ConcurrentCAS: 100 goroutines Rotate the same parent.
//
// Under grace semantics every goroutine either (a) is the one that flipped
// parent.rotated_at, or (b) arrives within the grace window and is given a
// distinct new child wire, or (c) arrives after the window and trips reuse
// detection (ErrRejected + cascade revoke).
//
// In practice with FakeClock the window always includes all 100 calls, so
// every goroutine must return err == nil and a wire of length 66. Assertion
// model: all successes, at least N-1 distinct child wires (the first wins
// the rotated_at flip, the others produce siblings).
//
// GraceMaxReuses is set to 200 so the concurrent test never hits the grace
// counter cap. The cap is separately exercised by T20 and the X14 integration
// tests (T20_GraceCounterCapTriggersReuse).
const t10GraceMaxReuses = 200

func runT10ConcurrentCAS(t *testing.T, factory Factory) {
	p := defaultPolicy
	p.GraceMaxReuses = t10GraceMaxReuses
	store, _ := factory(t, p)
	parentWire, _ := mustIssue(t, store, "sess-10", "user-10")

	const goroutines = 100
	type result struct {
		wire string
		err  error
	}
	results := make(chan result, goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			w, _, e := store.Rotate(context.Background(), parentWire)
			results <- result{w, e}
		}()
	}
	wg.Wait()
	close(results)

	distinct := make(map[string]struct{})
	errs := 0
	badWires := 0
	for r := range results {
		if r.err != nil {
			errs++
			continue
		}
		if len(r.wire) != refresh.WireLen {
			badWires++
		}
		distinct[r.wire] = struct{}{}
	}
	assert.Zero(t, errs, "all concurrent Rotates within grace window must succeed")
	assert.Zero(t, badWires, "every concurrent Rotate must return a wire of length WireLen")
	assert.Equal(t, goroutines, len(distinct), "each Rotate must yield a distinct wire")
}

// runT11ExpiresAtCalc: ExpiresAt == now + MaxAge at Issue time.
func runT11ExpiresAtCalc(t *testing.T, factory Factory) {
	policy := refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         storeMaxAge48h,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}
	store, clock := factory(t, policy)

	now := clock.Now()
	_, tok, err := store.Issue(context.Background(), "sess-11", "user-11")
	require.NoError(t, err)
	require.NotNil(t, tok)
	assert.True(t, tok.ExpiresAt.Equal(now.Add(policy.MaxAge)))
}

// runT12ErrcodeCategory: ErrRejected is CategoryAuth (→ HTTP 401 → not infra).
func runT12ErrcodeCategory(t *testing.T) {
	var ec *errcode.Error
	require.ErrorAs(t, refresh.ErrRejected, &ec)
	assert.Equal(t, errcode.CategoryAuth, ec.Category)
	assert.Equal(t, errcode.ErrRefreshTokenRejected, ec.Code)
	assert.False(t, errcode.IsInfraError(refresh.ErrRejected))
}

// runT13GCRemovesExpired: GC drops rows past expiresAt.
func runT13GCRemovesExpired(t *testing.T, factory Factory) {
	policy := refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         testtime.D1h,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}
	store, clock := factory(t, policy)

	activeWire, _ := mustIssue(t, store, "sess-13a", "user-13a")
	revokedWire, _ := mustIssue(t, store, "sess-13b", "user-13b")
	require.NoError(t, store.RevokeSession(context.Background(), "sess-13b"))

	clock.Advance(testtime.D2h)
	now := clock.Now()

	removed, err := store.GC(context.Background(), now)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, removed, 2, "both expired rows must be removed")

	_, _, err = store.Rotate(context.Background(), activeWire)
	assert.ErrorIs(t, err, refresh.ErrRejected)
	_, _, err = store.Rotate(context.Background(), revokedWire)
	assert.ErrorIs(t, err, refresh.ErrRejected)
}

// runT14VerifierMismatch: correct selector + wrong verifier → ErrRejected.
//
// Security invariant: the response must be indistinguishable from an
// unknown-selector rejection (no oracle distinguishing "I know this
// selector" from "I don't").
func runT14VerifierMismatch(t *testing.T, factory Factory) {
	store, _ := factory(t, defaultPolicy)

	// Issue A and B in different subjects.
	wireA, _ := mustIssue(t, store, "sess-14a", "user-14a")
	wireB, _ := mustIssue(t, store, "sess-14b", "user-14b")

	selA, _, _ := refresh.ParseOpaque(wireA)
	_, verB, _ := refresh.ParseOpaque(wireB)

	// Crafted: selector from A, verifier from B.
	crafted := refresh.EncodeOpaque(selA, verB)

	_, _, err := store.Rotate(context.Background(), crafted)
	assert.ErrorIs(t, err, refresh.ErrRejected, "selector-A + verifier-B must reject")

	// Original A must still work (verifier_hash untouched by the failed attempt).
	_, _, err = store.Rotate(context.Background(), wireA)
	assert.NoError(t, err, "legitimate rotate on A must still succeed after crafted attempt")
}

// runT15AfterRevokeUser: RevokeUser invalidates every chain for that subject.
func runT15AfterRevokeUser(t *testing.T, factory Factory) {
	store, _ := factory(t, defaultPolicy)
	ctx := context.Background()

	userAWire1, _ := mustIssue(t, store, "sess-a1", t15TargetSubject)
	userAWire2, _ := mustIssue(t, store, "sess-a2", t15TargetSubject)
	userBWire, _ := mustIssue(t, store, "sess-b1", t15OtherSubject)

	require.NoError(t, store.RevokeUser(ctx, t15TargetSubject))

	_, _, err := store.Rotate(ctx, userAWire1)
	assert.ErrorIs(t, err, refresh.ErrRejected)
	_, _, err = store.Rotate(ctx, userAWire2)
	assert.ErrorIs(t, err, refresh.ErrRejected)

	// user-B must be unaffected.
	_, _, err = store.Rotate(ctx, userBWire)
	assert.NoError(t, err)
}

// runT16GraceInside: parent presented twice inside the grace window must
// yield two distinct child wires (see T4 — this is a stricter assertion
// on the append-only sibling semantics).
func runT16GraceInside(t *testing.T, factory Factory) {
	policy := refresh.Policy{
		ReuseInterval:  testtime.D10s,
		MaxAge:         storeMaxAge7d,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}
	store, _ := factory(t, policy)

	parent, _ := mustIssue(t, store, "sess-16", "user-16")
	child1, _ := mustRotate(t, store, parent)
	child2, _ := mustRotate(t, store, parent) // same parent, within grace
	child3, _ := mustRotate(t, store, parent)

	assert.NotEqual(t, child1, child2)
	assert.NotEqual(t, child1, child3)
	assert.NotEqual(t, child2, child3)

	// All three children are independently live; rotating any of them
	// succeeds (sibling chains diverge freely in the append-only model).
	_, _, err := store.Rotate(context.Background(), child1)
	assert.NoError(t, err)
	_, _, err = store.Rotate(context.Background(), child2)
	assert.NoError(t, err)
	_, _, err = store.Rotate(context.Background(), child3)
	assert.NoError(t, err, "child3 must also be independently live")
}

// runT17ParseFailure: every shape of malformed wire token returns ErrRejected
// without ever touching the store's backing state.
func runT17ParseFailure(t *testing.T, factory Factory) {
	store, _ := factory(t, defaultPolicy)

	cases := []string{
		"",
		"no-dot-at-all",
		"two.dots.here",
		".",
		strings.Repeat("a", 66), // correct length, no dot at position 22
		strings.Repeat("!", 22) + "." + strings.Repeat("b", 43),
	}
	for _, in := range cases {
		_, _, err := store.Rotate(context.Background(), in)
		assert.ErrorIs(t, err, refresh.ErrRejected, "malformed input %q must yield ErrRejected", in)
	}
}

// runT18RevokeUser: RevokeUser leaves other subjects' chains live and is a
// no-op on repeat calls.
func runT18RevokeUser(t *testing.T, factory Factory) {
	store, _ := factory(t, defaultPolicy)
	ctx := context.Background()

	aWire, _ := mustIssue(t, store, "sess-18a", t18TargetSubject)
	bWire, _ := mustIssue(t, store, "sess-18b", t18OtherSubject)

	require.NoError(t, store.RevokeUser(ctx, t18TargetSubject))
	require.NoError(t, store.RevokeUser(ctx, t18TargetSubject)) // idempotent

	_, _, err := store.Rotate(ctx, aWire)
	assert.ErrorIs(t, err, refresh.ErrRejected)

	_, _, err = store.Rotate(ctx, bWire)
	assert.NoError(t, err, "user-18B chain must survive RevokeUser(user-18A)")

	// Unknown subject is a no-op.
	require.NoError(t, store.RevokeUser(ctx, "nobody"))
}

func runT19PeekDoesNotAdvance(t *testing.T, factory Factory) {
	store, _ := factory(t, defaultPolicy)

	parentWire, parentTok := mustIssue(t, store, "sess-19", "user-19")
	peeked := mustPeek(t, store, parentWire)
	assert.Equal(t, parentTok.ID, peeked.ID)
	assert.Equal(t, "sess-19", peeked.SessionID)

	childWire, childTok := mustRotate(t, store, parentWire)
	assert.NotEqual(t, parentWire, childWire)
	assert.NotEqual(t, parentTok.ID, childTok.ID)

	// The parent is now rotated but still inside grace; Peek must report the
	// presented parent metadata and still avoid issuing another child.
	peekedAgain := mustPeek(t, store, parentWire)
	assert.Equal(t, parentTok.ID, peekedAgain.ID)
}

func runT20PeekRejectionParityAndReuseCascade(t *testing.T, factory Factory) {
	store, clock := factory(t, refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         testtime.D1h,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	})
	ctx := context.Background()

	_, err := store.Peek(ctx, "malformed")
	assert.ErrorIs(t, err, refresh.ErrRejected, "malformed Peek must reject like Rotate")

	revokedWire, _ := mustIssue(t, store, "sess-20-revoked", t20Subject)
	require.NoError(t, store.RevokeSession(ctx, "sess-20-revoked"))
	_, err = store.Peek(ctx, revokedWire)
	assert.ErrorIs(t, err, refresh.ErrRejected, "revoked Peek must reject like Rotate")

	expiredWire, _ := mustIssue(t, store, "sess-20-expired", t20Subject)
	clock.Advance(testtime.D2h)
	_, err = store.Peek(ctx, expiredWire)
	assert.ErrorIs(t, err, refresh.ErrRejected, "expired Peek must reject like Rotate")

	store, clock = factory(t, refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         testtime.D1h,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	})
	parentWire, _ := mustIssue(t, store, "sess-20-reuse", t20Subject)
	childWire, _ := mustRotate(t, store, parentWire)
	clock.Advance(testtime.D3s)

	_, err = store.Peek(ctx, parentWire)
	assert.ErrorIs(t, err, refresh.ErrRejected, "reuse Peek must reject")
	_, _, err = store.Rotate(ctx, childWire)
	assert.ErrorIs(t, err, refresh.ErrRejected, "reuse Peek must cascade revoke the session")
}

// runT21PeekDoesNotConsumeGraceBudget verifies that Peek (read-only by Store
// contract) does not increment the X14 grace-reuse counter. The realistic
// shape is sessionrefresh.Refresh: it calls Peek immediately followed by
// Rotate inside the same request. If Peek consumed a grace slot, the
// effective re-use cap would be halved; configuring GraceMaxReuses=3 would
// allow only 1 retry in practice. This regression caught a PG-vs-memstore
// divergence (PR#388 review) where the PG store incremented used_times in
// the Peek path and memstore did not.
//
// Test shape:
//   - Issue + first Rotate (parent.rotated_at flips; grace window opens;
//     used_times still 0 because the first rotation does not enter
//     handleRotatedRow / markGraceUsed).
//   - Peek the parent GraceMaxReuses+5 times. The broken implementation
//     would silently push used_times past the cap; the fix keeps it at 0.
//   - Drain GraceMaxReuses grace retries (each is a legitimate Rotate that
//     increments used_times to GraceMaxReuses).
//   - One more Rotate now finds used_times == GraceMaxReuses and MUST
//     trigger reuse_detected via cascade revoke.
func runT21PeekDoesNotConsumeGraceBudget(t *testing.T, factory Factory) {
	p := defaultPolicy
	require.Greater(t, p.GraceMaxReuses, 1, "test prerequisites: GraceMaxReuses > 1")
	store, _ := factory(t, p)

	ctx := context.Background()
	parentWire, _ := mustIssue(t, store, "sess-21", "user-21")

	// First Rotate: rotated_at flips. used_times = 0 (handleRotatedRow not
	// reached because rotated_at was nil before this call).
	_, _ = mustRotate(t, store, parentWire)

	// Peek the parent many more times than the grace cap. None of these
	// should consume the grace budget. Without the fix, used_times would
	// reach GraceMaxReuses after this loop and the very next Rotate would
	// be rejected as reuse_detected.
	for i := 0; i < p.GraceMaxReuses+5; i++ {
		_, err := store.Peek(ctx, parentWire)
		require.NoError(t, err, "Peek %d must succeed in grace window", i)
	}

	// All grace slots still available. Drain GraceMaxReuses grace retries
	// (each is a legitimate Rotate that consumes one used_times slot).
	for i := 0; i < p.GraceMaxReuses; i++ {
		_, _, err := store.Rotate(ctx, parentWire)
		require.NoError(t, err, "grace Rotate %d must succeed (used_times %d → %d, cap %d)", i+1, i, i+1, p.GraceMaxReuses)
	}

	// Now used_times == GraceMaxReuses. Next Rotate trips the cap.
	_, _, err := store.Rotate(ctx, parentWire)
	assert.ErrorIs(t, err, refresh.ErrRejected, "Rotate at used_times == GraceMaxReuses must trigger reuse_detected")
}

// Silence unused-imports guard when errcode isn't needed (defensive).
var _ = errors.Is

// RunIdleExpireContractSuite runs the idle-expiry sub-test against the given
// factory. It is separate from RunContractSuite so backends can opt in once they
// support the MaxIdle field (Wave 2 policy enforcement). Both memstore and PG
// must pass this suite after Wave 2.
//
// Test identifiers Tn_IdleExpire_* are the contract for MaxIdle enforcement:
// a token that has not been rotated within MaxIdle of creation must be rejected
// with ErrRejected; the rejection reason is idle_expired.
func RunIdleExpireContractSuite(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("Tn_IdleExpire_RejectsAfterMaxIdle", func(t *testing.T) {
		t.Parallel()
		runTnIdleExpireRejectsAfterMaxIdle(t, factory)
	})
}

// idleExpireMaxIdleWindow is the per-test MaxIdle deadline used by the
// idle-expire contract subtest; one hour is short enough to fit comfortably
// in the test envelope yet long enough to differ from MaxAge.
const idleExpireMaxIdleWindow = testtime.D1h

// runTnIdleExpireRejectsAfterMaxIdle constructs a store with the per-test MaxIdle,
// issues a token, advances the clock past it, then calls Rotate and asserts
// ErrRejected is returned (idle_expired path).
func runTnIdleExpireRejectsAfterMaxIdle(t *testing.T, factory Factory) {
	policy := refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         storeMaxAge7d,
		MaxIdle:        idleExpireMaxIdleWindow,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}
	store, clock := factory(t, policy)
	ctx := context.Background()

	wire, _ := mustIssue(t, store, "sess-idle", "user-idle")

	// Advance past MaxIdle.
	clock.Advance(idleExpireMaxIdleWindow + time.Second)

	_, _, err := store.Rotate(ctx, wire)
	if !errors.Is(err, refresh.ErrRejected) {
		t.Errorf("Rotate after MaxIdle exceeded: got %v, want ErrRejected (idle_expired)", err)
	}
}
