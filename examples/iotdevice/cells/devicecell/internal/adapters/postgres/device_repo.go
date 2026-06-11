// Package postgres provides cell-private PostgreSQL implementations of the
// devicecell port interfaces. These implementations live inside the cell's
// internal package tree so they can import the cell's own internal/domain
// without violating Go module visibility rules — adapters/ cannot import
// examples/*/internal/..., but the reverse is allowed.
//
// Layering note: this package does NOT import adapters/postgres. SQLSTATE error
// classification goes through the single source pkg/pgquery (importable from
// examples/ — it is a leaf wire-error helper, see SQLSTATE-SINGLE-SOURCE-01);
// no local classifier is duplicated here.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/adapters/postgres/internal/pgexec"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/pgquery"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/validation"
)

// PGDeviceRepository implements domain.DeviceRepository backed by PostgreSQL.
// It reads/writes the `devices` table (migration 029).
//
// Transaction contract: txRunner is a construction-time policy declaration only.
// Write paths (Create) extract any ambient pgx.Tx from ctx via
// kernel/persistence.TxCtxKey. When no tx is present the pool is used directly.
type PGDeviceRepository struct {
	db       pgexec.PGExecutor
	txRunner persistence.TxRunner
	clock    clock.Clock
}

// Compile-time assertion: PGDeviceRepository implements domain.DeviceRepository.
var _ domain.DeviceRepository = (*PGDeviceRepository)(nil)

// NewPGDeviceRepository constructs a PGDeviceRepository. Fails fast on nil dependencies.
func NewPGDeviceRepository(pool *pgxpool.Pool, txRunner persistence.TxRunner, clk clock.Clock) (*PGDeviceRepository, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecell.NewPGDeviceRepository: pool must not be nil")
	}
	if validation.IsNilInterface(txRunner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecell.NewPGDeviceRepository: txRunner must not be nil")
	}
	if validation.IsNilInterface(clk) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecell.NewPGDeviceRepository: clock must not be nil")
	}
	return &PGDeviceRepository{
		db:       pgexec.New(pool),
		txRunner: txRunner,
		clock:    clk,
	}, nil
}

const (
	// deviceColumns is the single source for the full device column list (in
	// scan order) shared by every SELECT, so the column set and scanDeviceRow
	// never drift.
	deviceColumns = "id, name, status, last_seen, cert_epoch, cert_expires_at, renewal_requested_epoch, renewal_requested_at"

	insertDeviceSQL = "INSERT INTO devices (" + deviceColumns + ") VALUES ($1, $2, $3, $4, $5, $6, $7, $8)"

	selectDeviceByIDSQL = "SELECT " + deviceColumns + " FROM devices WHERE id = $1"

	// selectCertRenewalCandidatesSQL returns near-expiry certs that are
	// renewal-eligible. cert_expires_at IS NOT NULL excludes rows with no
	// issued cert (zero expiry <-> NULL).
	//
	// Eligibility predicate:
	//   renewal_requested_epoch <> cert_epoch  — new epoch, never requested
	//   OR renewal_requested_at IS NULL        — IS NULL is LOAD-BEARING:
	//     migration bridge for rows marked under the old logic (pre-060) that
	//     have renewal_requested_epoch == cert_epoch but NULL renewal_requested_at.
	//     Without this disjunct those rows would be permanently excluded —
	//     restoring the original single-direction suppression bug on existing rows.
	//   OR renewal_requested_at <= $2          — stale timestamp → retry window
	selectCertRenewalCandidatesSQL = `
SELECT id, cert_epoch, cert_expires_at
FROM devices
WHERE cert_expires_at IS NOT NULL
  AND cert_expires_at <= $1
  AND (renewal_requested_epoch <> cert_epoch
       OR renewal_requested_at IS NULL
       OR renewal_requested_at <= $2)
ORDER BY cert_expires_at ASC, id ASC
LIMIT $3`

	// markCertRenewalRequestedSQL is a compare-and-set on cert_epoch: it records
	// both renewal_requested_epoch and renewal_requested_at while the row's current
	// cert_epoch still matches. Re-marking the same epoch refreshes the timestamp
	// (time-window retry). Zero rows affected (re-issued/gone) is a valid no-op.
	markCertRenewalRequestedSQL = "UPDATE devices" +
		" SET renewal_requested_epoch = $2, renewal_requested_at = $3" +
		" WHERE id = $1 AND cert_epoch = $2"
)

