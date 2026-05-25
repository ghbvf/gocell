package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	pgquery "github.com/ghbvf/gocell/pkg/pgquery"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Compile-time interface assertions.
var (
	_ command.Queue         = (*PGCommandQueue)(nil)
	_ command.ActiveScanner = (*PGCommandQueue)(nil)
)

// PGCommandQueue implements kernel/command.Queue + command.ActiveScanner backed
// by PostgreSQL. All mutating methods (Enqueue/Dequeue/Report/Ack/ExtendLease/
// Cancel) self-wrap in a short RunInTx so callers require no ambient transaction.
// When the caller already holds an ambient tx, the inner RunInTx walks savepoint
// semantics via PGTxManager.RunInTx (adapters/postgres/tx_manager.go).
// Read-only methods (ScanActive, GetCommand) run directly against the pool.
//
// Consistency: L4 DeviceLatent — commands traverse the state machine
// Pending→Sent→Delivered→{Succeeded,Failed,Expired,Canceled} through distinct
// Queue method calls. Lease management and timeout detection are the sweeper's
// responsibility; Dequeue does NOT reclaim expired leases inline.
//
// ref: adapters/postgres/outbox_store.go (FOR UPDATE SKIP LOCKED pattern)
// ref: adapters/postgres/tx_manager.go (savepoint / nested RunInTx)
type PGCommandQueue struct {
	db       pgExecutor // routes SQL through ambient tx when present
	pool     *pgxpool.Pool
	txRunner persistence.TxRunner
	clock    clock.Clock
}

// NewCommandQueue constructs a PGCommandQueue backed by pool.
// Returns ErrValidationFailed when any required parameter is nil.
func NewCommandQueue(pool *pgxpool.Pool, txRunner persistence.TxRunner, clk clock.Clock) (*PGCommandQueue, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewCommandQueue: pool must not be nil")
	}
	if validation.IsNilInterface(txRunner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewCommandQueue: txRunner must not be nil")
	}
	if validation.IsNilInterface(clk) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewCommandQueue: clock must not be nil")
	}
	return &PGCommandQueue{
		db:       newPGExecutor(pool),
		pool:     pool,
		txRunner: txRunner,
		clock:    clk,
	}, nil
}

// msgCommandNotFound is the canonical error message for ErrCommandNotFound
// responses. Extracted to satisfy go:S1192 (string used in 5 call sites).
const msgCommandNotFound = "command not found"

// ---------------------------------------------------------------------------
// SQL constants
// ---------------------------------------------------------------------------

// commandSelectCols is the canonical column list for scanning a command row.
// Referenced in Dequeue, ScanActive, GetCommand to stay consistent.
const commandSelectCols = `id, device_id, command_type, payload, metadata, status, attempt,
	created_at, sent_at, delivered_at, completed_at,
	timeouts_schedule_to_send_ns, timeouts_send_to_complete_ns, timeouts_overall_ns`

const dequeueSQL = `
UPDATE commands
   SET status = 2,
       sent_at = $3,
       lease_expiry = $4,
       attempt = attempt + 1
 WHERE id IN (
   SELECT id FROM commands
    WHERE device_id = $1
      AND status = 1
    ORDER BY created_at ASC
    FOR UPDATE SKIP LOCKED
    LIMIT $2
 )
 RETURNING ` + commandSelectCols

// reportSQL advances a command from Sent (status=2) to Delivered (status=3).
// Only Sent→Delivered is allowed; already-Delivered rows produce rows=0 so the
// caller can distinguish idempotent no-op from invalid-state.
const reportSQL = `
UPDATE commands SET status = 3, delivered_at = $2
 WHERE id = $1 AND status = 2
 RETURNING status`

// reportStatusCheckSQL reads the current status for the already-Delivered /
// not-found disambiguation path in Report.
const reportStatusCheckSQL = `SELECT status FROM commands WHERE id = $1`

// selectStatusForUpdateSQL is used by both Ack and Cancel to lock the command
// row and read its current status in a single round-trip before deciding the
// next transition. Merged from the former ackSelectSQL / cancelSelectSQL which
// were identical — one constant, one truth source.
const selectStatusForUpdateSQL = `SELECT status FROM commands WHERE id = $1 FOR UPDATE`

const ackUpdateSQL = `
UPDATE commands SET status = $2, completed_at = $3, lease_expiry = NULL
 WHERE id = $1`

const extendLeaseSQL = `
UPDATE commands SET lease_expiry = $2
 WHERE id = $1 AND lease_expiry IS NOT NULL AND status IN (2, 3)
 RETURNING lease_expiry`

