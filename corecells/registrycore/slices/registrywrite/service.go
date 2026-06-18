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
	"errors"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	submit "github.com/ghbvf/gocell/generated/contracts/http/registry/contract/submit/v1"
)

// Service implements the generated submit.Service over the shared in-mem
// ContractRegistrar the cell injects. It is the application layer: it adapts the
// wire Request to the kernel SubmitInput, calls the sealed state machine, and
// projects the resulting ContractRegistration to the wire DTO. It holds no clock:
// the registrar stamps timestamps inside its own lock with the cell's clock.
type Service struct {
	registrar *registry.ContractRegistrar `gocell:"required"`
}

// NewService constructs the submit service. registrar is the cell-scoped in-mem
// state machine (required — validateRequired fail-fasts on nil).
func NewService(registrar *registry.ContractRegistrar) (*Service, error) {
	s := &Service{registrar: registrar}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// Submit implements submit.Service: it records the submission in the sealed
// state machine (state derives from registry.RegistrationState, never a string
// literal) and projects the resulting registration to the wire DTO. The submitter
// is the authenticated principal subject — the gate guarantees one, so the empty
// fallback is a defensive path the registrar rejects (ErrValidationFailed → 400).
func (s *Service) Submit(ctx context.Context, req *submit.Request) (submit.SubmitResponseObject, error) {
	submitter := ""
	if p, ok := auth.FromContext(ctx); ok && p != nil {
		submitter = p.Subject
	} else {
		// Defensive: the registry:submit gate guarantees a principal, so reaching
		// here means an upstream auth-wiring anomaly. Surface it (registrar then
		// fail-closes the empty submitter to 400) rather than registering anonymously.
		slog.WarnContext(ctx, "registrywrite: submit reached without an authenticated principal",
			"contract", "http.registry.contract.submit.v1")
	}
	reg, err := s.registrar.Submit(registry.SubmitInput{
		ID:            req.ID,
		Kind:          string(req.Kind),
		PayloadSchema: req.PayloadSchema,
		Submitter:     submitter,
	})
	if err != nil {
		return submitErrorResponse(err)
	}
	return submit.Submit201JSONResponse{Data: toSubmitData(reg)}, nil
}

// submitErrorResponse maps a registrar error to the contract's typed 4xx envelope:
// a duplicate id is 409, a validation failure (e.g. missing submitter on the
// defensive no-principal path) is 400. Anything else bubbles as an undeclared
// framework 5xx (cell-patterns.md §Typed response envelope).
func submitErrorResponse(err error) (submit.SubmitResponseObject, error) {
	var ce *errcode.Error
	if errors.As(err, &ce) {
		switch ce.Code {
		case errcode.ErrRegistrationDuplicate:
			return submit.Submit409ErrorResponse{Body: *ce}, nil
		case errcode.ErrValidationFailed:
			return submit.Submit400ErrorResponse{Body: *ce}, nil
		}
	}
	return nil, err
}

// toSubmitData projects a ContractRegistration onto the generated wire DTO. State
// is the sealed RegistrationState spelling; timestamps are RFC3339 UTC.
func toSubmitData(reg registry.ContractRegistration) *submit.ResponseData {
	return &submit.ResponseData{
		ID:            reg.ID,
		Kind:          reg.Kind,
		State:         reg.State.String(),
		Submitter:     reg.Submitter,
		PayloadSchema: reg.PayloadSchema,
		CreatedAt:     reg.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     reg.UpdatedAt.UTC().Format(time.RFC3339),
	}
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
