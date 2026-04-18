package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	outboxrt "github.com/ghbvf/gocell/runtime/outbox"
)

// ---------------------------------------------------------------------------
// Outbox entry status constants
// ---------------------------------------------------------------------------

const (
	statusPending   = "pending"   // awaiting publish (including retries)
	statusClaiming  = "claiming"  // locked by a relay instance, publishing in progress
	statusPublished = "published" // successfully delivered to broker
	statusDead      = "dead"      // exceeded MaxAttempts, requires manual intervention
)

// ---------------------------------------------------------------------------
// relayDB — shared DB interface
// ---------------------------------------------------------------------------

// relayDB abstracts the database operations needed by PGOutboxStore and the
// relay layer (previously OutboxRelay). The backing handle is typically a
// *pgxpool.Pool.
type relayDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// sanitizeError is a package-local alias for outboxrt.SanitizeError.
// Adapters/postgres calls this to redact sensitive data before storing error
// messages in the last_error column. The implementation lives in
// runtime/outbox/errors.go to avoid duplication with relay.go.
func sanitizeError(errMsg string, maxLen int) string {
	return outboxrt.SanitizeError(errMsg, maxLen)
}
