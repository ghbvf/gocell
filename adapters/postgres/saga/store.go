package saga

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	kerrors "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// DB abstracts the database operations PGJournal needs. The backing handle is
// typically a *pgxpool.Pool; tests can substitute a thin fake satisfying this
// interface. Mirrors the relayDB pattern in adapters/postgres/outbox_db.go but
// scoped to this subpackage so the parent package's relayDB stays internal.
type DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// PGJournal implements kernel/saga/journal.Journal over PostgreSQL.
//
// Each lease-fenced mutation (Append / MarkTerminal) opens its own short
// transaction that combines the lease check, projection update, and event
// insert atomically. ClaimPending uses a CTE with FOR UPDATE SKIP LOCKED so
// concurrent claimers see disjoint candidate sets. Heartbeat is a single
// UPDATE under lease_id CAS.
type PGJournal struct {
	db    DB
	clock clock.Clock
}

// Compile-time assertion.
var _ journal.Journal = (*PGJournal)(nil)

// NewJournal constructs a PGJournal backed by db. clk is required; a nil or
// typed-nil Clock panics via clock.MustHaveClock (programmer error, matching
// the kernel/ wiring convention shared with MemJournal / outbox).
func NewJournal(db DB, clk clock.Clock) (*PGJournal, error) {
	if db == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"saga journal: NewJournal requires non-nil DB handle")
	}
	clock.MustHaveClock(clk, "saga journal: NewJournal requires non-nil Clock")
	return &PGJournal{db: db, clock: clk}, nil
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
// updated_at. It does NOT re-check the lease — the caller is required to hold
// the row's FOR UPDATE lock when calling, so the lease state read at
// selectInstanceForUpdate cannot have rotated.
const updateInstanceAfterAppend = `UPDATE saga_instances
	SET current_version = current_version + 1, status = $1, updated_at = $2
	WHERE id = $3`

// updateInstanceTerminal flips status to a terminal value, releases the lease
// (both lease columns NULL), and stamps updated_at. CAS-fenced on lease_id so
// a tx that did not hold the lock at SELECT time cannot land it.
const updateInstanceTerminal = `UPDATE saga_instances
	SET status = $1, lease_id = NULL, lease_expires_at = NULL, updated_at = $2
	WHERE id = $3`

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
// $1 leaseID (text), $2 lease window microseconds (text), $3 batchSize,
// $4 now (timestamptz).
const claimPendingQuery = `WITH picked AS MATERIALIZED (
		SELECT id, started_at
		FROM saga_instances
		WHERE status IN (1, 2, 3)
			AND (lease_id IS NULL OR lease_expires_at <= $4::timestamptz)
		ORDER BY started_at, id
		LIMIT $3
		FOR UPDATE SKIP LOCKED
	),
	updated AS (
		UPDATE saga_instances AS si
		SET lease_id = $1,
			lease_expires_at = $4::timestamptz + ($2 || ' microseconds')::interval
		FROM picked
		WHERE si.id = picked.id
		RETURNING si.id, si.definition_id, si.status, si.current_version,
			si.started_at, si.updated_at,
			picked.started_at AS picked_started_at
	)
	SELECT id, definition_id, status, current_version, started_at, updated_at
	FROM updated
	ORDER BY picked_started_at, id`

// heartbeatQuery extends an active lease. CAS-fenced on (id, lease_id, not
// expired); RowsAffected==0 signals lost lease. The `now` parameter source is
// the injected clock (see claimPendingQuery rationale).
//
// $1 lease window microseconds (text), $2 instance id, $3 lease id,
// $4 now (timestamptz).
const heartbeatQuery = `UPDATE saga_instances
	SET lease_expires_at = $4::timestamptz + ($1 || ' microseconds')::interval
	WHERE id = $2 AND lease_id = $3 AND lease_expires_at > $4::timestamptz`

