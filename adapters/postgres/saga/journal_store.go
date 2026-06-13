package saga

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	kerrors "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/adapters/postgres/saga/internal/pgexec"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// PGJournal implements kernel/saga/journal.Journal over PostgreSQL.
//
// All SQL ops route through pgExecutor (pg_executor.go), which is ambient-tx
// aware via kernel/persistence.TxFromContext. Multi-statement mutations
// (Append / MarkTerminal / ClaimPending) acquire a transaction via
// pgExecutor.acquireTx — joining the ambient tx when one is present
// (Coordinator.commitStep wraps Append + outbox.Emit + RegisterAfterCommit in
// one txRunner.RunInTx; saga MUST join so an Emit failure rolls back the
// journal write together with the outbox row, preserving the L2 OutboxFact
// invariant). When no ambient tx is present, acquireTx opens a fresh one and
// the caller takes ownership of Commit / Rollback.
type PGJournal struct {
	db    pgexec.PGExecutor
	clock clock.Clock
}

// Compile-time assertions: PGJournal satisfies the full Journal and, hence,
// both halves of the JournalCore + Heartbeater split (#1209).
var (
	_ journal.Journal     = (*PGJournal)(nil)
	_ journal.JournalCore = (*PGJournal)(nil)
	_ journal.Heartbeater = (*PGJournal)(nil)
)

// NewJournal constructs a PGJournal backed by pool. clk is required; a nil or
// typed-nil Clock panics via clock.MustHaveClock (programmer error, matching
// the kernel/ wiring convention shared with MemJournal / outbox).
func NewJournal(pool *pgxpool.Pool, clk clock.Clock) (*PGJournal, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga journal: NewJournal requires non-nil pool")
	}
	clock.MustHaveClock(clk, "saga journal: NewJournal requires non-nil Clock")
	return &PGJournal{db: pgexec.New(pool), clock: clk}, nil
}

// ---------------------------------------------------------------------------
// SQL constants
// ---------------------------------------------------------------------------

// enqueueQuery inserts a fresh saga instance. ON CONFLICT (id) DO NOTHING +
// RowsAffected==0 detection lets us surface duplicate enqueue as a typed
// error without a second SELECT round-trip.
const enqueueQuery = `INSERT INTO saga_instances
	(id, definition_id, status, current_version, started_at, updated_at)
	VALUES ($1, $2, $3, 0, $4, $5)
	ON CONFLICT (id) DO NOTHING`

// selectInstanceForUpdate locks the row under FOR UPDATE so concurrent
// Append / MarkTerminal serialize on a single instance while letting different
// instances proceed in parallel.
const selectInstanceForUpdate = `SELECT status, current_version, lease_id, lease_expires_at,
	started_at, updated_at
	FROM saga_instances WHERE id = $1 FOR UPDATE`

// updateInstanceAfterAppend bumps current_version, flips status, and stamps
// updated_at. Defense-in-depth: the WHERE clause CAS-fences on lease_id so a
// future refactor that drops the selectInstanceForUpdate FOR UPDATE lock
// still cannot land an Append against a rotated lease (mirrors the outbox
// store's CAS shape, OUTBOX-LEASE-ID-CAS-01).
//
// $1 newStatus, $2 now, $3 instanceID, $4 leaseID.
const updateInstanceAfterAppend = `UPDATE saga_instances
	SET current_version = current_version + 1, status = $1, updated_at = $2
	WHERE id = $3 AND lease_id = $4`

// updateInstanceTerminal flips status to a terminal value, releases the lease
// (both lease columns NULL), advances current_version (the terminal event
// gets the new version stamped in saga_events; the projection must stay in
// lock-step or replay-from-projection paths see a stale version counter),
// and stamps updated_at. CAS-fenced on lease_id (same defense-in-depth
// rationale as updateInstanceAfterAppend).
//
// $1 finalStatus, $2 now, $3 instanceID, $4 leaseID.
const updateInstanceTerminal = `UPDATE saga_instances
	SET status = $1, current_version = current_version + 1,
		lease_id = NULL, lease_expires_at = NULL, updated_at = $2
	WHERE id = $3 AND lease_id = $4`

