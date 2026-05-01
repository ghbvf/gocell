package vault

// Lock-free version cache tests for TransitKeyProvider.
//
// TransitKeyProvider keeps the latest_version in an atomic.Int64 populated at
// construction (key existence check) and invalidated on Rotate. These tests
// lock down:
//
//   - Current() served from cache after construction (no Vault read).
//   - Rotate() invalidates and refills the cache; subsequent Current() sees
//     the new version.
//   - Concurrent Current() readers do not race or panic.
//   - Concurrent Current() readers + Rotate() do not race; the documented
//     benign Store(0)→Vault read→Store(N+1) window stays free of detector
//     warnings.
//   - Reflective regression guard: TransitKeyProvider must not regrow a
//     sync.RWMutex field.
//
// Reuses the shared fakeVaultClient from transit_provider_test.go.

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestRotate_InvalidatesAndRefillsCache(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 1}
	p := newTestProvider(t, fake)

	// Cache is warmed by NewTransitKeyProvider's existence check; capture the
	// baseline read count and assert no further reads on cache hits. A future
	// constructor change that drops the existence check would silently weaken
	// this test, so guard the implicit "construction issues at least one Read"
	// invariant before relying on it.
	baseline := fake.readCalls.Load()
	if baseline == 0 {
		t.Fatalf("expected NewTransitKeyProvider to issue at least one Read at construction; got 0")
	}
	for i := range 50 {
		if _, err := p.Current(context.Background()); err != nil {
			t.Fatalf("Current loop[%d]: %v", i, err)
		}
	}
	if got := fake.readCalls.Load(); got != baseline {
		t.Errorf("Current() loop must serve cache; reads grew %d→%d", baseline, got)
	}

	// Rotate invalidates + refreshes; one extra read is expected.
	if _, err := p.Rotate(context.Background()); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if fake.rotateCalls.Load() != 1 {
		t.Errorf("rotate call count = %d, want 1", fake.rotateCalls.Load())
	}
	if got := fake.readCalls.Load(); got <= baseline {
		t.Errorf("Rotate must trigger a fresh readLatestVersion; reads stayed at %d", got)
	}

	h, _ := p.Current(context.Background())
	if got := h.ID(); got != "vault-transit:v2" {
		t.Errorf("post-rotate Current().ID() = %q, want vault-transit:v2", got)
	}
}

func TestCurrent_ConcurrentReaders(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 7}
	p := newTestProvider(t, fake)

	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			h, err := p.Current(context.Background())
			if err != nil {
				t.Errorf("Current: %v", err)
				return
			}
			if h.ID() != "vault-transit:v7" {
				t.Errorf("ID() = %q, want vault-transit:v7", h.ID())
			}
		}()
	}
	wg.Wait()
}

// TestRotateAndCurrent_ConcurrentRace exercises the documented benign race
// window between Rotate (Store(0) → readLatestVersion → Store(N+1)) and a
// flood of concurrent Current() callers. Asserts only that nobody errors out
// or returns a malformed handle ID — the run also has to be -race clean.
func TestRotateAndCurrent_ConcurrentRace(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 1}
	p := newTestProvider(t, fake)

	const readerN = 32
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(readerN)
	for range readerN {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				h, err := p.Current(context.Background())
				if err != nil {
					t.Errorf("Current: %v", err)
					return
				}
				if !strings.HasPrefix(h.ID(), "vault-transit:v") {
					t.Errorf("unexpected ID format: %q", h.ID())
					return
				}
			}
		}()
	}
	for i := range 5 {
		if _, err := p.Rotate(context.Background()); err != nil {
			t.Errorf("Rotate iteration %d: %v", i, err)
			break
		}
	}
	close(stop)
	wg.Wait()
}

// TestTransitKeyProvider_NoRWMutex regression-guards the lock removal: the
// provider's top-level fields must never regrow a sync.RWMutex. We assert on
// the field type rather than the field name so that a future legitimate
// sync.Mutex / sync.Once with any name does not trip the test.
func TestTransitKeyProvider_NoRWMutex(t *testing.T) {
	rwMutexType := reflect.TypeFor[sync.RWMutex]()
	rt := reflect.TypeFor[TransitKeyProvider]()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type == rwMutexType {
			t.Fatalf("TransitKeyProvider.%s is sync.RWMutex; "+
				"the provider must stay lock-free — Current/Rotate use atomic.Int64 cache instead",
				f.Name)
		}
	}
}
