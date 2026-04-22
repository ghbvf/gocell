// Package devicelist implements the device-list slice: paginated device listing.
// Consistency: L0 LocalOnly — read-only query, no state mutation or outbox publishing.
package devicelist

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// defaultSort orders devices by name ASC, id ASC (stable, human-friendly).
var defaultSort = []query.SortColumn{
	{Name: "name", Direction: query.SortASC},
	{Name: "id", Direction: query.SortASC},
}

// Service lists devices with cursor pagination.
type Service struct {
	deviceRepo domain.DeviceRepository
	codec      *query.CursorCodec
	logger     *slog.Logger
	runMode    query.RunMode
}

// NewService creates a device-list Service.
// codec must be non-nil — cursor pagination cannot be served without it.
func NewService(deviceRepo domain.DeviceRepository, codec *query.CursorCodec, logger *slog.Logger, runMode query.RunMode) (*Service, error) {
	if codec == nil {
		return nil, errcode.New(errcode.ErrCellMissingCodec,
			"device-list: cursor codec is required")
	}
	return &Service{deviceRepo: deviceRepo, codec: codec, logger: logger, runMode: runMode}, nil
}

// List returns a paginated page of devices sorted by name ASC, id ASC.
func (s *Service) List(ctx context.Context, pageReq query.PageRequest) (query.PageResult[*domain.Device], error) {
	qctx := query.QueryContext("endpoint", "device-list")
	return query.ExecutePagedQuery(ctx, query.PagedQueryConfig[*domain.Device]{
		Codec:    s.codec,
		Request:  pageReq,
		Sort:     defaultSort,
		QueryCtx: qctx,
		Fetch: func(ctx context.Context, params query.ListParams) ([]*domain.Device, error) {
			devices, err := s.deviceRepo.List(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("device-list: fetch: %w", err)
			}
			return devices, nil
		},
		Extract: func(d *domain.Device) []any {
			return []any{d.Name, d.ID}
		},
		OnCursorErr: query.LogCursorError(s.logger, "device-list"),
		RunMode:     s.runMode,
	})
}
