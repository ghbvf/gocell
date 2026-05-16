// Package configreader holds the shared config-read business logic (GetByKey /
// List) consumed by two sibling slices that sit on different HTTP trust
// boundaries: the public-facing `configread` slice (GET + list under
// /api/v1, admin-gated) and the internal control-plane `configreadinternal`
// slice (GET under /internal/v1, caller-cell gated). Slices may not import
// each other, so the read logic lives here and each slice type-aliases
// configreader.Service. This mirrors cells/auditcore/internal/appender.
//
// The split is enforced by governance rule SLICE-HTTP-VISIBILITY-SEGREGATION-01
// (FMT-33): a single slice must not serve both public and internal HTTP
// contracts.
//
// configread.Service and configreadinternal.Service are type aliases (not interfaces):
// both slices call identical methods with zero divergence, so an interface + separate
// impl would add indirection with no benefit.
package configreader

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// configSort defines the default sort for config listings.
var configSort = []query.SortColumn{
	{Name: "key", Direction: query.SortASC},
	{Name: "id", Direction: query.SortASC},
}

// Service implements config read business logic shared by the public and
// internal read slices.
type Service struct {
	repo      ports.ConfigRepository
	codec     *query.CursorCodec
	logger    *slog.Logger
	runMode   query.RunMode
	sliceName string
}

// NewService creates a config-read Service. sliceName identifies the owning
// slice in observability labels (e.g. "configread" or "configreadinternal");
// each slice must create its own Service instance so that cursor-error logs and
// query-context labels can be attributed to the correct slice.
//
// runMode controls cursor fail-open vs fail-closed semantics; pass
// query.RunModeProd unless the assembly declares DurabilityDemo.
//
// codec must be non-nil — pagination cannot be served without a cursor codec.
// Passing nil is a caller programming error; NewService returns errcode.ErrCellMissingCodec
// so the cell Init() can propagate a structured error instead of a runtime panic.
func NewService(
	repo ports.ConfigRepository,
	codec *query.CursorCodec,
	logger *slog.Logger,
	sliceName string,
	runMode query.RunMode,
) (*Service, error) {
	if codec == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellMissingCodec,
			"configreader: cursor codec is required")
	}
	return &Service{
		repo:      repo,
		codec:     codec,
		logger:    logger,
		runMode:   runMode,
		sliceName: sliceName,
	}, nil
}

// GetByKey retrieves a config entry by key.
func (s *Service) GetByKey(ctx context.Context, key string) (*domain.ConfigEntry, error) {
	entry, err := s.repo.GetByKey(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("config-read: get: %w", err)
	}
	return entry, nil
}

// List returns a paginated page of config entries.
func (s *Service) List(ctx context.Context, pageReq query.PageParams) (query.PageResult[*domain.ConfigEntry], error) {
	qctx := query.QueryContext("endpoint", s.sliceName)
	return query.ExecutePagedQuery(ctx, query.PagedQueryConfig[*domain.ConfigEntry]{
		Codec:      s.codec,
		PageParams: pageReq,
		Sort:       configSort,
		QueryCtx:   qctx,
		Fetch: func(ctx context.Context, params query.ListParams) ([]*domain.ConfigEntry, error) {
			entries, err := s.repo.List(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("config-read: list: %w", err)
			}
			return entries, nil
		},
		Extract: func(e *domain.ConfigEntry) []any {
			return []any{e.Key, e.ID}
		},
		OnCursorErr: query.LogCursorError(s.logger, s.sliceName),
		RunMode:     s.runMode,
	})
}