const cancelUpdateSQL = `UPDATE commands SET status = 7, completed_at = $2, lease_expiry = NULL WHERE id = $1`

const existsSQL = `SELECT 1 FROM commands WHERE id = $1`

const scanActiveSQL = `
SELECT ` + commandSelectCols + `
  FROM commands
 WHERE status IN (1, 2, 3)
   AND ($1::TEXT = '' OR device_id = $1)
 ORDER BY created_at ASC`

const getCommandSQL = `SELECT ` + commandSelectCols + ` FROM commands WHERE id = $1`

// enqueueInsertSQL writes a new command row. The `ON CONFLICT … DO NOTHING`
// clause matches migration 031's partial unique index
// `idx_commands_idempotency_key` on `metadata->>'_idempotency_key'` (predicate
// `metadata->>'_idempotency_key' IS NOT NULL`). When a row with the same key
// already exists, PG silently skips the insert — RowsAffected returns 0 and
// the surrounding transaction stays clean. This is the L4 idempotent-no-op
// path; PK collisions (different idempotency context, same id) still raise
// unique_violation and route to ErrConflict in insertEntry.
//
// ref: River queue / Hatchet INSERT … ON CONFLICT DO NOTHING dedup pattern.
const enqueueInsertSQL = `
INSERT INTO commands (
	id, device_id, command_type, payload, metadata, status, attempt,
	created_at,
	timeouts_schedule_to_send_ns, timeouts_send_to_complete_ns, timeouts_overall_ns
) VALUES ($1, $2, $3, $4, $5, 1, 0, $6, $7, $8, $9)
ON CONFLICT ((metadata->>'_idempotency_key'))
   WHERE metadata->>'_idempotency_key' IS NOT NULL
   DO NOTHING`

// ---------------------------------------------------------------------------
// command.Queue implementation
// ---------------------------------------------------------------------------

// Enqueue stores entry in the commands table with status=Pending.
// If opts.Authz is non-nil, it is invoked before the transaction opens.
// If opts.IdempotencyKey is set, an existing entry with the same key is a no-op.
// Duplicate PK returns ErrConflict; unknown device_id returns ErrDeviceNotFound.
// The method self-manages a short transaction; nested calls walk savepoint semantics.
func (q *PGCommandQueue) Enqueue(ctx context.Context, entry command.Entry, opts command.EnqueueOptions) error {
	if opts.Authz != nil {
		if err := opts.Authz(ctx); err != nil {
			return fmt.Errorf("command_queue: authz rejected: %w", err)
		}
	}

	if entry.ID == "" {
		id, err := commandQueueNewID()
		if err != nil {
			return err
		}
		entry.ID = id
	}

	if opts.IdempotencyKey != "" {
		q.stampIdempotencyKey(&entry, opts.IdempotencyKey)
	}

	return q.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		if err := entry.ValidateNew(); err != nil {
			return err
		}
		return q.insertEntry(txCtx, entry)
	})
}

// stampIdempotencyKey writes the idempotency key into entry.Metadata so it is
// persisted in the JSONB column. The DB unique index
// idx_commands_idempotency_key is the atomic dedup gate — no SELECT pre-check
// is needed or performed here (migration 031).
func (q *PGCommandQueue) stampIdempotencyKey(entry *command.Entry, key string) {
	if entry.Metadata == nil {
		entry.Metadata = make(map[string]string)
	}
	entry.Metadata["_idempotency_key"] = key
}

// insertEntry runs the INSERT and translates PG-level errors to typed errcode.
//
// Idempotency dedup is now handled in-SQL via `ON CONFLICT DO NOTHING` against
// the partial unique index `idx_commands_idempotency_key`. When the existing
// row already carries the same idempotency key, PG returns RowsAffected=0
// without raising an error — the surrounding transaction stays clean, no
// savepoint rollback gymnastics. PK collisions still raise unique_violation
// (the ON CONFLICT clause is keyed on the idempotency-key index only) and are
// reported as ErrConflict.
func (q *PGCommandQueue) insertEntry(ctx context.Context, entry command.Entry) error {
	metaBytes, err := marshalMetadata(entry.Metadata)
	if err != nil {
		return err
	}

	tag, insertErr := q.db.Exec(ctx, enqueueInsertSQL,
		entry.ID,
		entry.DeviceID,
		entry.CommandType,
		entry.Payload,
		metaBytes,
		entry.CreatedAt,
		nullableNs(entry.Timeouts.ScheduleToSend),
		nullableNs(entry.Timeouts.SendToComplete),
		nullableNs(entry.Timeouts.OverallDeadline),
	)
	if insertErr == nil {
		// rows=0 → ON CONFLICT (idempotency_key) DO NOTHING fired, idempotent no-op.
		if tag.RowsAffected() == 0 {
			return nil
		}
		return nil
	}
	if pgquery.IsUniqueViolation(insertErr) {
		// PK collision (same id, no matching idempotency key) → ErrConflict.
		return errcode.New(errcode.KindConflict, errcode.ErrConflict,
			"command already exists",
			errcode.WithInternal(fmt.Sprintf("id=%q", entry.ID)))
	}
	if pgquery.IsForeignKeyViolation(insertErr) {
		return errcode.New(errcode.KindNotFound, errcode.ErrDeviceNotFound,
			"device not found",
			errcode.WithDetails(slog.String("deviceId", entry.DeviceID)))
	}
	slog.Error("command_queue: pg write failed",
		slog.String("operation", "insert"),
		slog.String("device_id", entry.DeviceID),
		slog.Any("error", insertErr))
	return fmt.Errorf("command_queue: insert: %w", insertErr)
}

