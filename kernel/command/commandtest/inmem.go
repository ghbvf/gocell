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
	"sort"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// InMemQueue is a process-local, thread-safe implementation of command.Queue,
// command.Reader, command.Writer, and command.StateAdvancer backed by a map.
// It is NOT suitable for multi-replica coordination — use for tests and examples.
//
// Implements:
//   - command.Queue (Enqueue/Dequeue/Ack/ExtendLease/Cancel/ListPending)
//   - command.Reader (PendingCommands/GetCommand)
//   - command.Writer (WriteCommand)
//   - command.StateAdvancer (AdvanceStatus)
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
	_ command.Reader        = (*InMemQueue)(nil)
	_ command.Writer        = (*InMemQueue)(nil)
	_ command.StateAdvancer = (*InMemQueue)(nil)
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
		result = append(result, *e)
	}
	return result, nil
}

// Ack finalises a command. The AckReason determines the target status:
//   - AckSuccess:   Sent→Delivered→Succeeded (or Delivered→Succeeded)
//   - AckFailed:    current→StatusFailed
//   - AckTimeout:   releases lease and calls ResetForRetry (back to Pending)
//   - AckRejected:  current→StatusCanceled
func (q *InMemQueue) Ack(_ context.Context, commandID string, reason command.AckReason, now time.Time) error {
	if !reason.Valid() {
		return errcode.New(errcode.ErrValidationFailed, "commandtest: invalid AckReason")
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	e, ok := q.entries[commandID]
	if !ok {
		return errcode.New(errcode.ErrCommandNotFound, "commandtest: command not found: "+commandID)
	}

	delete(q.leases, commandID)
	return q.applyAck(e, reason, now)
}

// applyAck applies the ack reason transition to the entry (must be called
// with q.mu held). Separated to reduce cognitive complexity of Ack.
func (q *InMemQueue) applyAck(e *command.Entry, reason command.AckReason, now time.Time) error {
	switch reason {
	case command.AckSuccess:
		return q.ackSuccess(e, now)
	case command.AckFailed:
		if err := command.AdvanceCommand(e, command.StatusFailed, now); err != nil {
			return fmt.Errorf("commandtest: advance to Failed: %w", err)
		}
	case command.AckTimeout:
		return q.ackTimeout(e)
	case command.AckRejected:
		if err := command.AdvanceCommand(e, command.StatusCanceled, now); err != nil {
			return fmt.Errorf("commandtest: advance to Canceled: %w", err)
		}
	}
	return nil
}

// ackSuccess advances the entry to Succeeded, first going through Delivered
// if the entry is currently in Sent status.
func (q *InMemQueue) ackSuccess(e *command.Entry, now time.Time) error {
	if e.Status == command.StatusSent {
		if err := command.AdvanceCommand(e, command.StatusDelivered, now); err != nil {
			return fmt.Errorf("commandtest: advance to Delivered: %w", err)
		}
	}
	if err := command.AdvanceCommand(e, command.StatusSucceeded, now); err != nil {
		return fmt.Errorf("commandtest: advance to Succeeded: %w", err)
	}
	return nil
}

// ackTimeout releases the processing lease and resets the entry for retry.
func (q *InMemQueue) ackTimeout(e *command.Entry) error {
	if e.Status == command.StatusPending {
		return errcode.New(errcode.ErrValidationFailed,
			"commandtest: AckTimeout on Pending entry is not allowed; entry is not leased")
	}
	if err := command.ResetForRetry(e); err != nil {
		return fmt.Errorf("commandtest: reset for retry: %w", err)
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
		return errcode.New(errcode.ErrCommandNotFound, "commandtest: command not found: "+commandID)
	}

	expiry, hasLease := q.leases[commandID]
	if !hasLease || now.After(expiry) {
		return errcode.New(errcode.ErrValidationFailed,
			"commandtest: lease expired or not acquired for command: "+commandID)
	}
	q.leases[commandID] = now.Add(extension)
	return nil
}

// Cancel transitions a non-terminal command to StatusCanceled.
func (q *InMemQueue) Cancel(_ context.Context, commandID string, now time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	e, ok := q.entries[commandID]
	if !ok {
		return errcode.New(errcode.ErrCommandNotFound, "commandtest: command not found: "+commandID)
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

// ListPending returns up to limit non-terminal entries (Pending/Sent/Delivered)
// for targetID, ordered by CreatedAt.
func (q *InMemQueue) ListPending(_ context.Context, targetID string, limit int) ([]command.Entry, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	var result []command.Entry
	for _, e := range q.entries {
		if e.DeviceID == targetID && !e.Status.IsTerminal() {
			result = append(result, *e)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// command.Reader implementation
// ---------------------------------------------------------------------------

// PendingCommands returns entries in StatusPending for the given device.
func (q *InMemQueue) PendingCommands(_ context.Context, deviceID string) ([]command.Entry, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	var result []command.Entry
	for _, e := range q.entries {
		if e.DeviceID == deviceID && e.Status == command.StatusPending {
			result = append(result, *e)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}

// GetCommand returns a single command by ID, or nil if not found.
func (q *InMemQueue) GetCommand(_ context.Context, id string) (*command.Entry, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	e, ok := q.entries[id]
	if !ok {
		return nil, nil
	}
	cp := *e
	return &cp, nil
}

// ---------------------------------------------------------------------------
// command.Writer implementation
// ---------------------------------------------------------------------------

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
// command.StateAdvancer implementation
// ---------------------------------------------------------------------------

// AdvanceStatus fetches the entry, checks that current status equals from,
// calls AdvanceCommand to apply side effects, and persists.
func (q *InMemQueue) AdvanceStatus(_ context.Context, id string, from, to command.Status, now time.Time) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	e, ok := q.entries[id]
	if !ok {
		return errcode.New(errcode.ErrCommandNotFound, "commandtest: command not found: "+id)
	}
	if e.Status != from {
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("commandtest: optimistic lock: expected status %s, got %s", from, e.Status))
	}
	return command.AdvanceCommand(e, to, now)
}
