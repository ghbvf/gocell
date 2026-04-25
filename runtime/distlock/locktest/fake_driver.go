// Package locktest provides a controllable in-memory Driver implementation
// and a conformance test suite for use in unit tests.
//
// FakeDriver implements runtime/distlock.Driver in memory and is intended
// for unit tests of the distlock manager. RunDriverConformance verifies
// that any Driver implementation behaves identically for token ownership
// semantics.
package locktest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/runtime/distlock"
)

// Compile-time assertion: FakeDriver must satisfy distlock.Driver.
var _ distlock.Driver = (*FakeDriver)(nil)

// fakeEntry represents a key held in the FakeDriver.
type fakeEntry struct {
	token     string
	expiresAt time.Time
}

// FakeDriver is a thread-safe in-memory implementation of distlock.Driver.
// It is intended for unit tests only.
//
// Controls:
//   - NextSetNXResult: if set to false, the next SetNX returns (false, nil) — simulates busy key
//   - NextRenewError:  if non-nil, the next Renew call returns (false, err)
//   - NextRenewHeld:   if set to false, the next Renew returns (false, nil) — simulates ownership lost
//   - ErrIO:           if non-nil, injected as the error for SetNX/Renew/Release calls
type FakeDriver struct {
	mu    sync.Mutex
	keys  map[string]*fakeEntry
	calls map[string]*atomic.Int64

	// Injection controls (consumed once per call, then reset).
	nextSetNXResult *bool
	nextRenewError  error
	nextRenewHeld   *bool

	// clock for TTL expiry checks (defaults to real time.Now).
	clock func() time.Time
}

// NewFakeDriver creates a new FakeDriver using real-time clock.
func NewFakeDriver() *FakeDriver {
	fd := &FakeDriver{
		keys:  make(map[string]*fakeEntry),
		calls: make(map[string]*atomic.Int64),
		clock: time.Now,
	}
	for _, m := range []string{"SetNX", "Renew", "Release"} {
		fd.calls[m] = &atomic.Int64{}
	}
	return fd
}

// WithClock replaces the time source used for TTL expiry.
// Useful when tests need to advance time to simulate TTL expiry.
func (fd *FakeDriver) WithClock(now func() time.Time) *FakeDriver {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.clock = now
	return fd
}

// SetNextSetNX injects the result for the next SetNX call.
// false simulates "another holder owns the key".
func (fd *FakeDriver) SetNextSetNX(acquired bool) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.nextSetNXResult = &acquired
}

// SetNextRenewError injects an I/O error for the next Renew call.
func (fd *FakeDriver) SetNextRenewError(err error) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.nextRenewError = err
}

// SetNextRenewHeld injects the held result for the next Renew call.
// false simulates ownership lost (another holder took the key).
func (fd *FakeDriver) SetNextRenewHeld(held bool) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.nextRenewHeld = &held
}

// Calls returns the total number of times the named method was called.
// method is one of "SetNX", "Renew", "Release".
func (fd *FakeDriver) Calls(method string) int {
	if c, ok := fd.calls[method]; ok {
		return int(c.Load())
	}
	return 0
}

// ResetCalls resets the call counters for all methods.
func (fd *FakeDriver) ResetCalls() {
	for _, c := range fd.calls {
		c.Store(0)
	}
}

// SetNX implements distlock.Driver.
func (fd *FakeDriver) SetNX(_ context.Context, key, token string, ttl time.Duration) (bool, error) {
	fd.calls["SetNX"].Add(1)

	fd.mu.Lock()
	defer fd.mu.Unlock()

	// Consume injected result first.
	if fd.nextSetNXResult != nil {
		result := *fd.nextSetNXResult
		fd.nextSetNXResult = nil
		if !result {
			return false, nil
		}
		// result==true falls through to normal acquire path below
	}

	// Expire stale keys.
	if entry, ok := fd.keys[key]; ok {
		if fd.clock().Before(entry.expiresAt) {
			// Key still valid and held by another token.
			return false, nil
		}
		// Expired: allow overwrite.
	}

	fd.keys[key] = &fakeEntry{
		token:     token,
		expiresAt: fd.clock().Add(ttl),
	}
	return true, nil
}

// Renew implements distlock.Driver.
func (fd *FakeDriver) Renew(_ context.Context, key, token string, ttl time.Duration) (bool, error) {
	fd.calls["Renew"].Add(1)

	fd.mu.Lock()
	defer fd.mu.Unlock()

	// Consume injected error.
	if fd.nextRenewError != nil {
		err := fd.nextRenewError
		fd.nextRenewError = nil
		return false, err
	}

	// Consume injected held.
	if fd.nextRenewHeld != nil {
		held := *fd.nextRenewHeld
		fd.nextRenewHeld = nil
		if held {
			// Actually renew in the map too.
			if entry, ok := fd.keys[key]; ok && entry.token == token {
				entry.expiresAt = fd.clock().Add(ttl)
			}
			return true, nil
		}
		return false, nil
	}

	entry, ok := fd.keys[key]
	if !ok || fd.clock().After(entry.expiresAt) {
		// Key gone or expired.
		return false, nil
	}
	if entry.token != token {
		// Different holder.
		return false, nil
	}
	entry.expiresAt = fd.clock().Add(ttl)
	return true, nil
}

// Release implements distlock.Driver.
func (fd *FakeDriver) Release(_ context.Context, key, token string) error {
	fd.calls["Release"].Add(1)

	fd.mu.Lock()
	defer fd.mu.Unlock()

	entry, ok := fd.keys[key]
	if !ok {
		// Already gone: idempotent.
		return nil
	}
	if entry.token != token {
		// Different holder: do not delete (per C-3 conformance).
		return nil
	}
	delete(fd.keys, key)
	return nil
}

// ErrDriverIO is a sentinel error for injecting I/O failures in tests.
var ErrDriverIO = errors.New("locktest: simulated driver I/O error")

// Snapshot returns the current keys held by the FakeDriver.
// Used for white-box assertions in conformance tests.
func (fd *FakeDriver) Snapshot() map[string]string {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	out := make(map[string]string, len(fd.keys))
	for k, v := range fd.keys {
		out[k] = v.token
	}
	return out
}
