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

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
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

// List implements list.Service. STUB (303-US4 RED): the real pagination wiring
// lands in the GREEN commit.
func (s *Service) List(_ context.Context, _ *list.Request) (list.ListResponseObject, error) {
	return nil, errcode.New(errcode.KindInternal, errcode.ErrInternal, "registrycore: list not implemented")
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
