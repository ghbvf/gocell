// Package commandtest provides in-memory implementations of the command
// package interfaces for use in unit tests and examples.
//
// NOTE: Not suitable for production deployments. Replace with a persistent
// adapter (e.g., adapters/postgres command store) for durable mode. This
// package is importable from non-test code intentionally — the guard against
// production misuse lives at the Cell wiring layer.
package commandtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// duplicate ID sentinel — the same message string is reused in storeIfNotDup
// so it is extracted here to satisfy the ≥3-use constant rule. The MESSAGE
// constant carries no runtime data (MESSAGE-CONST-LITERAL-01 compliant).
const msgCommandAlreadyExists = "commandtest: command already exists"

// commandIDInternalPrefix is the WithInternal detail prefix carrying the
// runtime command ID. Extracted to satisfy go:S1192 (used in 5 lookups).
const commandIDInternalPrefix = "commandID="

// msgCommandNotFound is the static message for not-found errors. The runtime
// command ID is attached via WithInternal so the message itself is a const
// literal (go:S1192 / MESSAGE-CONST-LITERAL-01 compliant).
const msgCommandNotFound = "commandtest: command not found"

// InMemQueue is a process-local, thread-safe implementation of command.Queue,
// command.ActiveScanner, and command.Writer backed by a map.
// It is NOT suitable for multi-replica coordination — use for tests and examples.
//
// Implements:
//   - command.Queue          (Enqueue/Dequeue/Report/Ack/ExtendLease/Cancel)
//   - command.ActiveScanner  (ScanActive/GetCommand)
//   - command.Writer         (WriteCommand — for test seeding)
type InMemQueue struct {
	mu              sync.RWMutex
	entries         map[string]*command.Entry
	leases          map[string]time.Time // commandID → lease expiry
	idempotencyKeys map[string]struct{}  // idempotencyKey → present (O(1) dedup)

	// Now supplies the clock. Defaults to time.Now if nil.
	Now func() time.Time
}

// Compile-time interface checks.
var (
	_ command.Queue         = (*InMemQueue)(nil)
	_ command.ActiveScanner = (*InMemQueue)(nil)
	_ command.Writer        = (*InMemQueue)(nil)
)

// NewInMemQueue creates a new InMemQueue with the default wall clock.
func NewInMemQueue() *InMemQueue {
	return &InMemQueue{
		entries:         make(map[string]*command.Entry),
		leases:          make(map[string]time.Time),
		idempotencyKeys: make(map[string]struct{}),
		Now:             time.Now,
	}
}

func (q *InMemQueue) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}

// newID generates a random hex ID for entries that don't have one.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("commandtest: generate ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ---------------------------------------------------------------------------
// command.Queue implementation
// ---------------------------------------------------------------------------

// Enqueue stores an entry atomically with optional authz and idempotency.
// If entry.ID is empty, a random ID is assigned.
// If opts.Authz is non-nil, it is called before any write; a non-nil return
// rejects the enqueue.
// If opts.IdempotencyKey is non-empty and an entry with that key already
// exists, this is a no-op (idempotent dedup). InMemQueue stores the key
// as metadata["_idempotency_key"].
func (q *InMemQueue) Enqueue(ctx context.Context, entry command.Entry, opts command.EnqueueOptions) error {
	if opts.Authz != nil {
		if err := opts.Authz(ctx); err != nil {
			return fmt.Errorf("commandtest: authz rejected: %w", err)
		}
	}

	// Generate ID before acquiring the lock (crypto/rand outside critical section).
	if entry.ID == "" {
		id, err := newID()
		if err != nil {
			return err
		}
		entry.ID = id
	}

	// Stamp idempotency key into metadata before validation.
	if opts.IdempotencyKey != "" {
		if entry.Metadata == nil {
			entry.Metadata = make(map[string]string)
		}
		entry.Metadata["_idempotency_key"] = opts.IdempotencyKey
	}

	if err := entry.ValidateNew(); err != nil {
		return err
	}

	return q.storeIfNotDup(entry, opts.IdempotencyKey)
}

