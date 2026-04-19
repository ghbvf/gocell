// Package storetest provides a reusable contract test suite for refresh.Store
// implementations. Backends (memstore, postgres) each run RunContractSuite
// to prove they honour the same semantics defined in F2 contract C1-C7 from
// docs/plans/202604191515-auth-federated-whistle.md.
//
// ref: dexidp/dex storage/storage.go RefreshToken
// ref: dexidp/dex server/refreshhandlers.go AllowedToReuse + CAS semantics
package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

// FakeClock is a step-driven clock for deterministic testing.
// Callers advance it via Advance; Now returns the current synthetic time.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFakeClock constructs a FakeClock anchored at the supplied time.
func NewFakeClock(t time.Time) *FakeClock {
	return &FakeClock{now: t}
}

// Now returns the current synthetic time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// Factory constructs a fresh Store and its deterministic clock. Backends that
// have per-test setup (e.g. PG schema reset) should do it inside Factory.
type Factory func(t *testing.T, policy refresh.Policy) (store refresh.Store, clock *FakeClock)

// defaultPolicy is the policy used by tests unless they need specific values.
var defaultPolicy = refresh.Policy{
	ReuseInterval: 2 * time.Second,
	MaxAge:        7 * 24 * time.Hour,
}

// mustIssue calls store.Issue and fatally fails the test if it returns an error
// or a nil token. This helper is used by multiple tests to set up preconditions.
func mustIssue(t *testing.T, store refresh.Store, sessionID, subjectID string) *refresh.Token {
	t.Helper()
	tok, err := store.Issue(context.Background(), sessionID, subjectID)
	if err != nil {
		t.Fatalf("Issue(%q, %q): unexpected error: %v", sessionID, subjectID, err)
	}
	if tok == nil {
		t.Fatalf("Issue(%q, %q): returned nil token (not yet implemented)", sessionID, subjectID)
	}
	return tok
}

// mustRotate calls store.Rotate and fatally fails the test if it returns an
// error or a nil token.
func mustRotate(t *testing.T, store refresh.Store, tokenID string) *refresh.Token {
	t.Helper()
	tok, err := store.Rotate(context.Background(), tokenID)
	if err != nil {
		t.Fatalf("Rotate(%q): unexpected error: %v", tokenID, err)
	}
	if tok == nil {
		t.Fatalf("Rotate(%q): returned nil token (not yet implemented)", tokenID)
	}
	return tok
}

// RunContractSuite runs the full C1-C7 contract test suite against factory.
// Each of the 13 test cases (T1-T13) is a t.Run sub-test; they can be run
// individually with -run TestXxx/T1_Issue_Basic etc.
func RunContractSuite(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("T1_Issue_Basic", func(t *testing.T) { t.Parallel(); runT1IssueBasic(t, factory) })
	t.Run("T2_Issue_TwoTokens_SameSession", func(t *testing.T) { t.Parallel(); runT2IssueTwoTokens(t, factory) })
	t.Run("T3_Rotate_HappyPath", func(t *testing.T) { t.Parallel(); runT3RotateHappyPath(t, factory) })
	t.Run("T4_Rotate_GracePeriod_Idempotent", func(t *testing.T) { t.Parallel(); runT4GracePeriod(t, factory) })
	t.Run("T5_Rotate_ReuseDetection_CascadeRevoke", func(t *testing.T) { t.Parallel(); runT5ReuseDetection(t, factory) })
	t.Run("T6_Rotate_UnknownToken", func(t *testing.T) { t.Parallel(); runT6UnknownToken(t, factory) })
	t.Run("T7_Rotate_RevokedToken", func(t *testing.T) { t.Parallel(); runT7RevokedToken(t, factory) })
	t.Run("T8_Rotate_ExpiredToken", func(t *testing.T) { t.Parallel(); runT8ExpiredToken(t, factory) })
	t.Run("T9_Revoke_Cascade", func(t *testing.T) { t.Parallel(); runT9RevokeCascade(t, factory) })
	t.Run("T10_Rotate_Concurrent_CAS", func(t *testing.T) { runT10ConcurrentCAS(t, factory) })
	t.Run("T11_Clock_ExpiresAt_Calc", func(t *testing.T) { t.Parallel(); runT11ExpiresAtCalc(t, factory) })
	t.Run("T12_Errcode_Categories", func(t *testing.T) { t.Parallel(); runT12ErrcodeCategories(t) })
	t.Run("T13_GC_RemovesExpiredTokens", func(t *testing.T) { t.Parallel(); runT13GCRemovesExpired(t, factory) })
}