// Create inserts a new device row. Returns ErrConflict on unique constraint violation.
func (r *PGDeviceRepository) Create(ctx context.Context, device *domain.Device) error {
	d := *device
	d.NormalizeCertState() // single source: backfill zero cert_epoch -> 1 (satisfies CHECK)
	_, err := r.db.Exec(ctx, insertDeviceSQL,
		d.ID,
		d.Name,
		d.Status,
		d.LastSeen,
		d.CertEpoch,
		nullableTime(d.CertExpiresAt),
		d.RenewalRequestedEpoch,
		nullableTime(d.RenewalRequestedAt),
	)
	if err != nil {
		if pgquery.IsUniqueViolation(err) {
			return errcode.New(errcode.KindConflict, errcode.ErrConflict,
				"device already exists",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%q", device.ID))))
		}
		slog.Error("device_repo: pg write failed",
			slog.String("operation", "create"),
			slog.String("device_id", device.ID),
			slog.Any("error", err))
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "device_repo: create", err)
	}
	return nil
}

// GetByID fetches a device by primary key. Returns ErrDeviceNotFound when absent.
func (r *PGDeviceRepository) GetByID(ctx context.Context, id string) (*domain.Device, error) {
	row := r.db.QueryRow(ctx, selectDeviceByIDSQL, id)
	d, err := scanDeviceRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrDeviceNotFound,
				"device not found",
				errcode.WithDetails(errcode.PublicString("deviceId", id)))
		}
		var ec *errcode.Error
		if errors.As(err, &ec) && ec.Code == errcode.ErrPGSchemaShape {
			return nil, err
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "device_repo: get-by-id", err)
	}
	return d, nil
}

// List returns up to params.FetchLimit() (= Limit+1) devices sorted per params.
// Callers use the extra row to detect HasMore without a separate COUNT query.
//
// When params.CursorValues is non-nil it must have the same length as
// params.Sort; otherwise ErrCursorInvalid is returned. Only uniform-direction
// sorts (all ASC or all DESC) are supported; mixed-direction sorts return
// ErrCursorInvalid.
//
// Sort columns must be a trusted subset of {name, id, status} — column names
// come from query.SortColumn.Name which is produced by trusted code paths, not
// raw user input, so they are safe to interpolate into the ORDER BY clause.
func (r *PGDeviceRepository) List(ctx context.Context, params query.ListParams) ([]*domain.Device, error) {
	sqlStr, args, err := buildListQuery(params)
	if err != nil {
		return nil, err
	}

	rows, err := r.db.Query(ctx, sqlStr, args...)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "device_repo: list", err)
	}
	defer rows.Close()

	var devices []*domain.Device
	for rows.Next() {
		d, scanErr := scanDeviceRow(rows)
		if scanErr != nil {
			var ec *errcode.Error
			if errors.As(scanErr, &ec) && ec.Code == errcode.ErrPGSchemaShape {
				return nil, scanErr
			}
			return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "device_repo: list scan", scanErr)
		}
		devices = append(devices, d)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "device_repo: list rows", err)
	}
	return devices, nil
}

