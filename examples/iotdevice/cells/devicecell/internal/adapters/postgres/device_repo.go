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

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
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
	db       pgExecutor
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
		db:       newPGExecutor(pool),
		txRunner: txRunner,
		clock:    clk,
	}, nil
}

const (
	insertDeviceSQL = `
INSERT INTO devices (id, name, status, last_seen)
VALUES ($1, $2, $3, $4)`

	selectDeviceByIDSQL = `
SELECT id, name, status, last_seen
FROM devices
WHERE id = $1`
)

// Create inserts a new device row. Returns ErrConflict on unique constraint violation.
func (r *PGDeviceRepository) Create(ctx context.Context, device *domain.Device) error {
	_, err := r.db.Exec(ctx, insertDeviceSQL,
		device.ID,
		device.Name,
		device.Status,
		device.LastSeen,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errcode.New(errcode.KindConflict, errcode.ErrConflict,
				"device already exists",
				errcode.WithInternal(fmt.Sprintf("id=%q", device.ID)))
		}
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "device_repo: create", err)
	}
	return nil
}

// GetByID fetches a device by primary key. Returns ErrDeviceNotFound when absent.
func (r *PGDeviceRepository) GetByID(ctx context.Context, id string) (*domain.Device, error) {
	row := r.db.QueryRow(ctx, selectDeviceByIDSQL, id)
	d, err := scanDevice(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrDeviceNotFound,
				"device not found",
				errcode.WithDetails(slog.String("deviceId", id)))
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
// Sort columns must be a trusted subset of {name, id, status} — column names
// come from query.SortColumn.Name which is produced by trusted code paths, not
// raw user input, so they are safe to interpolate into the ORDER BY clause.
func (r *PGDeviceRepository) List(ctx context.Context, params query.ListParams) ([]*domain.Device, error) {
	orderBy := buildOrderBy(params.Sort)
	sql := "SELECT id, name, status, last_seen FROM devices ORDER BY " + orderBy + " LIMIT $1"

	rows, err := r.db.Query(ctx, sql, params.FetchLimit())
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "device_repo: list", err)
	}
	defer rows.Close()

	var devices []*domain.Device
	for rows.Next() {
		d, scanErr := scanDeviceFromRows(rows)
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

// scanDevice scans a pgx.Row into a domain.Device.
func scanDevice(row pgx.Row) (*domain.Device, error) {
	var d domain.Device
	var status string
	var lastSeen time.Time
	err := row.Scan(&d.ID, &d.Name, &status, &lastSeen)
	if err != nil {
		return nil, err
	}
	if !validDeviceStatus(status) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrPGSchemaShape,
			"device row has invalid status enum",
			errcode.WithDetails(slog.String("table", "devices"), slog.String("column", "status")),
			errcode.WithInternal(fmt.Sprintf("scanned status=%q", status)))
	}
	d.Status = status
	d.LastSeen = lastSeen
	return &d, nil
}

// scanDeviceFromRows scans a pgx.Rows cursor into a domain.Device.
func scanDeviceFromRows(rows pgx.Rows) (*domain.Device, error) {
	var d domain.Device
	var status string
	var lastSeen time.Time
	err := rows.Scan(&d.ID, &d.Name, &status, &lastSeen)
	if err != nil {
		return nil, err
	}
	if !validDeviceStatus(status) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrPGSchemaShape,
			"device row has invalid status enum",
			errcode.WithDetails(slog.String("table", "devices"), slog.String("column", "status")),
			errcode.WithInternal(fmt.Sprintf("scanned status=%q", status)))
	}
	d.Status = status
	d.LastSeen = lastSeen
	return &d, nil
}

// validDeviceStatus reports whether s is a known device status value.
// Must stay in sync with the CHECK constraint in migration 029_devices.sql.
func validDeviceStatus(s string) bool {
	return s == "online" || s == "offline"
}