// repoReadyQuery is a cost-free probe: parses + plans against the
// saga_instances relation but never reads a row. Mirrors session_store.go.
const repoReadyQuery = `SELECT 1 FROM saga_instances WHERE false`

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
		return errDuplicateInstance(instance.ID)
	}
	return nil
}

// Append validates the event, then under one transaction reads the projection
// (FOR UPDATE), checks the lease fence, advances the status via
// saga.AdvanceSaga, bumps current_version, and inserts the event row. Returns
// the assigned version on success.
func (s *PGJournal) Append(ctx context.Context, instanceID, leaseID idutil.SafeID, event journal.Event) (int64, error) {
	if err := event.ValidateForAppend(); err != nil {
		return 0, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGConnect,
			"saga journal: Append begin tx", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	row, err := selectInstanceLocked(ctx, tx, instanceID)
	if err != nil {
		// Append semantics: unknown instance → typed KindNotFound.
		if errors.Is(err, errInstanceMissingSentinel) {
			return 0, errInstanceNotFound(instanceID)
		}
		return 0, err
	}

	now := s.clock.Now()
	if !fenced(row, leaseID, now) {
		return 0, errStaleLease(instanceID, leaseID)
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
		statusToInt16(newStatus), now, string(instanceID),
	); err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: Append update projection failed", err)
	}

	version := row.currentVersion + 1
	if _, err := tx.Exec(ctx, insertEvent,
		string(instanceID), version, kindToInt16(event.Kind),
		nullableStepName(event.StepName), event.Payload, now,
	); err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: Append insert event failed", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGConnect,
			"saga journal: Append commit failed", err)
	}
	committed = true
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
			return nil, errInstanceNotFound(instanceID)
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
		return nil, "", errNonPositiveBatchSize(batchSize)
	}
	if leaseDuration <= 0 {
		return nil, "", errNonPositiveLeaseDuration(leaseDuration)
	}

	leaseStr := uuid.NewString()
	leaseMicros := fmt.Sprintf("%d", leaseDuration.Microseconds())
	now := s.clock.Now()

	rows, err := s.db.Query(ctx, claimPendingQuery, leaseStr, leaseMicros, batchSize, now)
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
			currentVer   int64
			startedAt    time.Time
			updatedAt    time.Time
		)
		if err := rows.Scan(&id, &definitionID, &rawStatus, &currentVer, &startedAt, &updatedAt); err != nil {
			return nil, "", errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
				"saga journal: ClaimPending scan failed", err)
		}
		status, sErr := statusFromInt16(rawStatus)
		if sErr != nil {
			return nil, "", errcode.Wrap(errcode.KindInternal, errcode.ErrPGSchemaShape,
				"saga journal: ClaimPending encountered invalid status", sErr)
		}
		// updatedAt is non-nullable in saga_instances; copy it for the projection.
		ua := updatedAt
		_ = currentVer
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
		return false, errNonPositiveLeaseDuration(leaseDuration)
	}
	micros := fmt.Sprintf("%d", leaseDuration.Microseconds())
	now := s.clock.Now()
	ct, err := s.db.Exec(ctx, heartbeatQuery, micros, string(instanceID), string(leaseID), now)
	if err != nil {
		return false, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: Heartbeat failed", err)
	}
	return ct.RowsAffected() == 1, nil
}

