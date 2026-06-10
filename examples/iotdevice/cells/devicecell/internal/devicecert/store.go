// Package devicecert holds the devicecell certificate-renewal state.
//
// CertState models a device's current TLS/identity certificate as a (NotAfter,
// Epoch) pair. The renewal reconcile.Loop scans this store for near-expiry certs
// and enqueues a deduplicated rotate-cert command per epoch (the loop is wired by
// buildCertRenewalSweeper in cells/devicecell/cell.go over the Reconciler in this
// package — there is no dedicated slice).
//
// EPHEMERAL BY DESIGN: Store is an in-memory, cell-internal operational store —
// it is NOT persisted to the devices table. This is a deliberate scope choice for
// the iotdevice L4 reference example (issue #1757): the demonstrated invariant is
// "reconcile → async command → cross-tick Claimer dedup", not certificate PKI
// durability. In durable (PG) deployments the device rows survive a restart but
// the cert state does not; after a restart the renewal loop re-issues cert state
// only for devices that register again. Promoting cert state to a persisted,
// restart-surviving store is out of scope here (would require a devices-table
// migration + schema-guard change) and is a separate concern from the producer
// mechanic this example exists to show.
package devicecert

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// CertState is a device's current certificate identity. Epoch is a monotonic
// per-device counter advanced on each (re-)issue; it makes the rotate-cert
// command id deterministic per certificate generation so renewals for the SAME
// epoch dedup across reconcile ticks, while a post-rotation re-issue (new epoch)
// becomes a fresh, dispatchable command.
type CertState struct {
	DeviceID string
	NotAfter time.Time
	Epoch    int64
}

// Store is a thread-safe in-memory device certificate store. See the package doc
// for its ephemeral, cell-internal scope.
type Store struct {
	mu    sync.RWMutex
	certs map[string]CertState
}

// NewStore creates an empty certificate store.
func NewStore() *Store {
	return &Store{certs: make(map[string]CertState)}
}

// Issue (re-)issues a device's certificate with the given NotAfter and returns
// the resulting state. The first issue starts Epoch at 1; each subsequent issue
// for the same device advances Epoch by one. Fails fast on an empty deviceID or
// a zero NotAfter.
func (s *Store) Issue(_ context.Context, deviceID string, notAfter time.Time) (CertState, error) {
	if deviceID == "" {
		return CertState{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecert.Issue: deviceID must not be empty")
	}
	if notAfter.IsZero() {
		return CertState{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecert.Issue: notAfter must not be zero")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	st := CertState{
		DeviceID: deviceID,
		NotAfter: notAfter,
		Epoch:    s.certs[deviceID].Epoch + 1, // zero value -> 1 on first issue
	}
	s.certs[deviceID] = st
	return st, nil
}

// ScanNearExpiry returns every certificate whose NotAfter is at or before cutoff,
// sorted by DeviceID for deterministic iteration. It is a pure read filter — the
// caller (renewal loop) supplies cutoff = now + renewal-threshold. The in-memory
// implementation never returns a non-nil error; the error return preserves the
// reconciler's contract for a future persisted swap-in.
func (s *Store) ScanNearExpiry(_ context.Context, cutoff time.Time) ([]CertState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []CertState
	for _, st := range s.certs {
		if !st.NotAfter.After(cutoff) { // NotAfter <= cutoff
			out = append(out, st)
		}
	}
	slices.SortFunc(out, func(a, b CertState) int { return cmp.Compare(a.DeviceID, b.DeviceID) })
	return out, nil
}