// insertEvent appends one row to saga_events. PK (instance_id, version)
// enforces monotonicity; a duplicate (instance_id, version) is a programmer
// error (we always insert current_version+1 immediately after UPDATE within
// the same tx).
const insertEvent = `INSERT INTO saga_events
	(instance_id, version, kind, step_name, payload, created_at)
	VALUES ($1, $2, $3, $4, $5, $6)`

// selectEvents returns the full ordered event history for an instance.
const selectEvents = `SELECT version, kind, step_name, payload, created_at
	FROM saga_events WHERE instance_id = $1 ORDER BY version`

// instanceExistsQuery distinguishes "never enqueued" (KindNotFound) from
// "enqueued, no events yet" (empty slice) when Load returns zero rows.
const instanceExistsQuery = `SELECT 1 FROM saga_instances WHERE id = $1`

// claimPendingQuery materializes eligible non-terminal rows under FOR UPDATE
// SKIP LOCKED, then stamps them with the freshly minted batch lease. Mirrors
// adapters/postgres outbox claimPendingQuery shape.
//
// The `now` value is injected (s.clock.Now()) rather than read from PG's
// now() so the FakeClock-driven sagajournaltest conformance suite controls
// lease arithmetic deterministically. In production the injected clock is
// the real one, so there is no behavioral divergence vs PG now().
//
// $1 leaseID (text), $2 lease window (interval), $3 batchSize,
// $4 now (timestamptz).
//
// current_version is intentionally NOT in the RETURNING list:
// kernel/saga/journal.Journal contract says callers rebuild step cursor by
// folding Load output, so the projection's version counter is opaque to
// ClaimPending consumers (mirrors memjournal which exposes no currentVersion
// in ClaimedInstance).
//
// Lease-expiry boundary semantics: memjournal's fenced check is `!Before(now)`
// — at exact equality the lease is STILL VALID. SQL aligns: claim eligibility
// uses strict `< $now` so a lease whose expires_at equals now is NOT reclaim-
// eligible (matching the fenced "valid" verdict from the journal Go side).
const claimPendingQuery = `WITH picked AS MATERIALIZED (
		SELECT id, started_at
		FROM saga_instances
		WHERE status IN (1, 2, 3)
			AND (lease_id IS NULL OR lease_expires_at < $4::timestamptz)
		ORDER BY started_at, id
		LIMIT $3
		FOR UPDATE SKIP LOCKED
	),
	updated AS (
		UPDATE saga_instances AS si
		SET lease_id = $1,
			lease_expires_at = $4::timestamptz + $2
		FROM picked
		WHERE si.id = picked.id
		RETURNING si.id, si.definition_id, si.status,
			si.started_at, si.updated_at,
			picked.started_at AS picked_started_at
	)
	SELECT id, definition_id, status, started_at, updated_at
	FROM updated
	ORDER BY picked_started_at, id`

// heartbeatQuery extends an active lease. CAS-fenced on (id, lease_id,
// lease_expires_at >= $now); RowsAffected==0 signals lost lease. The `now`
// parameter source is the injected clock (see claimPendingQuery rationale).
//
// Boundary semantics: `>= $now` keeps heartbeat consistent with memjournal's
// fenced ("lease valid at exact equality"). Strict `> $now` would race the
// claim predicate `< $now` and leave an instant where both reject.
//
// $1 lease window (interval), $2 instance id, $3 lease id,
// $4 now (timestamptz).
const heartbeatQuery = `UPDATE saga_instances
	SET lease_expires_at = $4::timestamptz + $1
	WHERE id = $2 AND lease_id = $3 AND lease_expires_at >= $4::timestamptz`

// sagaEventsGlobalAppendLockKey is the pg_advisory_xact_lock key that
// serializes saga_events INSERTs across all saga instances so that the
// BIGINT IDENTITY global_seq value order equals commit order.
//
// Why this is necessary: PostgreSQL IDENTITY sequences allocate the next
// value at INSERT time, not at COMMIT time. Without serialization, two
// concurrent transactions can be assigned seq=5 and seq=6 respectively,
// but commit in the opposite order. A tailer that delivered seq=6 and
// checkpointed to 6 will skip seq=5 permanently (WHERE global_seq > 6
// excludes it), silently losing the earlier event (F1 in PR #1630).
//
// Lock-ordering invariant: every Append / MarkTerminal first acquires the
// per-instance FOR UPDATE lock (selectInstanceForUpdate) and THEN acquires
// this single global advisory key. Because each transaction touches exactly
// one saga instance, no transaction can already hold another instance's row
// lock when it requests the advisory key, so the single shared key cannot
// produce a deadlock cycle. The advisory key is xact-scoped
// (pg_advisory_xact_lock), so it is released automatically at the end of
// the transaction without an explicit unlock call.
//
// Throughput note: serialized appends across all instances is the minimal
// correct fix for commit-ordered global_seq. A safe-lag committed-prefix
// reader that relaxes this serialization without losing events is tracked
// in issue #2070.
const sagaEventsGlobalAppendLockKey int64 = 0x5341474145565400

