// Package mem provides the in-memory CertRecord repository for the status slice.
// It is used by the demo / memory topology; the postgres-backed implementation
// lands in PR-15. The repository is safe for concurrent use.
package mem

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/framework/kernel/clock"

	"github.com/ghbvf/gocell-mdm/cells/enrollcell/slices/status"
)

// Repository is the in-memory implementation of status.Repository.
// All operations are protected by a read-write mutex. Put is provided for
// seeding records in tests and the composition root (PR-2 enroll writes).
type Repository struct {
	clk     clock.Clock
	mu      sync.RWMutex
	records map[string][]status.CertRecord // keyed by DeviceID
}

// New constructs an empty in-memory Repository. clk is the mandatory positional
// clock (go-standards: clock.Clock is positional, validated via MustHaveClock;
// not a WithClock option or Config field).
func New(clk clock.Clock) *Repository {
	clock.MustHaveClock(clk, "status/mem.New")
	return &Repository{
		clk:     clk,
		records: make(map[string][]status.CertRecord),
	}
}

// ActiveByDeviceID returns the CertRecord with the highest Epoch for deviceID.
// Returns (CertRecord{}, false, nil) when no record exists for the given device.
func (r *Repository) ActiveByDeviceID(_ context.Context, deviceID string) (status.CertRecord, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	recs, ok := r.records[deviceID]
	if !ok || len(recs) == 0 {
		return status.CertRecord{}, false, nil
	}

	best := recs[0]
	for _, rec := range recs[1:] {
		if rec.Epoch > best.Epoch {
			best = rec
		}
	}
	return best, true, nil
}

// Put inserts or appends a CertRecord for testing and seeding (PR-2 enroll path).
// It does not deduplicate by Epoch; callers may insert multiple records for the
// same device to simulate multi-generation history.
func (r *Repository) Put(rec status.CertRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records[rec.DeviceID] = append(r.records[rec.DeviceID], rec)
}

var _ status.Repository = (*Repository)(nil)