// runT1IssueBasic: Issue succeeds → ID len 43, ObsoleteToken empty, CreatedAt == LastUsed.
func runT1IssueBasic(t *testing.T, factory Factory) {
	t.Helper()
	store, _ := factory(t, defaultPolicy)
	ctx := context.Background()

	tok, err := store.Issue(ctx, "sess-1", "user-1")
	if err != nil {
		t.Fatalf("Issue: unexpected error: %v", err)
	}
	if tok == nil {
		t.Fatal("Issue: returned nil token (not yet implemented)")
	}
	if len(tok.ID) != 43 {
		t.Errorf("Issue: want ID length 43, got %d (%q)", len(tok.ID), tok.ID)
	}
	if tok.ObsoleteToken != "" {
		t.Errorf("Issue: want empty ObsoleteToken, got %q", tok.ObsoleteToken)
	}
	if tok.SessionID != "sess-1" {
		t.Errorf("Issue: want SessionID %q, got %q", "sess-1", tok.SessionID)
	}
	if tok.SubjectID != "user-1" {
		t.Errorf("Issue: want SubjectID %q, got %q", "user-1", tok.SubjectID)
	}
	if !tok.CreatedAt.Equal(tok.LastUsed) {
		t.Errorf("Issue: want CreatedAt == LastUsed, got %v vs %v", tok.CreatedAt, tok.LastUsed)
	}
}

// runT2IssueTwoTokens: two Issue calls for same session return different IDs.
func runT2IssueTwoTokens(t *testing.T, factory Factory) {
	t.Helper()
	store, _ := factory(t, defaultPolicy)
	ctx := context.Background()

	tok1, err := store.Issue(ctx, "sess-2", "user-2")
	if err != nil {
		t.Fatalf("Issue #1: %v", err)
	}
	if tok1 == nil {
		t.Fatal("Issue #1: returned nil token (not yet implemented)")
	}
	tok2, err := store.Issue(ctx, "sess-2", "user-2")
	if err != nil {
		t.Fatalf("Issue #2: %v", err)
	}
	if tok2 == nil {
		t.Fatal("Issue #2: returned nil token (not yet implemented)")
	}
	if tok1.ID == tok2.ID {
		t.Errorf("Issue: two tokens for same session must have different IDs, got %q twice", tok1.ID)
	}
}

// runT3RotateHappyPath: Rotate → new token, ObsoleteToken == old ID.
func runT3RotateHappyPath(t *testing.T, factory Factory) {
	t.Helper()
	store, _ := factory(t, defaultPolicy)

	issued := mustIssue(t, store, "sess-3", "user-3")
	oldID := issued.ID

	rotated, err := store.Rotate(context.Background(), oldID)
	if err != nil {
		t.Fatalf("Rotate: unexpected error: %v", err)
	}
	if rotated == nil {
		t.Fatal("Rotate: returned nil token (not yet implemented)")
	}
	if rotated.ID == oldID {
		t.Errorf("Rotate: new token ID must differ from old ID %q", oldID)
	}
	if rotated.ObsoleteToken != oldID {
		t.Errorf("Rotate: want ObsoleteToken == %q, got %q", oldID, rotated.ObsoleteToken)
	}
	if len(rotated.ID) != 43 {
		t.Errorf("Rotate: want new ID length 43, got %d", len(rotated.ID))
	}
}

// runT4GracePeriod: within ReuseInterval, presenting obsolete token returns current.
func runT4GracePeriod(t *testing.T, factory Factory) {
	t.Helper()
	policy := refresh.Policy{ReuseInterval: 5 * time.Second, MaxAge: 7 * 24 * time.Hour}
	store, clock := factory(t, policy)

	issued := mustIssue(t, store, "sess-4", "user-4")
	obsoleteID := issued.ID

	rotated := mustRotate(t, store, issued.ID)
	currentID := rotated.ID

	clock.Advance(3 * time.Second) // < 5s — within ReuseInterval

	retry, err := store.Rotate(context.Background(), obsoleteID)
	if err != nil {
		t.Fatalf("Rotate grace retry: unexpected error: %v", err)
	}
	if retry == nil {
		t.Fatal("Rotate grace retry: returned nil token (not yet implemented)")
	}
	if retry.ID != currentID {
		t.Errorf("Rotate grace retry: want current ID %q, got %q", currentID, retry.ID)
	}
}