// advisoryLockSQL acquires a transaction-scoped advisory lock on the given
// int64 key. The lock is held until the surrounding transaction commits or
// rolls back; no explicit unlock is needed or possible.
const advisoryLockSQL = "SELECT pg_advisory_xact_lock($1)"

// repoReadyQuery probes BOTH saga relations so schema/migration drift on
// either table surfaces independently of pool-level health. UNION ALL with
// WHERE false plans both relations without reading any row.
const repoReadyQuery = `SELECT 1 FROM saga_instances WHERE false
	UNION ALL
	SELECT 1 FROM saga_events WHERE false`

// ---------------------------------------------------------------------------
// Journal interface implementation
// ---------------------------------------------------------------------------

// Enqueue inserts a Pending instance. Duplicate IDs surface as
// errcode.ErrSagaDuplicateInstance (KindConflict).
func (s *PGJournal) Enqueue(ctx context.Context, instance saga.Instance) error {
	if err := instance.ValidateNew(); err != nil {
		return err
	}
	now := s.clock.Now()
	ct, err := s.db.Exec(ctx, enqueueQuery,
		string(instance.ID),
		string(instance.DefinitionID),
		statusToInt16(instance.Status),
		instance.StartedAt,
		now,
	)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: Enqueue failed", err)
	}
	if ct.RowsAffected() == 0 {
		return journal.NewDuplicateInstanceError(instance.ID)
	}
	return nil
}

// Append acquires a transaction (joining the ambient tx if present), reads
// the projection (FOR UPDATE), checks the lease fence, validates the event,
// advances the status via saga.AdvanceSaga, bumps current_version, and
// inserts the event row. Returns the assigned version on success.
//
// Error precedence matches memjournal: instance-existence and lease fence are
// checked BEFORE Event.ValidateForAppend so a bad payload on an unknown
// instance returns ErrSagaNotFound (not ErrValidationFailed). Conformance
// suite locks this with Append_UnknownInstanceWithBadPayload_PrefersNotFound.
//
// Commit-order correctness (F1, #1630): immediately before the insertEvent
// INSERT this method acquires sagaEventsGlobalAppendLockKey via
// pg_advisory_xact_lock. The xact-scoped lock serializes all saga_events
// INSERTs so that the IDENTITY-assigned global_seq order equals commit order
// (a tailer advancing its cursor by delivered global_seq will never
// permanently skip a lower seq that committed later). Lock-ordering: per-
// instance FOR UPDATE is acquired first, then the single global advisory key
// in that consistent order across every transaction; no deadlock is possible
// because each transaction holds at most one instance row lock before
// requesting the shared advisory key. The throughput tradeoff (serialized
// appends) is replaced by a safe-lag committed-prefix reader in #2070.
func (s *PGJournal) Append(ctx context.Context, instanceID, leaseID idutil.SafeID, event journal.Event) (int64, error) {
	tx, owned, err := s.db.AcquireTx(ctx)
	if err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGConnect,
			"saga journal: Append acquireTx", err)
	}
	committed := !owned // ambient tx: caller owns lifecycle; we don't Commit/Rollback
	defer func() {
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	row, err := selectInstanceLocked(ctx, tx, instanceID)
	if err != nil {
		if errors.Is(err, errInstanceMissingSentinel) {
			return 0, journal.NewInstanceNotFoundError(instanceID)
		}
		return 0, err
	}

	now := s.clock.Now()
	if !fenced(row, leaseID, now) {
		return 0, journal.NewStaleLeaseError(instanceID, leaseID)
	}

	// Validate after fence so unknown-instance / stale-lease take precedence
	// over event-shape errors (memjournal parity).
	if err := event.ValidateForAppend(); err != nil {
		return 0, err
	}

	// Build a working saga.Instance for AdvanceSaga's transition gate; only
	// Status / StartedAt / UpdatedAt influence the validation.
	inst := saga.Instance{
		ID:        instanceID,
		Status:    row.status,
		StartedAt: row.startedAt,
		UpdatedAt: &row.updatedAt,
	}
	newStatus, err := projectAppend(&inst, event.Kind, now)
	if err != nil {
		return 0, err
	}

	if _, err := tx.Exec(ctx, updateInstanceAfterAppend,
		statusToInt16(newStatus), now, string(instanceID), string(leaseID),
	); err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: Append update projection failed", err)
	}

	// Serialize saga_events INSERTs so global_seq order == commit order (F1).
	// See sagaEventsGlobalAppendLockKey for the full rationale.
	if _, err := tx.Exec(ctx, advisoryLockSQL, sagaEventsGlobalAppendLockKey); err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: Append advisory lock", err)
	}

	version := row.currentVersion + 1
	if _, err := tx.Exec(ctx, insertEvent,
		string(instanceID), version, kindToInt16(event.Kind),
		nullableStepName(event.StepName), event.Payload, now,
	); err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: Append insert event failed", err)
	}

	if owned {
		if err := tx.Commit(ctx); err != nil {
			return 0, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGConnect,
				"saga journal: Append commit failed", err)
		}
		committed = true
	}
	return version, nil
}

