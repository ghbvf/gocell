package postgres

import (
	"context"
	"regexp"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// ---------------------------------------------------------------------------
// Error sanitization helpers
// ---------------------------------------------------------------------------

// sensitivePatterns matches common sensitive substrings in error messages
// (connection strings, hostnames, credentials) to redact before storage.
var sensitivePatterns = regexp.MustCompile(
	`(?i)(password|passwd|secret|token|dsn|connection[_ ]?string)=[^\s;,]+`,
)

// truncateError truncates an error message to maxLen runes, preserving valid
// UTF-8 (avoids splitting multi-byte characters at byte boundaries).
func truncateError(msg string, maxLen int) string {
	if utf8.RuneCountInString(msg) <= maxLen {
		return msg
	}
	runes := []rune(msg)
	return string(runes[:maxLen])
}

// sanitizeError truncates and redacts sensitive patterns from an error message
// before storing it in the last_error column.
func sanitizeError(errMsg string, maxLen int) string {
	redacted := sensitivePatterns.ReplaceAllString(errMsg, "$1=<REDACTED>")
	return truncateError(redacted, maxLen)
}