// ListCertificateRenewalCandidates returns near-expiry certs that are
// renewal-eligible, sorted by expiry then id, capped at limit. See the
// domain.DeviceRepository contract for the full eligibility predicate.
func (r *PGDeviceRepository) ListCertificateRenewalCandidates(
	ctx context.Context, expiresBefore time.Time, retryBefore time.Time, limit int,
) ([]domain.CertificateRenewalCandidate, error) {
	rows, err := r.db.Query(ctx, selectCertRenewalCandidatesSQL, expiresBefore, retryBefore, limit)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "device_repo: list cert renewal candidates", err)
	}
	defer rows.Close()

	out := make([]domain.CertificateRenewalCandidate, 0)
	for rows.Next() {
		var c domain.CertificateRenewalCandidate
		// cert_expires_at is guaranteed non-NULL by the WHERE clause.
		if scanErr := rows.Scan(&c.DeviceID, &c.CertEpoch, &c.CertExpiresAt); scanErr != nil {
			return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "device_repo: scan cert renewal candidate", scanErr)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "device_repo: list cert renewal candidates rows", err)
	}
	return out, nil
}

// MarkCertRenewalRequested is a compare-and-set on cert_epoch (see the
// domain.DeviceRepository contract). Records both renewal_requested_epoch and
// renewal_requested_at. Re-marking the same epoch refreshes the timestamp.
// Zero rows affected (re-issued/gone) is a valid no-op, not an error.
func (r *PGDeviceRepository) MarkCertRenewalRequested(ctx context.Context, deviceID string, epoch int64, requestedAt time.Time) error {
	if _, err := r.db.Exec(ctx, markCertRenewalRequestedSQL, deviceID, epoch, requestedAt); err != nil {
		slog.Error("device_repo: pg write failed",
			slog.String("operation", "mark_cert_renewal_requested"),
			slog.String("device_id", deviceID),
			slog.Any("error", err))
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "device_repo: mark cert renewal requested", err)
	}
	return nil
}

// buildListQuery constructs the SELECT SQL and placeholder args for a List call.
// When CursorValues is nil the query is a plain ORDER BY ... LIMIT.
// When CursorValues is non-nil a keyset WHERE predicate is prepended.
// Only uniform sort directions (all ASC or all DESC) are supported.
func buildListQuery(params query.ListParams) (string, []any, error) {
	orderBy := buildOrderBy(params.Sort)

	if len(params.CursorValues) == 0 {
		// First page: no keyset predicate.
		sqlStr := "SELECT " + deviceColumns + " FROM devices ORDER BY " + orderBy + " LIMIT $1"
		return sqlStr, []any{params.FetchLimit()}, nil
	}

	// Validate cursor length matches sort columns.
	if len(params.CursorValues) != len(params.Sort) {
		return "", nil, errcode.New(errcode.KindInvalid, errcode.ErrCursorInvalid,
			"cursor values length must match sort columns")
	}
	if len(params.Sort) == 0 {
		return "", nil, errcode.New(errcode.KindInvalid, errcode.ErrCursorInvalid,
			"sort columns required when cursor values are present")
	}

	// Determine uniform sort direction — only all-ASC or all-DESC are supported.
	dir := params.Sort[0].Direction
	for _, col := range params.Sort[1:] {
		if col.Direction != dir {
			return "", nil, errcode.New(errcode.KindInvalid, errcode.ErrCursorInvalid,
				"mixed sort directions not supported for keyset pagination")
		}
	}

	// Build row-value comparison: (col1, col2) > ($1, $2) for ASC,
	//                              (col1, col2) < ($1, $2) for DESC.
	op := ">"
	if dir == query.SortDESC {
		op = "<"
	}

	colNames := make([]string, len(params.Sort))
	placeholders := make([]string, len(params.Sort))
	args := make([]any, 0, len(params.CursorValues)+1)
	for i, col := range params.Sort {
		colNames[i] = trustedDeviceColumn(col.Name)
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args = append(args, params.CursorValues[i])
	}
	args = append(args, params.FetchLimit())
	limitPlaceholder := fmt.Sprintf("$%d", len(params.Sort)+1)

	sqlStr := fmt.Sprintf(
		"SELECT "+deviceColumns+" FROM devices WHERE (%s) %s (%s) ORDER BY %s LIMIT %s",
		strings.Join(colNames, ", "),
		op,
		strings.Join(placeholders, ", "),
		orderBy,
		limitPlaceholder,
	)
	return sqlStr, args, nil
}