// Load returns the full ordered event history. A never-enqueued instance
// returns errcode.ErrSagaNotFound (KindNotFound); an enqueued instance with
// no events yet returns an empty non-nil slice.
func (s *PGJournal) Load(ctx context.Context, instanceID idutil.SafeID) ([]journal.Event, error) {
	rows, err := s.db.Query(ctx, selectEvents, string(instanceID))
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: Load query failed", err)
	}
	defer rows.Close()

	events := make([]journal.Event, 0)
	for rows.Next() {
		var (
			version  int64
			rawKind  int16
			stepName *string
			payload  []byte
			created  time.Time
		)
		if err := rows.Scan(&version, &rawKind, &stepName, &payload, &created); err != nil {
			return nil, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
				"saga journal: Load scan failed", err)
		}
		kind, kindErr := kindFromInt16(rawKind)
		if kindErr != nil {
			// Schema-shape error: persisted row contains a kind value the
			// application enum does not recognize (migration drift or
			// out-of-band write). Surface in slog so ops can correlate the
			// row identity even when the upstream caller only sees the
			// wrapped errcode.
			slog.Error("saga journal: Load encountered invalid event kind",
				slog.String("instance_id", string(instanceID)),
				slog.Int64("version", version),
				slog.Int("raw_kind", int(rawKind)),
				slog.Any("error", kindErr),
			)
			return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrPGSchemaShape,
				"saga journal: Load encountered invalid event kind", kindErr)
		}
		var step idutil.SafeID
		if stepName != nil {
			step = idutil.SafeID(*stepName)
		}
		events = append(events, journal.Event{
			Version:   version,
			Kind:      kind,
			StepName:  step,
			Payload:   payload,
			CreatedAt: created,
		})
	}
	if rows.Err() != nil {
		return nil, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: Load iteration failed", rows.Err())
	}
	if len(events) == 0 {
		// Distinguish never-enqueued from enqueued-but-no-events by an extra
		// existence probe; only done on the empty path so the happy path
		// stays at one query.
		var one int
		err := s.db.QueryRow(ctx, instanceExistsQuery, string(instanceID)).Scan(&one)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil, journal.NewInstanceNotFoundError(instanceID)
		case err != nil:
			return nil, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
				"saga journal: Load existence check failed", err)
		}
	}
	return events, nil
}

