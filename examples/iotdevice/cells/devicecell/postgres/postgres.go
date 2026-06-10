// Package postgres exposes devicecell-owned PostgreSQL repository factories to
// composition roots (examples/iotdevice/main.go) while keeping the concrete
// implementation under the cell's internal adapter tree.
//
// ref: corecells/accesscore/postgres (mirror pattern)
package postgres

import (
	"github.com/jackc/pgx/v5/pgxpool"

	devicerepo "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/adapters/postgres"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// DeviceRepository re-exports the cell-private interface so that composition
// roots outside the devicecell subtree can name the return type without
// importing the internal/domain package. Same pattern as
// corecells/accesscore/postgres exposing cell-private ports types.
type DeviceRepository = domain.DeviceRepository

// NewDeviceRepository constructs the PG-backed devicecell DeviceRepository.
// Pass adapterpg.Pool.DB() as the pool argument — Pool.DB() returns the
// underlying *pgxpool.Pool accepted by this function. Passing a Pool value
// directly (not its .DB() result) will fail the type assertion.
//
// pool is accepted as `any` so callers can pass adapterpg.Pool.DB() without
// the devicecell public surface re-exporting pgx types.
func NewDeviceRepository(pool any, txRunner persistence.TxRunner, clk clock.Clock) (domain.DeviceRepository, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecell/postgres.NewDeviceRepository: pool must not be nil")
	}
	p, ok := pool.(*pgxpool.Pool)
	if !ok {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecell/postgres.NewDeviceRepository: pool must be *pgxpool.Pool")
	}
	if validation.IsNilInterface(txRunner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecell/postgres.NewDeviceRepository: txRunner must not be nil")
	}
	if validation.IsNilInterface(clk) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"devicecell/postgres.NewDeviceRepository: clock must not be nil")
	}
	return devicerepo.NewPGDeviceRepository(p, txRunner, clk)
}
