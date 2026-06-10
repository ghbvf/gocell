package testutil

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
)

// RealRefreshStore returns a ready-to-use in-memory refresh.Store for unit
// tests. It uses a permissive policy suitable for test scenarios.
func RealRefreshStore(t testing.TB) refresh.Store {
	t.Helper()
	store, err := refreshmem.New(
		refresh.Policy{
			ReuseInterval:  time.Second,
			MaxAge:         time.Hour,
			MaxIdle:        refresh.DefaultMaxIdle,
			GraceMaxReuses: refresh.DefaultGraceMaxReuses,
		},
		clock.Real(), nil,
	)
	if err != nil {
		t.Fatalf("testutil.RealRefreshStore: store setup failed: %v", err)
	}
	return store
}