// ClaimPending leases up to batchSize claimable instances under a fresh batch
// UUID, returning them stamped with the lease the caller MUST echo on every
// lease-fenced mutation for each claimed instance.
func (s *PGJournal) ClaimPending(
	ctx context.Context, batchSize int, leaseDuration time.Duration,
) ([]journal.ClaimedInstance, idutil.SafeID, error) {
	if batchSize <= 0 {
		return nil, "", journal.NewNonPositiveBatchSizeError(batchSize)
	}
	if leaseDuration <= 0 {
		return nil, "", journal.NewNonPositiveLeaseDurationError(leaseDuration)
	}

	leaseStr := uuid.NewString()
	// Typed interval (pgx encodes the OID) rather than string-concatenated
	// `($n || ' microseconds')::interval` — keeps the lease window inside pgx's
	// type system. (make_interval has no microsecs arg, so pgtype.Interval is the
	// exact-microsecond route.)
	leaseWindow := pgtype.Interval{Microseconds: leaseDuration.Microseconds(), Valid: true}
	now := s.clock.Now()

	rows, err := s.db.Query(ctx, claimPendingQuery, leaseStr, leaseWindow, batchSize, now)
	if err != nil {
		return nil, "", errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: ClaimPending query failed", err)
	}
	defer rows.Close()

	var claimed []journal.ClaimedInstance
	for rows.Next() {
		var (
			id           string
			definitionID string
			rawStatus    int16
			startedAt    time.Time
			updatedAt    time.Time
		)
		if err := rows.Scan(&id, &definitionID, &rawStatus, &startedAt, &updatedAt); err != nil {
			return nil, "", errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
				"saga journal: ClaimPending scan failed", err)
		}
		status, sErr := statusFromInt16(rawStatus)
		if sErr != nil {
			slog.Error("saga journal: ClaimPending encountered invalid status",
				slog.String("instance_id", id),
				slog.Int("raw_status", int(rawStatus)),
				slog.Any("error", sErr),
			)
			return nil, "", errcode.Wrap(errcode.KindInternal, errcode.ErrPGSchemaShape,
				"saga journal: ClaimPending encountered invalid status", sErr)
		}
		// updatedAt is non-nullable in saga_instances; copy it for the projection.
		ua := updatedAt
		claimed = append(claimed, journal.ClaimedInstance{
			Instance: saga.Instance{
				ID:           idutil.SafeID(id),
				DefinitionID: idutil.SafeID(definitionID),
				Status:       status,
				StartedAt:    startedAt,
				UpdatedAt:    &ua,
			},
			LeaseID: idutil.SafeID(leaseStr),
		})
	}
	if rows.Err() != nil {
		return nil, "", errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: ClaimPending iteration failed", rows.Err())
	}
	if len(claimed) == 0 {
		return nil, "", nil
	}
	return claimed, idutil.SafeID(leaseStr), nil
}

// Heartbeat extends an active lease. Returns (false, nil) on stale lease /
// missing instance — distinguishing the two would leak existence to a zombie
// leader, so the silent semantic is deliberate.
func (s *PGJournal) Heartbeat(ctx context.Context, instanceID, leaseID idutil.SafeID, leaseDuration time.Duration) (bool, error) {
	if leaseDuration <= 0 {
		return false, journal.NewNonPositiveLeaseDurationError(leaseDuration)
	}
	leaseWindow := pgtype.Interval{Microseconds: leaseDuration.Microseconds(), Valid: true}
	now := s.clock.Now()
	ct, err := s.db.Exec(ctx, heartbeatQuery, leaseWindow, string(instanceID), string(leaseID), now)
	if err != nil {
		return false, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: Heartbeat failed", err)
	}
	return ct.RowsAffected() == 1, nil
}

// MarkTerminal transitions the instance to finalStatus and appends the
// matching terminal event atomically. Joins the ambient tx when one is
// present (Coordinator wraps commitStep in RunInTx). (false, nil) on stale
// lease / missing instance; KindInvalid on a non-terminal finalStatus or
// illegal transition.
//
// Commit-order correctness (F1, #1630): same advisory-lock protocol as
// Append — acquires sagaEventsGlobalAppendLockKey immediately before the
// insertEvent INSERT so global_seq order equals commit order. See Append
// godoc and sagaEventsGlobalAppendLockKey for the full rationale.
func (s *PGJournal) MarkTerminal(ctx context.Context, instanceID, leaseID idutil.SafeID, finalStatus saga.Status) (bool, error) {
	tx, owned, err := s.db.AcquireTx(ctx)
	if err != nil {
		return false, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGConnect,
			"saga journal: MarkTerminal acquireTx", err)
	}
	committed := !owned
	defer func() {
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	row, err := selectInstanceLocked(ctx, tx, instanceID)
	if err != nil {
		if errors.Is(err, errInstanceMissingSentinel) {
			return false, nil // silent: matches memjournal
		}
		return false, err
	}

	now := s.clock.Now()
	if !fenced(row, leaseID, now) {
		return false, nil
	}

	if !finalStatus.IsTerminal() {
		return false, journal.NewInvalidTerminalStatusError(instanceID, row.status, finalStatus)
	}
	termKind, okKind := journal.TerminalEventKind(finalStatus)
	if !okKind {
		return false, journal.NewInvalidTerminalStatusError(instanceID, row.status, finalStatus)
	}

	inst := saga.Instance{
		ID:        instanceID,
		Status:    row.status,
		StartedAt: row.startedAt,
		UpdatedAt: &row.updatedAt,
	}
	if err := saga.AdvanceSaga(&inst, finalStatus, now); err != nil {
		return false, err
	}

	if err := markTerminalWrites(ctx, tx, instanceID, leaseID, finalStatus, termKind, row.currentVersion+1, now); err != nil {
		return false, err
	}

	if owned {
		if err := tx.Commit(ctx); err != nil {
			return false, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGConnect,
				"saga journal: MarkTerminal commit failed", err)
		}
		committed = true
	}
	return true, nil
}

