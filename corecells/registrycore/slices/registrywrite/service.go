// Package registrywrite serves the runtime contract-registration submit contract
// http.registry.contract.submit.v1 (303-US4, #2235) over the in-mem kernel
// ContractRegistrar (303-US2, #2233). A submit records a new registration in the
// submitted state; the submitter is the authenticated principal, never a body
// field. The route is gated by the registry:submit permission (tenancy.md §"ABAC
// authz 接线"). The durable store is US5; the governance gate and audit/events
// are US6/US8 — this slice holds no cross-cell reference.
package registrywrite

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	submit "github.com/ghbvf/gocell/generated/contracts/http/registry/contract/submit/v1"
)

// Service implements the generated submit.Service over the shared in-mem
// ContractRegistrar the cell injects. It is the application layer: it adapts the
// wire Request to the kernel SubmitInput, calls the sealed state machine, and
// projects the resulting ContractRegistration to the wire DTO.
type Service struct {
	clk       clock.Clock
	registrar *registry.ContractRegistrar `gocell:"required"`
}

// NewService constructs the submit service. clk is the positional clock
// dependency (clock.Clock convention); registrar is the cell-scoped in-mem state
// machine (required — validateRequired fail-fasts on nil).
func NewService(clk clock.Clock, registrar *registry.ContractRegistrar) (*Service, error) {
	clock.MustHaveClock(clk, "registrywrite.NewService")
	s := &Service{clk: clk, registrar: registrar}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// Submit implements submit.Service. STUB (303-US4 RED): the real registrar wiring
// lands in the GREEN commit.
func (s *Service) Submit(_ context.Context, _ *submit.Request) (submit.SubmitResponseObject, error) {
	return nil, errcode.New(errcode.KindInternal, errcode.ErrInternal, "registrycore: submit not implemented")
}

// Handler wires the generated submit.Handler with the registry:submit PDP gate.
type Handler struct {
	h *submit.Handler
}

// NewHandler builds the route handler with the registry:submit permission policy.
func NewHandler(svc *Service) *Handler {
	return &Handler{h: submit.NewHandler(svc, auth.RequirePermission(authz.PermRegistrySubmit()))}
}

// RegisterRoutes mounts the contract on mux via the generated handler.
func (h *Handler) RegisterRoutes(mux cell.RouteHandler) error {
	return h.h.RegisterRoutes(mux)
}