// storeIfNotDup acquires the write lock, checks for idempotency key dedup
// in O(1) via the idempotencyKeys map, and stores the entry.
// Separated from Enqueue to reduce cognitive complexity.
func (q *InMemQueue) storeIfNotDup(entry command.Entry, idempotencyKey string) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if idempotencyKey != "" {
		if _, exists := q.idempotencyKeys[idempotencyKey]; exists {
			return nil // idempotent no-op
		}
	}

	// Reject duplicate IDs (consistent with PG PK constraint).
	if _, exists := q.entries[entry.ID]; exists {
		return errcode.New(errcode.KindConflict, errcode.ErrConflict,
			msgCommandAlreadyExists,
			errcode.WithInternal(fmt.Sprintf("id=%q", entry.ID)))
	}

	if idempotencyKey != "" {
		q.idempotencyKeys[idempotencyKey] = struct{}{}
	}
	cp := entry
	q.entries[entry.ID] = &cp
	return nil
}

// Dequeue returns up to n Pending entries for targetID, oldest first.
// Each returned entry is advanced to StatusSent (incrementing Attempt) and
// assigned a lease.
func (q *InMemQueue) Dequeue(_ context.Context, targetID string, n int, leaseDuration time.Duration) ([]command.Entry, error) {
	if leaseDuration <= 0 {
		leaseDuration = command.DefaultLeaseDuration
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	now := q.now()

	// Collect and sort pending entries for this device by CreatedAt (FIFO).
	var pending []*command.Entry
	for _, e := range q.entries {
		if e.DeviceID == targetID && e.Status == command.StatusPending {
			pending = append(pending, e)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		return pending[i].CreatedAt.Before(pending[j].CreatedAt)
	})

	if n > len(pending) {
		n = len(pending)
	}
	pending = pending[:n]

	result := make([]command.Entry, 0, len(pending))
	for _, e := range pending {
		if err := command.AdvanceCommand(e, command.StatusSent, now); err != nil {
			continue
		}
		q.leases[e.ID] = now.Add(leaseDuration)
		result = append(result, stripInternalMetaKeys(*e))
	}
	return result, nil
}

// Report advances a command from Sent to Delivered, recording that the device
// has acknowledged receipt and begun execution. No-op (nil) if the command is
// already Delivered (idempotent); returns ErrValidationFailed for any other
// non-Sent status.
func (q *InMemQueue) Report(_ context.Context, commandID string, now time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	e, ok := q.entries[commandID]
	if !ok {
		return errcode.New(errcode.KindNotFound, errcode.ErrCommandNotFound, msgCommandNotFound,
			errcode.WithInternal(commandIDInternalPrefix+commandID))
	}
	if e.Status == command.StatusDelivered {
		return nil // idempotent
	}
	if err := command.AdvanceCommand(e, command.StatusDelivered, now); err != nil {
		return fmt.Errorf("commandtest: report: %w", err)
	}
	return nil
}

// Ack finalizes a command atomically in a single transition step. The AckReason
// maps directly to a terminal status:
//   - AckSuccess:  current → StatusSucceeded
//   - AckFailed:   current → StatusFailed
//   - AckTimeout:  current → StatusExpired (used by Sweeper on deadline elapse)
//   - AckRejected: current → StatusCanceled
//
// No chaining: Ack does NOT advance through intermediate states. Acking from
// StatusSent directly to StatusSucceeded leaves DeliveredAt nil, which is the
// signal that the device skipped the optional Report step.
// Already-terminal entries are idempotent only when the requested reason maps
// to the existing terminal status. A different terminal target is rejected.
func (q *InMemQueue) Ack(_ context.Context, commandID string, reason command.AckReason, now time.Time) error {
	if !reason.Valid() {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "commandtest: invalid AckReason")
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	e, ok := q.entries[commandID]
	if !ok {
		return errcode.New(errcode.KindNotFound, errcode.ErrCommandNotFound, msgCommandNotFound,
			errcode.WithInternal(commandIDInternalPrefix+commandID))
	}

	target := reason.TargetStatus()
	if e.Status.IsTerminal() {
		if e.Status == target {
			return nil
		}
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			fmt.Sprintf("commandtest: command already terminal with status %s; cannot ack as %s", e.Status, target))
	}

	delete(q.leases, commandID)
	if err := command.AdvanceCommand(e, target, now); err != nil {
		return fmt.Errorf("commandtest: advance to %s: %w", target, err)
	}
	return nil
}

// ExtendLease renews the lease for a command.
// Returns ErrNotFound if the command does not exist in the queue.
// Returns ErrValidationFailed (lease expired) if the command exists but its
// lease has expired or was never acquired (e.g. Pending, not yet Dequeued).
func (q *InMemQueue) ExtendLease(_ context.Context, commandID string, extension time.Duration, now time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if _, ok := q.entries[commandID]; !ok {
		return errcode.New(errcode.KindNotFound, errcode.ErrCommandNotFound, msgCommandNotFound,
			errcode.WithInternal(commandIDInternalPrefix+commandID))
	}

	expiry, hasLease := q.leases[commandID]
	if !hasLease || now.After(expiry) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"commandtest: lease expired or not acquired for command: "+commandID)
	}
	q.leases[commandID] = now.Add(extension)
	return nil
}