// buildOrderBy converts sort columns to a SQL ORDER BY clause fragment.
// Column names are trusted identifiers from code paths — not user input.
func buildOrderBy(cols []query.SortColumn) string {
	if len(cols) == 0 {
		return "name ASC, id ASC"
	}
	parts := make([]string, 0, len(cols))
	for _, c := range cols {
		col := trustedDeviceColumn(c.Name)
		dir := "ASC"
		if c.Direction == query.SortDESC {
			dir = "DESC"
		}
		parts = append(parts, col+" "+dir)
	}
	return strings.Join(parts, ", ")
}

// trustedDeviceColumn maps a sort column name to the literal SQL column name.
// Unknown columns fall back to "id" (stable, unique tiebreaker) to avoid
// SQL injection while preserving query validity.
func trustedDeviceColumn(name string) string {
	switch name {
	case "name":
		return "name"
	case "id":
		return "id"
	case "status":
		return "status"
	default:
		return "id"
	}
}

// rowScanner is the common Scan surface of pgx.Row and pgx.Rows, so a single
// scanDeviceRow serves both GetByID (Row) and List (Rows) without drift.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanDeviceRow scans a full device row (id..renewal_requested_at, in
// deviceColumns order) into a domain.Device. cert_expires_at and
// renewal_requested_at are nullable: SQL NULL leaves the corresponding field
// zero ("no cert issued" / "never requested").
func scanDeviceRow(s rowScanner) (*domain.Device, error) {
	var d domain.Device
	var status string
	var lastSeen time.Time
	var certExpiresAt *time.Time
	var renewalRequestedAt *time.Time
	if err := s.Scan(
		&d.ID, &d.Name, &status, &lastSeen,
		&d.CertEpoch, &certExpiresAt, &d.RenewalRequestedEpoch, &renewalRequestedAt,
	); err != nil {
		return nil, err
	}
	if !validDeviceStatus(status) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrPGSchemaShape,
			"device row has invalid status enum",
			errcode.WithDetails(errcode.PublicString("table", "devices"), errcode.PublicString("column", "status")),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("scanned status=%q", status))))
	}
	d.Status = status
	d.LastSeen = lastSeen
	if certExpiresAt != nil {
		d.CertExpiresAt = *certExpiresAt
	}
	if renewalRequestedAt != nil {
		d.RenewalRequestedAt = *renewalRequestedAt
	}
	return &d, nil
}

// nullableTime maps a zero time.Time to SQL NULL and any other value to itself,
// so nullable timestamp columns round-trip zero <-> NULL.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// RepoReady verifies that the devices table is reachable by executing a
// lightweight probe query. It is registered as the "devicecell_repo_ready"
// probe (cellgen ProbeRepoReady) via the generated devicecell.RegisterReadiness
// typed funnel in the cell Init path (hand-written cell code must not call
// reg.RegisterReadiness with a bare string — PROBENAME-SEALED-FUNNEL-01).
func (r *PGDeviceRepository) RepoReady(ctx context.Context) error {
	var dummy int
	err := r.db.QueryRow(ctx, `SELECT 1 FROM devices LIMIT 1`).Scan(&dummy)
	// pgx.ErrNoRows means the table exists but is empty — that is healthy.
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "device_repo: readiness probe", err)
	}
	return nil
}

// validDeviceStatus reports whether s is a known device status value.
// Must stay in sync with the CHECK constraint in migration 029_devices.sql.
func validDeviceStatus(s string) bool {
	return s == "online" || s == "offline"
}