// markTerminalWrites performs the terminal projection update plus the
// advisory-locked terminal event append inside the caller's tx. Split out of
// MarkTerminal to keep that method under the cognitive-complexity budget; the
// commit-order protocol (F1, #1630) is unchanged — sagaEventsGlobalAppendLockKey
// is still acquired immediately before the insertEvent INSERT.
func markTerminalWrites(
	ctx context.Context, tx pgx.Tx, instanceID, leaseID idutil.SafeID,
	finalStatus saga.Status, termKind journal.EventKind, version int64, now time.Time,
) error {
	if _, err := tx.Exec(ctx, updateInstanceTerminal,
		statusToInt16(finalStatus), now, string(instanceID), string(leaseID),
	); err != nil {
		return errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: MarkTerminal update failed", err)
	}

	// Serialize saga_events INSERTs so global_seq order == commit order (F1).
	// See sagaEventsGlobalAppendLockKey for the full rationale.
	if _, err := tx.Exec(ctx, advisoryLockSQL, sagaEventsGlobalAppendLockKey); err != nil {
		return errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: MarkTerminal advisory lock", err)
	}

	if _, err := tx.Exec(ctx, insertEvent,
		string(instanceID), version, kindToInt16(termKind),
		nullableStepName(""), nil, now,
	); err != nil {
		return errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: MarkTerminal insert event failed", err)
	}
	return nil
}

// RepoReady probes the saga_instances relation so schema/migration drift
// surfaces independently from pool-level health.
func (s *PGJournal) RepoReady(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, repoReadyQuery); err != nil {
		return errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: RepoReady probe failed", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Private helpers
// ---------------------------------------------------------------------------

// instanceRow holds the columns selectInstanceLocked returns. Defined here so
// Append and MarkTerminal share the scan shape without re-typing.
type instanceRow struct {
	status         saga.Status
	currentVersion int64
	leaseID        string
	leaseExpiresAt time.Time
	leaseValid     bool // false when both lease columns are NULL
	startedAt      time.Time
	updatedAt      time.Time
}

// errInstanceMissingSentinel signals that selectInstanceLocked saw no rows.
// MarkTerminal converts this to (false, nil); Append converts it to a typed
// journal.NewInstanceNotFoundError. Internal to this package — never returned
// through the Journal surface.
var errInstanceMissingSentinel = errors.New("saga journal: instance missing")

// selectInstanceLocked returns the instance projection under FOR UPDATE
// lock. errInstanceMissingSentinel signals "no such row"; callers translate
// (Append → journal.NewInstanceNotFoundError, MarkTerminal → silent (false, nil)).
func selectInstanceLocked(ctx context.Context, tx pgx.Tx, instanceID idutil.SafeID) (instanceRow, error) {
	var (
		out         instanceRow
		rawStatus   int16
		leaseStr    *string
		leaseExpiry *time.Time
	)
	err := tx.QueryRow(ctx, selectInstanceForUpdate, string(instanceID)).Scan(
		&rawStatus, &out.currentVersion, &leaseStr, &leaseExpiry,
		&out.startedAt, &out.updatedAt,
	)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return instanceRow{}, errInstanceMissingSentinel
	case err != nil:
		return instanceRow{}, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: select instance failed", err)
	}
	status, sErr := statusFromInt16(rawStatus)
	if sErr != nil {
		slog.Error("saga journal: instance row has invalid status",
			slog.String("instance_id", string(instanceID)),
			slog.Int("raw_status", int(rawStatus)),
			slog.Any("error", sErr),
		)
		return instanceRow{}, errcode.Wrap(errcode.KindInternal, errcode.ErrPGSchemaShape,
			"saga journal: instance row has invalid status", sErr)
	}
	out.status = status
	if leaseStr != nil && leaseExpiry != nil {
		out.leaseID = *leaseStr
		out.leaseExpiresAt = *leaseExpiry
		out.leaseValid = true
	}
	return out, nil
}