// Cancel transitions a non-terminal command to StatusCanceled (operator action).
func (q *InMemQueue) Cancel(_ context.Context, commandID string, now time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	e, ok := q.entries[commandID]
	if !ok {
		return errcode.New(errcode.KindNotFound, errcode.ErrCommandNotFound, msgCommandNotFound,
			errcode.WithInternal(commandIDInternalPrefix+commandID))
	}
	if err := command.AdvanceCommand(e, command.StatusCanceled, now); err != nil {
		return fmt.Errorf("commandtest: cancel: %w", err)
	}
	delete(q.leases, commandID)
	// Remove idempotency key so a re-enqueue is allowed after cancel.
	if e.Metadata != nil {
		if ikey, ok := e.Metadata["_idempotency_key"]; ok {
			delete(q.idempotencyKeys, ikey)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// command.ActiveScanner implementation
// ---------------------------------------------------------------------------

// ScanActive returns all non-terminal entries matching filter, ordered by
// CreatedAt ascending. filter.DeviceID="" means scan all devices;
// filter.Statuses=nil means all non-terminal statuses (Pending/Sent/Delivered).
// Terminal statuses in filter.Statuses are silently ignored.
func (q *InMemQueue) ScanActive(_ context.Context, filter command.ScanFilter) ([]command.Entry, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	wantStatus := buildStatusAllowlist(filter.Statuses)

	var result []command.Entry
	for _, e := range q.entries {
		if e.Status.IsTerminal() {
			continue
		}
		if filter.DeviceID != "" && e.DeviceID != filter.DeviceID {
			continue
		}
		if wantStatus != nil && !slices.Contains(wantStatus, e.Status) {
			continue
		}
		result = append(result, stripInternalMetaKeys(*e))
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}

// buildStatusAllowlist normalises the caller-supplied filter to the set of
// non-terminal statuses we actually match against. Returns nil to mean
// "all non-terminal" (cheaper than building a default list).
func buildStatusAllowlist(in []command.Status) []command.Status {
	if len(in) == 0 {
		return nil
	}
	out := make([]command.Status, 0, len(in))
	for _, s := range in {
		if !s.IsTerminal() {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		// All requested statuses were terminal — caller asked for nothing.
		// Return an empty (non-nil) slice so the filter rejects everything.
		return []command.Status{}
	}
	return out
}

// GetCommand returns a single command by ID, or nil if not found.
// Internal metadata keys (prefix "_") are stripped from the returned copy.
func (q *InMemQueue) GetCommand(_ context.Context, id string) (*command.Entry, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	e, ok := q.entries[id]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrCommandNotFound, msgCommandNotFound,
			errcode.WithInternal(commandIDInternalPrefix+id))
	}
	cp := stripInternalMetaKeys(*e)
	return &cp, nil
}

// ---------------------------------------------------------------------------
// command.Writer implementation (test fixture seeding)
// ---------------------------------------------------------------------------

// RepoReady always returns nil for the in-memory queue (no external dependency).
// Satisfies healthz.RepoProber so the same queue value can be registered
// as the "command_queue_ready" readiness probe in demo/test mode.
func (q *InMemQueue) RepoReady(_ context.Context) error { return nil }

// WriteCommand stores an entry directly (bypasses Enqueue validation).
// Used by adapter-level tests that need to seed pre-existing entries.
func (q *InMemQueue) WriteCommand(_ context.Context, entry command.Entry) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	cp := entry
	q.entries[entry.ID] = &cp
	return nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// stripInternalMetaKeys returns a copy of e with all metadata keys whose name
// begins with "_" removed. These are adapter-internal bookkeeping fields (e.g.
// "_idempotency_key") that must not be visible to callers reading entries.
// The stored entry is not mutated.
func stripInternalMetaKeys(e command.Entry) command.Entry {
	if len(e.Metadata) == 0 {
		return e
	}
	stripped := make(map[string]string, len(e.Metadata))
	for k, v := range e.Metadata {
		if len(k) == 0 || k[0] != '_' {
			stripped[k] = v
		}
	}
	if len(stripped) == 0 {
		e.Metadata = nil
	} else {
		e.Metadata = stripped
	}
	return e
}