// MarkTerminal transitions the instance to finalStatus and appends the
// matching terminal event atomically. (false, nil) on stale lease / missing
// instance; KindInvalid on a non-terminal finalStatus or illegal transition.
func (s *PGJournal) MarkTerminal(ctx context.Context, instanceID, leaseID idutil.SafeID, finalStatus saga.Status) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGConnect,
			"saga journal: MarkTerminal begin tx", err)
	}
	committed := false
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
		return false, errInvalidTerminalStatus(instanceID, row.status, finalStatus)
	}
	termKind, okKind := journal.TerminalEventKind(finalStatus)
	if !okKind {
		return false, errInvalidTerminalStatus(instanceID, row.status, finalStatus)
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

	if _, err := tx.Exec(ctx, updateInstanceTerminal,
		statusToInt16(finalStatus), now, string(instanceID),
	); err != nil {
		return false, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: MarkTerminal update failed", err)
	}

	version := row.currentVersion + 1
	if _, err := tx.Exec(ctx, insertEvent,
		string(instanceID), version, kindToInt16(termKind),
		nullableStepName(""), nil, now,
	); err != nil {
		return false, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGQuery,
			"saga journal: MarkTerminal insert event failed", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return false, errcode.Wrap(errcode.KindInternal, kerrors.ErrAdapterPGConnect,
			"saga journal: MarkTerminal commit failed", err)
	}
	committed = true
	return true, nil
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
// errInstanceNotFound. Internal to this package — never returned through the
// Journal surface.
var errInstanceMissingSentinel = errors.New("saga journal: instance missing")

// selectInstanceLocked returns the instance projection under FOR UPDATE
// lock. errInstanceMissingSentinel signals "no such row"; callers translate
// (Append → errInstanceNotFound, MarkTerminal → silent (false, nil)).
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
			return 0, errEventPhase(inst.ID, kind, st)
		}
	case journal.KindCompensationStarted:
		if err := saga.AdvanceSaga(inst, saga.StatusCompensating, now); err != nil {
			return 0, err
		}
		return saga.StatusCompensating, nil
	case journal.KindStepCompensated:
		if st != saga.StatusCompensating {
			return 0, errEventPhase(inst.ID, kind, st)
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
	// Range-check before narrowing int16→uint8 so any out-of-band value is
	// rejected as a schema-shape error rather than silently wrapping.
	if v < 1 || v > 255 {
		return 0, fmt.Errorf("status %d out of int8 range", v)
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
	if v < 1 || v > 255 {
		return 0, fmt.Errorf("event kind %d out of int8 range", v)
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

// ---------------------------------------------------------------------------
// Error constructors (mirror kernel/saga/journal/errors.go shape)
// ---------------------------------------------------------------------------

func errDuplicateInstance(id idutil.SafeID) error {
	return errcode.New(errcode.KindConflict, errcode.ErrSagaDuplicateInstance,
		"saga journal: instance already enqueued",
		errcode.WithInternal(fmt.Sprintf("instanceID=%s", id)),
	)
}

func errInstanceNotFound(id idutil.SafeID) error {
	return errcode.New(errcode.KindNotFound, errcode.ErrSagaNotFound,
		"saga journal: instance not found",
		errcode.WithInternal(fmt.Sprintf("instanceID=%s", id)),
	)
}

func errStaleLease(instanceID, leaseID idutil.SafeID) error {
	return errcode.New(errcode.KindConflict, errcode.ErrSagaStaleLease,
		"saga journal: stale lease on append",
		errcode.WithInternal(fmt.Sprintf("instanceID=%s leaseID=%s", instanceID, leaseID)),
	)
}

func errInvalidTerminalStatus(instanceID idutil.SafeID, from, to saga.Status) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"saga journal: invalid terminal status",
		errcode.WithInternal(fmt.Sprintf("instanceID=%s from=%s to=%s", instanceID, from, to)),
	)
}

func errNonPositiveBatchSize(n int) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"saga journal: ClaimPending batchSize must be positive",
		errcode.WithInternal(fmt.Sprintf("batchSize=%d", n)),
	)
}

func errNonPositiveLeaseDuration(d time.Duration) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"saga journal: leaseDuration must be positive",
		errcode.WithInternal(fmt.Sprintf("leaseDuration=%s", d)),
	)
}

func errEventPhase(instanceID idutil.SafeID, kind journal.EventKind, status saga.Status) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"saga journal: event kind not allowed in current phase",
		errcode.WithInternal(fmt.Sprintf("instanceID=%s kind=%s status=%s", instanceID, kind, status)),
	)
}
