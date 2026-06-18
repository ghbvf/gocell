// Package registryread serves the runtime contract-registration list contract
// http.registry.contract.list.v1 (303-US4, #2235) over the in-mem kernel
// ContractRegistrar (303-US2, #2233). It returns a paginated, id-ordered view of
// all registrations (the registrar already sorts by id, so the cursor is the last
// id of the page). The route is gated by the registry:read permission. The
// durable store + tenant/RLS scoping is US5 — this slice holds no cross-cell
// reference and no tenant filtering today.
package registryread

import (
	"context"
	"sort"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	list "github.com/ghbvf/gocell/generated/contracts/http/registry/contract/list/v1"
)

// Service implements the generated list.Service over the shared in-mem
// ContractRegistrar the cell injects. It is read-only: it gathers registrations,
// applies cursor pagination, and projects them to the wire DTO.
type Service struct {
	registrar *registry.ContractRegistrar `gocell:"required"`
}

// NewService constructs the list service. registrar is the cell-scoped in-mem
// state machine (required — validateRequired fail-fasts on nil).
func NewService(registrar *registry.ContractRegistrar) (*Service, error) {
	s := &Service{registrar: registrar}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

const (
	// defaultLimit is the page size used when the request omits limit (or sends ≤0).
	defaultLimit = 50
	// maxLimit is the hard ceiling (go-standards.md §列表分页: limit ≤ 500).
	maxLimit = 500
)

// effectiveLimit clamps the request limit to [1, maxLimit], defaulting an unset
// (≤0) limit to defaultLimit. The ceiling holds even if the handler did not
// enforce the schema maximum (defense in depth).
func effectiveLimit(reqLimit int64) int {
	switch {
	case reqLimit <= 0:
		return defaultLimit
	case reqLimit > maxLimit:
		return maxLimit
	default:
		return int(reqLimit)
	}
}

// List implements list.Service: an id-ordered, cursor-paginated view of all
// registrations. The registrar returns ids already sorted ascending, so the
// cursor is simply the last id of the previous page; resumption is "first id
// strictly greater than the cursor". A list always succeeds (200) — there is no
// tenant scoping in US4 (RLS is US5).
func (s *Service) List(_ context.Context, req *list.Request) (list.ListResponseObject, error) {
	limit := effectiveLimit(req.Limit)
	ids := s.registrar.AllIDs() // sorted ascending

	start := 0
	if req.Cursor != "" {
		start = sort.Search(len(ids), func(i int) bool { return ids[i] > req.Cursor })
	}
	end := start + limit
	hasMore := end < len(ids)
	if end > len(ids) {
		end = len(ids)
	}
	page := ids[start:end]

	items := make([]*list.ResponseDataItem, 0, len(page))
	for _, id := range page {
		reg, ok := s.registrar.Get(id)
		if !ok {
			continue // concurrently removed; skip (registrar is the source of truth)
		}
		items = append(items, toListItem(reg))
	}

	nextCursor := ""
	if hasMore && len(page) > 0 {
		nextCursor = page[len(page)-1]
	}
	return list.List200JSONResponse{Data: items, NextCursor: nextCursor, HasMore: hasMore}, nil
}

// toListItem projects a ContractRegistration onto the generated wire DTO. State is
// the sealed RegistrationState spelling; timestamps are RFC3339 UTC.
func toListItem(reg registry.ContractRegistration) *list.ResponseDataItem {
	return &list.ResponseDataItem{
		ID:            reg.ID,
		Kind:          reg.Kind,
		State:         reg.State.String(),
		Submitter:     reg.Submitter,
		Approver:      reg.Approver,
		PayloadSchema: reg.PayloadSchema,
		CreatedAt:     reg.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     reg.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// Handler wires the generated list.Handler with the registry:read PDP gate.
type Handler struct {
	h *list.Handler
}

// NewHandler builds the route handler with the registry:read permission policy.
func NewHandler(svc *Service) *Handler {
	return &Handler{h: list.NewHandler(svc, auth.RequirePermission(authz.PermRegistryRead()))}
}

// RegisterRoutes mounts the contract on mux via the generated handler.
func (h *Handler) RegisterRoutes(mux cell.RouteHandler) error {
	return h.h.RegisterRoutes(mux)
}