// runT5ReuseDetection: beyond ReuseInterval, obsolete token triggers cascade revoke.
func runT5ReuseDetection(t *testing.T, factory Factory) {
	t.Helper()
	policy := refresh.Policy{ReuseInterval: 2 * time.Second, MaxAge: 7 * 24 * time.Hour}
	store, clock := factory(t, policy)
	ctx := context.Background()

	issued := mustIssue(t, store, "sess-5", "user-5")
	obsoleteID := issued.ID

	rotated := mustRotate(t, store, issued.ID)

	clock.Advance(10 * time.Second) // > 2s — beyond ReuseInterval

	_, err := store.Rotate(ctx, obsoleteID)
	if !errors.Is(err, refresh.ErrTokenReused) {
		t.Errorf("Rotate reuse: want ErrTokenReused, got %v", err)
	}

	// Session must be fully revoked.
	_, err = store.Rotate(ctx, obsoleteID)
	if err == nil {
		t.Error("Rotate after cascade revoke: expected an error, got nil")
	}

	// Verify cascade revoke also invalidates the current-generation token
	// (plan §F2 C1: reuse detection triggers Revoke(sessionID) atomically).
	if _, err := store.Rotate(ctx, rotated.ID); !errors.Is(err, refresh.ErrTokenRevoked) {
		t.Errorf("Rotate current after cascade revoke: want ErrTokenRevoked, got %v", err)
	}
}

// runT6UnknownToken: random token → ErrTokenNotFound.
func runT6UnknownToken(t *testing.T, factory Factory) {
	t.Helper()
	store, _ := factory(t, defaultPolicy)

	_, err := store.Rotate(context.Background(), "this-token-does-not-exist-at-all-X")
	if !errors.Is(err, refresh.ErrTokenNotFound) {
		t.Errorf("Rotate unknown: want ErrTokenNotFound, got %v", err)
	}
}

