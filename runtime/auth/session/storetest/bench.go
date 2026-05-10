package storetest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// NewBenchProtocol mirrors NewTestProtocol but takes *testing.B so callers
// inside Benchmark functions can construct the canonical S2 protocol shape
// without forking testing.TB. The returned *Protocol is identical in shape
// to NewTestProtocol(t).
func NewBenchProtocol(b *testing.B) *session.Protocol {
	b.Helper()
	p, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	if err != nil {
		b.Fatalf("storetest: NewBenchProtocol failed: %v", err)
	}
	return p
}

// BenchFactory mirrors Factory but takes *testing.B so backend setup can use
// b.Fatal / b.Helper. Implementations must return:
//
//   - a fresh session.Store
//   - a *clockmock.FakeClock pinned at EpochAnchor() (Bench reads now from it)
//   - a cleanup func that is safe to call exactly once
type BenchFactory func(b *testing.B) (store session.Store, fakeClock *clockmock.FakeClock, cleanup func())

// Bench runs the canonical Session.Store benchmark suite against backend.
// MemStore and the PG-backed adapter share the same Bench so micro-benchmarks
// stay comparable across backends (PR #444 review carry-over —
// PR444-FU-SESSIONSTORE-BENCH-01). Mirrors the real-world hot paths:
//
//	BenchmarkRevokeForSubject_1000  — credential-event revoke fan-out
//	BenchmarkMixedConcurrent        — login/validate/logout interleave
func Bench(b *testing.B, factory BenchFactory, protocol *session.Protocol) {
	b.Helper()
	if factory == nil {
		b.Fatal("storetest.Bench: factory must not be nil")
	}
	if protocol == nil {
		b.Fatal("storetest.Bench: protocol must not be nil")
	}

	b.Run("RevokeForSubject_1000", func(b *testing.B) {
		benchRevokeForSubject(b, factory, 1000)
	})
	b.Run("MixedConcurrent", func(b *testing.B) {
		benchMixedConcurrent(b, factory)
	})
}

const (
	benchSubject = "bench-subject"
	benchTTL     = time.Hour
	benchEpoch   = int64(1)
)

// benchRevokeForSubject pre-loads N active sessions for a single subject and
// times RevokeForSubject — the credential-event fan-out path.
func benchRevokeForSubject(b *testing.B, factory BenchFactory, perRun int) {
	b.Helper()
	for i := 0; i < b.N; i++ {
		store, fc, cleanup := factory(b)
		seedSubjectSessions(b, store, fc.Now(), perRun)

		b.ReportAllocs()
		b.StartTimer()
		err := store.RevokeForSubject(context.Background(), benchSubject, session.CredentialEventPasswordReset)
		b.StopTimer()
		cleanup()

		if err != nil {
			b.Fatalf("RevokeForSubject: %v", err)
		}
	}
}

// benchMixedConcurrent runs Create/Get/Revoke at a 1:1:1 ratio across all
// CPUs. Backends use this to expose connection-pool / lock contention.
func benchMixedConcurrent(b *testing.B, factory BenchFactory) {
	b.Helper()
	store, fc, cleanup := factory(b)
	defer cleanup()

	// pre-seed so Get/Revoke hit something on the first iteration.
	seedSubjectSessions(b, store, fc.Now(), 100)

	var counter atomic.Uint64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		now := fc.Now()
		for pb.Next() {
			n := counter.Add(1)
			switch n % 3 {
			case 0:
				fix := &session.Session{
					ID:                fmt.Sprintf("sess-mix-%d", n),
					SubjectID:         benchSubject,
					JTI:               fmt.Sprintf("jti-mix-%d", n),
					AuthzEpochAtIssue: benchEpoch,
					CreatedAt:         now,
					ExpiresAt:         now.Add(benchTTL),
				}
				if err := store.Create(ctx, fix); err != nil && !isAcceptableBenchErr(err) {
					b.Errorf("mixed Create: %v", err)
				}
			case 1:
				_, _ = store.Get(ctx, fmt.Sprintf("sess-jti-bench-seed-%d", n%100))
			case 2:
				_ = store.Revoke(ctx, fmt.Sprintf("sess-jti-bench-seed-%d", n%100))
			}
		}
	})
}

// seedSubjectSessions pre-creates n active sessions for benchSubject. Errors
// other than duplicate-key collisions abort the bench (a real fault).
func seedSubjectSessions(b *testing.B, store session.Store, now time.Time, n int) {
	b.Helper()
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fix := &session.Session{
				ID:                fmt.Sprintf("sess-jti-bench-seed-%d", i),
				SubjectID:         benchSubject,
				JTI:               fmt.Sprintf("jti-bench-seed-%d", i),
				AuthzEpochAtIssue: benchEpoch,
				CreatedAt:         now,
				ExpiresAt:         now.Add(benchTTL),
			}
			if err := store.Create(ctx, fix); err != nil && !isAcceptableBenchErr(err) {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		b.Fatalf("seed Create: %v", err)
	}
}

// isAcceptableBenchErr filters away ID-collision noise from mixed-workload
// runs. Storetest cases assert on these directly; bench cases tolerate them
// because the iteration counter intentionally wraps modulo a small window
// (so Get/Revoke can hit warm rows without coordinating with Create).
func isAcceptableBenchErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate") || strings.Contains(msg, "already exists")
}
