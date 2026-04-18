// Package outbox provides the runtime-layer Store interface and relay worker
// for transactional outbox delivery. SQL dialect is intentionally not part of
// this package's surface — adapter packages implement Store against concrete
// backends.
package outbox

import (
	"context"
	"time"

	kout "github.com/ghbvf/gocell/kernel/outbox"
)

// ClaimedEntry extends kernel/outbox.Entry with the Attempts counter that is
// relay-runtime state (not a domain concept).
type ClaimedEntry struct {
	kout.Entry
	Attempts int
}

// Store is the contract between the relay worker and its backing storage.
// Implementations MUST be SQL-dialect-neutral at the interface boundary
// (no *sql.DB / *pgx.Tx in method signatures). Each method opens its own
// short transaction; methods do not compose into a larger transaction.
type Store interface {
	// ClaimPending atomically transitions up to batchSize rows from pending
	// to claiming status, skipping rows locked by other relay instances.
	// Returns an empty slice + nil error when there is nothing to claim.
	ClaimPending(ctx context.Context, batchSize int) ([]ClaimedEntry, error)

	// MarkPublished optimistically transitions an entry from claiming to
	// published. updated=false means the entry was reclaimed by ReclaimStale
	// (not an error).
	MarkPublished(ctx context.Context, id string) (updated bool, err error)

	// MarkRetry transitions a failing entry back to pending with attempts
	// incremented and the supplied nextRetryAt. Relay is responsible for
	// computing nextRetryAt (Go-side backoff). updated=false same as above.
	MarkRetry(ctx context.Context, id string, attempts int, nextRetryAt time.Time, lastError string) (updated bool, err error)

	// MarkDead transitions a failing entry to dead (exceeded max attempts).
	// updated=false same as above.
	MarkDead(ctx context.Context, id string, attempts int, lastError string) (updated bool, err error)

	// ReclaimStale transitions claiming rows whose claimed_at is older than
	// claimTTL back to pending (with attempts+1 and next_retry_at = backoff)
	// or to dead (when attempts+1 >= maxAttempts). Returns count of rows
	// recovered across both destinations.
	ReclaimStale(ctx context.Context, claimTTL time.Duration, maxAttempts int, baseDelay, maxDelay time.Duration) (count int, err error)

	// CleanupPublished deletes a batch of published rows older than cutoff.
	// Caller is responsible for looping until deleted < batchSize.
	CleanupPublished(ctx context.Context, cutoff time.Time, batchSize int) (deleted int, err error)

	// CleanupDead deletes a batch of dead rows older than cutoff.
	CleanupDead(ctx context.Context, cutoff time.Time, batchSize int) (deleted int, err error)

	// OldestEligibleAt returns the oldest published_at (when status="published")
	// or dead_at (when status="dead") in the table. The relay uses this to schedule
	// data-driven cleanup wake-ups: sleep until oldest+retention instead of polling
	// on a fixed interval. Returns ok=false when no rows of the given status exist.
	//
	// status MUST be either "published" or "dead". Implementations should reject
	// other values to keep the contract narrow.
	OldestEligibleAt(ctx context.Context, status string) (at time.Time, ok bool, err error)
}