func fenced(row instanceRow, leaseID idutil.SafeID, now time.Time) bool {
	if !row.leaseValid {
		return false
	}
	if row.leaseID != string(leaseID) {
		return false
	}
	return !row.leaseExpiresAt.Before(now)
}

// projectAppend mirrors memjournal.applyProjection: it computes the new
// status that this event kind implies (or the existing status when the kind
// is a no-op for the current status) and returns it. Illegal phase / kind
// combinations return a journal-package error (KindInvalid).
func projectAppend(inst *saga.Instance, kind journal.EventKind, now time.Time) (saga.Status, error) {
	st := inst.Status
	switch kind {
	case journal.KindStepStarted, journal.KindStepCompleted, journal.KindStepFailed:
		switch st {
		case saga.StatusPending:
			if err := saga.AdvanceSaga(inst, saga.StatusRunning, now); err != nil {
				return 0, err
			}
			return saga.StatusRunning, nil
		case saga.StatusRunning:
			return st, nil
		default:
			return 0, journal.NewEventPhaseError(inst.ID, kind, st)
		}
	case journal.KindCompensationStarted:
		if err := saga.AdvanceSaga(inst, saga.StatusCompensating, now); err != nil {
			return 0, err
		}
		return saga.StatusCompensating, nil
	case journal.KindStepCompensated, journal.KindStepCompensationFailed:
		// Compensate-phase per-step outcomes — both legal only while
		// Compensating, status unchanged. KindStepCompensationFailed
		// (#1181) is the failure variant; see memjournal.applyProjection
		// for the parallel mem-side branch.
		if st != saga.StatusCompensating {
			return 0, journal.NewEventPhaseError(inst.ID, kind, st)
		}
		return st, nil
	default:
		return st, nil
	}
}

// statusToInt16 maps a saga.Status to the SMALLINT wire value. iota+1 is
// already the wire value; we add a Valid gate so an out-of-range status is
// caught at write time rather than silently persisted.
func statusToInt16(s saga.Status) int16 {
	if !s.Valid() {
		// Programmer error: every persisted status must be Valid. We surface
		// as an out-of-range int16 so the DB CHECK constraint
		// (saga_instances_status_range) rejects on insert/update, fail-closed.
		return 0
	}
	return int16(s)
}

func statusFromInt16(v int16) (saga.Status, error) {
	// Reject negatives and >255 before narrowing int16→uint8 so out-of-band
	// values are rejected as a schema-shape error rather than silently
	// wrapping. v=0 and 8..255 fall through to s.Valid() below.
	if v < 0 || v > 255 {
		return 0, fmt.Errorf("status %d out of uint8 range", v)
	}
	s := saga.Status(uint8(v))
	if !s.Valid() {
		return 0, fmt.Errorf("status %d out of range", v)
	}
	return s, nil
}

func kindToInt16(k journal.EventKind) int16 {
	if !k.Valid() {
		return 0
	}
	return int16(k)
}

func kindFromInt16(v int16) (journal.EventKind, error) {
	if v < 0 || v > 255 {
		return 0, fmt.Errorf("event kind %d out of uint8 range", v)
	}
	k := journal.EventKind(uint8(v))
	if !k.Valid() {
		return 0, fmt.Errorf("event kind %d out of range", v)
	}
	return k, nil
}

// nullableStepName returns nil for the empty step name (saga-scoped events:
// CompensationStarted / terminals); otherwise the string value. PG stores
// SQL NULL for nil and a value otherwise.
func nullableStepName(s idutil.SafeID) any {
	if s == "" {
		return nil
	}
	return string(s)
}

// Error constructors live in kernel/saga/journal/errors.go (exported as
// NewXxxError helpers) and are reused at PG callsites verbatim — DRY per
// PR-04 review carry-over.