// runT7RevokedToken: Revoke then Rotate → ErrTokenRevoked.
func runT7RevokedToken(t *testing.T, factory Factory) {
	t.Helper()
	store, _ := factory(t, defaultPolicy)
	ctx := context.Background()

	issued := mustIssue(t, store, "sess-7", "user-7")

	if err := store.Revoke(ctx, "sess-7"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	_, err := store.Rotate(ctx, issued.ID)
	if !errors.Is(err, refresh.ErrTokenRevoked) {
		t.Errorf("Rotate revoked: want ErrTokenRevoked, got %v", err)
	}
}

// runT8ExpiredToken: clock past MaxAge → ErrTokenExpired.
func runT8ExpiredToken(t *testing.T, factory Factory) {
	t.Helper()
	policy := refresh.Policy{ReuseInterval: 2 * time.Second, MaxAge: 1 * time.Hour}
	store, clock := factory(t, policy)

	issued := mustIssue(t, store, "sess-8", "user-8")

	clock.Advance(2 * time.Hour) // > MaxAge

	_, err := store.Rotate(context.Background(), issued.ID)
	if !errors.Is(err, refresh.ErrTokenExpired) {
		t.Errorf("Rotate expired: want ErrTokenExpired, got %v", err)
	}
}

// runT9RevokeCascade: 3 rotations then Revoke → all chain revoked.
func runT9RevokeCascade(t *testing.T, factory Factory) {
	t.Helper()
	store, _ := factory(t, defaultPolicy)
	ctx := context.Background()

	tok := mustIssue(t, store, "sess-9", "user-9")
	for range 3 {
		tok = mustRotate(t, store, tok.ID)
	}

	if err := store.Revoke(ctx, "sess-9"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	_, err := store.Rotate(ctx, tok.ID)
	if !errors.Is(err, refresh.ErrTokenRevoked) {
		t.Errorf("Rotate after Revoke cascade: want ErrTokenRevoked, got %v", err)
	}
}

// runT10ConcurrentCAS: 100 goroutines Rotate the same token.
//
// Under grace-semantics (Dex/Fosite): the first goroutine rotates the token,
// the other 99 present the (now-obsolete) original token within the grace
// window and receive the same current token idempotently. No goroutine
// observes an error. The store internally performs exactly one rotation.
//
// Assertion model:
//  1. All 100 goroutines return err == nil.
//  2. All 100 goroutines return tokens with the same ID (the post-rotation
//     current token); this token ID differs from the pre-rotation one.
//  3. Exactly one goroutine observes a non-empty ObsoleteToken equal to the
//     original (the one that actually performed the rotation).
//
// This model verifies CAS correctness more precisely than "exactly 1 success"
// by asserting grace-idempotency alongside single-rotation.
func runT10ConcurrentCAS(t *testing.T, factory Factory) {
	t.Helper()
	store, _ := factory(t, defaultPolicy)

	issued := mustIssue(t, store, "sess-10", "user-10")
	originalID := issued.ID

	const goroutines = 100
	type result struct {
		tok *refresh.Token
		err error
	}
	results := make(chan result, goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			tok, e := store.Rotate(context.Background(), originalID)
			results <- result{tok, e}
		}()
	}
	wg.Wait()
	close(results)

	var (
		returnedIDs     = make(map[string]int) // distinct returned token IDs
		actualRotations int                    // count of tokens whose ObsoleteToken == originalID
	)
	for r := range results {
		if r.err != nil {
			t.Errorf("Rotate concurrent: unexpected error: %v", r.err)
			continue
		}
		if r.tok == nil {
			t.Error("Rotate concurrent: nil token returned")
			continue
		}
		returnedIDs[r.tok.ID]++
		if r.tok.ObsoleteToken == originalID {
			actualRotations++
		}
	}

	if len(returnedIDs) != 1 {
		t.Errorf("Rotate concurrent: all goroutines must return the same token ID, got %d distinct IDs: %v", len(returnedIDs), returnedIDs)
	}
	for id := range returnedIDs {
		if id == originalID {
			t.Errorf("Rotate concurrent: returned token ID should differ from original (rotation did not happen)")
		}
	}
	if actualRotations != 1 {
		t.Errorf("Rotate concurrent CAS: exactly 1 actual rotation must happen (obsolete == original), got %d", actualRotations)
	}
}

// runT11ExpiresAtCalc: ExpiresAt == now + MaxAge at Issue time.
func runT11ExpiresAtCalc(t *testing.T, factory Factory) {
	t.Helper()
	policy := refresh.Policy{ReuseInterval: 2 * time.Second, MaxAge: 48 * time.Hour}
	store, clock := factory(t, policy)

	now := clock.Now()
	tok, err := store.Issue(context.Background(), "sess-11", "user-11")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if tok == nil {
		t.Fatal("Issue: returned nil token (not yet implemented)")
	}

	wantExpiry := now.Add(policy.MaxAge)
	if !tok.ExpiresAt.Equal(wantExpiry) {
		t.Errorf("ExpiresAt: want %v, got %v", wantExpiry, tok.ExpiresAt)
	}
}

// runT13GCRemovesExpired: GC(now) removes tokens whose ExpiresAt < now,
// including both active and revoked rows. Post-GC, the tokens are no
// longer addressable via Rotate.
//
// Covers plan §F2 GC contract (single-time-axis cleanup).
func runT13GCRemovesExpired(t *testing.T, factory Factory) {
	t.Helper()
	policy := refresh.Policy{ReuseInterval: 2 * time.Second, MaxAge: 1 * time.Hour}
	store, clock := factory(t, policy)

	// Issue two chains: one will remain active, one will be revoked.
	activeTok := mustIssue(t, store, "sess-13a", "user-13a")
	revokedTok := mustIssue(t, store, "sess-13b", "user-13b")
	if err := store.Revoke(context.Background(), "sess-13b"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// Advance past MaxAge so both are expired.
	clock.Advance(2 * time.Hour)
	now := clock.Now()

	removed, err := store.GC(context.Background(), now)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if removed != 2 {
		t.Errorf("GC: want 2 rows removed (active + revoked), got %d", removed)
	}

	// Both tokens must be un-addressable.
	if _, err := store.Rotate(context.Background(), activeTok.ID); !errors.Is(err, refresh.ErrTokenNotFound) {
		t.Errorf("Rotate after GC (active): want ErrTokenNotFound, got %v", err)
	}
	if _, err := store.Rotate(context.Background(), revokedTok.ID); !errors.Is(err, refresh.ErrTokenNotFound) {
		t.Errorf("Rotate after GC (revoked): want ErrTokenNotFound, got %v", err)
	}
}

// runT12ErrcodeCategories: sentinel error categories match spec.
func runT12ErrcodeCategories(t *testing.T) {
	t.Helper()

	if errcode.IsInfraError(refresh.ErrTokenReused) {
		t.Error("IsInfraError(ErrTokenReused): want false (CategoryAuth), got true")
	}

	checkCategory := func(name string, err error, want errcode.Category) {
		t.Helper()
		var ec *errcode.Error
		if !errors.As(err, &ec) {
			t.Fatalf("%s is not *errcode.Error", name)
		}
		if ec.Category != want {
			t.Errorf("%s.Category: want %d, got %d", name, want, ec.Category)
		}
	}

	checkCategory("ErrTokenReused", refresh.ErrTokenReused, errcode.CategoryAuth)
	checkCategory("ErrTokenNotFound", refresh.ErrTokenNotFound, errcode.CategoryDomain)
	checkCategory("ErrTokenExpired", refresh.ErrTokenExpired, errcode.CategoryDomain)
	checkCategory("ErrTokenRevoked", refresh.ErrTokenRevoked, errcode.CategoryDomain)
}