// Dequeue atomically claims up to n Pending entries for deviceID (oldest FIFO),
// advancing them to StatusSent and setting a lease.
// The method self-manages a short transaction; nested calls walk savepoint semantics.
func (q *PGCommandQueue) Dequeue(ctx context.Context, deviceID string, n int, leaseDuration time.Duration) ([]command.Entry, error) {
	if leaseDuration <= 0 {
		leaseDuration = command.DefaultLeaseDuration
	}

	now := q.clock.Now()
	leaseExpiry := now.Add(leaseDuration)

	var result []command.Entry
	err := q.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		rows, err := q.db.Query(txCtx, dequeueSQL, deviceID, n, now, leaseExpiry)
		if err != nil {
			return fmt.Errorf("command_queue: dequeue query: %w", err)
		}
		defer rows.Close()

		var scanErr error
		result, scanErr = scanCommandRows(rows)
		return scanErr
	})
	return result, err
}

// Report advances a command from Sent to Delivered. Idempotent when the command
// is already Delivered (preserves the original delivered_at timestamp).
// Returns ErrCommandNotFound when commandID does not exist.
// The method self-manages a short transaction; nested calls walk savepoint semantics.
func (q *PGCommandQueue) Report(ctx context.Context, commandID string, now time.Time) error {
	return q.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		var gotStatus command.Status
		err := q.db.QueryRow(txCtx, reportSQL, commandID, now).Scan(&gotStatus)
		if err == nil {
			return nil // Sent→Delivered advanced
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("command_queue: report update: %w", err)
		}

		// rows=0: either already Delivered (idempotent), not found, or invalid transition.
		var cur command.Status
		err2 := q.db.QueryRow(txCtx, reportStatusCheckSQL, commandID).Scan(&cur)
		if errors.Is(err2, pgx.ErrNoRows) {
			return errcode.New(errcode.KindNotFound, errcode.ErrCommandNotFound,
				msgCommandNotFound)
		}
		if err2 != nil {
			return fmt.Errorf("command_queue: report status check: %w", err2)
		}
		if cur == command.StatusDelivered {
			return nil // idempotent — delivered_at preserved
		}
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command status does not allow this operation")
	})
}

// Ack finalizes a command in a single transition to a terminal status.
// Same-target idempotent; different-target on terminal returns ErrValidationFailed.
// The method self-manages a short transaction; nested calls walk savepoint semantics.
func (q *PGCommandQueue) Ack(ctx context.Context, commandID string, reason command.AckReason, now time.Time) error {
	if !reason.Valid() {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command_queue: invalid AckReason")
	}
	return q.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		return q.ackInTx(txCtx, commandID, reason.TargetStatus(), now)
	})
}

// ackInTx executes the Ack state-machine check and UPDATE inside an active tx.
// Extracted to keep Ack's cognitive complexity within the project limit (≤15).
func (q *PGCommandQueue) ackInTx(txCtx context.Context, commandID string, target command.Status, now time.Time) error {
	var current command.Status
	err := q.db.QueryRow(txCtx, selectStatusForUpdateSQL, commandID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return errcode.New(errcode.KindNotFound, errcode.ErrCommandNotFound,
			msgCommandNotFound)
	}
	if err != nil {
		return fmt.Errorf("command_queue: ack select: %w", err)
	}

	if current.IsTerminal() {
		if current == target {
			return nil // idempotent same target
		}
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command already in terminal state",
			errcode.WithInternal(fmt.Sprintf("current=%s target=%s", current, target)))
	}

	if err := command.Transition(current, target); err != nil {
		return fmt.Errorf("command_queue: ack: %w", err)
	}
	if _, err := q.db.Exec(txCtx, ackUpdateSQL, commandID, target, now); err != nil {
		return fmt.Errorf("command_queue: ack update: %w", err)
	}
	return nil
}

// ExtendLease renews the lease for a Sent or Delivered command.
// Returns ErrCommandNotFound when not found; ErrValidationFailed when no lease.
// The method self-manages a short transaction; nested calls walk savepoint semantics.
func (q *PGCommandQueue) ExtendLease(ctx context.Context, commandID string, extension time.Duration, now time.Time) error {
	newExpiry := now.Add(extension)

	return q.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		var got time.Time
		err := q.db.QueryRow(txCtx, extendLeaseSQL, commandID, newExpiry).Scan(&got)
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("command_queue: extend lease: %w", err)
		}

		return q.notFoundOrInvalidLease(txCtx, commandID)
	})
}

// Cancel transitions a non-terminal command to StatusCanceled.
// Returns ErrValidationFailed when the command is already terminal.
// The method self-manages a short transaction; nested calls walk savepoint semantics.
func (q *PGCommandQueue) Cancel(ctx context.Context, commandID string, now time.Time) error {
	return q.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		var current command.Status
		err := q.db.QueryRow(txCtx, selectStatusForUpdateSQL, commandID).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			return errcode.New(errcode.KindNotFound, errcode.ErrCommandNotFound,
				msgCommandNotFound)
		}
		if err != nil {
			return fmt.Errorf("command_queue: cancel select: %w", err)
		}

		if current.IsTerminal() {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"command already in terminal state",
				errcode.WithInternal(fmt.Sprintf("current=%s", current)))
		}

		if err := command.Transition(current, command.StatusCanceled); err != nil {
			return fmt.Errorf("command_queue: cancel: %w", err)
		}

		if _, err := q.db.Exec(txCtx, cancelUpdateSQL, commandID, now); err != nil {
			return fmt.Errorf("command_queue: cancel update: %w", err)
		}
		return nil
	})
}

// RepoReady verifies that the commands table is reachable by executing a
// lightweight probe query. Registered as "command_queue_ready" via the
// cellgen-emitted typed helper in the devicecell Init path.
func (q *PGCommandQueue) RepoReady(ctx context.Context) error {
	var dummy int
	err := q.db.QueryRow(ctx, `SELECT 1 FROM commands LIMIT 1`).Scan(&dummy)
	// pgx.ErrNoRows means the table exists but is empty — that is healthy.
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("command_queue: readiness probe: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// command.ActiveScanner implementation
// ---------------------------------------------------------------------------

// ScanActive returns non-terminal entries matching filter, ordered by
// CreatedAt ascending. Does not require an ambient transaction.
func (q *PGCommandQueue) ScanActive(ctx context.Context, filter command.ScanFilter) ([]command.Entry, error) {
	wantStatus := buildStatusAllowlistPG(filter.Statuses)
	if wantStatus != nil && len(wantStatus) == 0 {
		return nil, nil // all requested statuses were terminal
	}

	deviceID := filter.DeviceID

	// Use ambient tx when available; fall back to pool for read-only path.
	var rows pgx.Rows
	var err error
	if tx, ok := persistence.TxFromContext[pgx.Tx](ctx); ok {
		rows, err = tx.Query(ctx, scanActiveSQL, deviceID)
	} else {
		rows, err = q.pool.Query(ctx, scanActiveSQL, deviceID)
	}
	if err != nil {
		return nil, fmt.Errorf("command_queue: scan active: %w", err)
	}
	defer rows.Close()

	all, err := scanCommandRows(rows)
	if err != nil {
		return nil, err
	}

	if wantStatus == nil {
		return all, nil
	}

	// Apply status filter in memory (slice is typically small).
	filtered := all[:0]
	for _, e := range all {
		if slices.Contains(wantStatus, e.Status) {
			filtered = append(filtered, e)
		}
	}
	return filtered, nil
}

// GetCommand returns a single command entry by ID.
// Returns ErrCommandNotFound when not found.
func (q *PGCommandQueue) GetCommand(ctx context.Context, id string) (*command.Entry, error) {
	row := q.db.QueryRow(ctx, getCommandSQL, id)
	e, err := scanCommandRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrCommandNotFound,
			msgCommandNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("command_queue: get command: %w", err)
	}
	return e, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// scanCommandRow scans a single pgx.Row into a command.Entry.
func scanCommandRow(row pgx.Row) (*command.Entry, error) {
	var (
		e              command.Entry
		metaBytes      []byte
		scheduleNs     *int64
		sendCompleteNs *int64
		overallNs      *int64
	)
	err := row.Scan(
		&e.ID, &e.DeviceID, &e.CommandType, &e.Payload, &metaBytes,
		&e.Status, &e.Attempt,
		&e.CreatedAt, &e.SentAt, &e.DeliveredAt, &e.CompletedAt,
		&scheduleNs, &sendCompleteNs, &overallNs,
	)
	if err != nil {
		return nil, err
	}
	if err := unmarshalMetadata(metaBytes, &e.Metadata); err != nil {
		return nil, fmt.Errorf("command_queue: unmarshal metadata: %w", err)
	}
	// Strip internal keys (prefix "_") before returning to callers.
	// They are persisted for dedup but must not be visible outside the adapter.
	stripInternalMetadataKeys(e.Metadata)
	e.Timeouts = command.Timeouts{
		ScheduleToSend:  durationFromNs(scheduleNs),
		SendToComplete:  durationFromNs(sendCompleteNs),
		OverallDeadline: durationFromNs(overallNs),
	}
	return &e, nil
}

// scanCommandRows scans multiple rows from a pgx.Rows into []command.Entry.
func scanCommandRows(rows pgx.Rows) ([]command.Entry, error) {
	var result []command.Entry
	for rows.Next() {
		e, err := scanCommandRow(rows)
		if err != nil {
			return nil, fmt.Errorf("command_queue: scan row: %w", err)
		}
		result = append(result, *e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("command_queue: rows error: %w", err)
	}
	return result, nil
}

// marshalMetadata serializes the metadata map to JSONB bytes.
func marshalMetadata(m map[string]string) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterPGMarshal,
			"command_queue: marshal metadata")
	}
	return b, nil
}

// unmarshalMetadata deserializes JSONB bytes into a map[string]string.
func unmarshalMetadata(b []byte, out *map[string]string) error {
	if len(b) == 0 || string(b) == "{}" {
		return nil
	}
	*out = make(map[string]string)
	return json.Unmarshal(b, out)
}

// nullableNs converts a time.Duration to a *int64 nanosecond value.
// Zero duration maps to nil (no timeout).
func nullableNs(d time.Duration) *int64 {
	if d <= 0 {
		return nil
	}
	n := d.Nanoseconds()
	return &n
}

// durationFromNs converts a nullable int64 nanosecond value to time.Duration.
func durationFromNs(ns *int64) time.Duration {
	if ns == nil {
		return 0
	}
	return time.Duration(*ns)
}

// notFoundOrInvalidLease distinguishes msgCommandNotFound from "no active
// lease" when an ExtendLease UPDATE affected 0 rows.
func (q *PGCommandQueue) notFoundOrInvalidLease(ctx context.Context, commandID string) error {
	var dummy int
	err := q.db.QueryRow(ctx, existsSQL, commandID).Scan(&dummy)
	if errors.Is(err, pgx.ErrNoRows) {
		return errcode.New(errcode.KindNotFound, errcode.ErrCommandNotFound,
			msgCommandNotFound)
	}
	if err != nil {
		return fmt.Errorf("command_queue: lease existence check: %w", err)
	}
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"command has no active lease (pending or terminal state)")
}

// commandQueueNewID generates a random hex-encoded 16-byte ID for entries
// that do not have one set by the caller.
func commandQueueNewID() (string, error) { return newCommandQueueID(rand.Reader) }

func newCommandQueueID(r io.Reader) (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("command_queue: read entropy: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// buildStatusAllowlistPG normalises the caller-supplied status filter to the
// set of non-terminal statuses to match. Returns nil for "all non-terminal";
// returns empty slice when all requested statuses were terminal.
func buildStatusAllowlistPG(in []command.Status) []command.Status {
	if len(in) == 0 {
		return nil
	}
	out := make([]command.Status, 0, len(in))
	for _, s := range in {
		if !s.IsTerminal() {
			out = append(out, s)
		}
	}
	return out
}

// stripInternalMetadataKeys removes all metadata keys that begin with "_" from
// m in-place. These keys (e.g. "_idempotency_key") are adapter-internal
// bookkeeping fields persisted for dedup; they must not be visible to callers
// reading command entries. The raw DB row is preserved; stripping happens only
// in the returned Entry value.
func stripInternalMetadataKeys(m map[string]string) {
	for k := range m {
		if len(k) > 0 && k[0] == '_' {
			delete(m, k)
		}
	}
}
