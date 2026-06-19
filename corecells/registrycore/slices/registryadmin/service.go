// Package registryadmin serves the runtime contract-registration admin approval
// contracts http.registry.contract.{approve,reject,retire}.v1 (303-US7, #2238)
// over the durable ports.Registry store. The three endpoints are the same shape —
// an admin-gated lifecycle transition that differs only in target state and
// permission — so a single slice/service hosts all three (mirroring configpublish
// serving publish+rollback), sharing one transition helper.
//
// Authorization is ABAC permission-based, NOT a handler role literal: each
// endpoint declares endpoints.http.permission (registry:approve|reject|retire) and
// the route gate is the cell-derived auth.MethodPolicyResolver → PDP. The PDP
// baseline grant for those permissions is role-conditioned (admin/super-admin) in
// accesscore; this service never inspects principal.Roles. The state-machine
// legality (pending-approval→approved; reject; active→retired) is enforced by the
// kernel registry.Transition single source via ports.Registry.Transition.
//
// Scope boundary: the cross-cell event.registry.contract-retired event + auditcore
// approval hash-chain are US8 (#2239); data-plane removal is US11/US12. This slice
// performs the state transition only; ports.Registry.Transition already appends the
// in-store migration-history event.
package registryadmin

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	approve "github.com/ghbvf/gocell/generated/contracts/http/registry/contract/approve/v1"
	reject "github.com/ghbvf/gocell/generated/contracts/http/registry/contract/reject/v1"
	retire "github.com/ghbvf/gocell/generated/contracts/http/registry/contract/retire/v1"
)

// Option configures a registryadmin Service.
type Option func(*Service)

// WithTxManager sets the CellTxManager for transactional guarantees (L1
// atomicity). Composition roots construct via persistence.WrapForCell.
func WithTxManager(tx persistence.CellTxManager) Option {
	return func(s *Service) {
		if tx != nil {
			s.txRunner = tx
		}
	}
}

// WithLogger sets an optional structured logger. When omitted the service uses
// the default slog logger.
func WithLogger(l *slog.Logger) Option {
	return func(s *Service) {
		if l != nil {
			s.logger = l
		}
	}
}

// Service implements the generated approve/reject/retire Services. Each method is
// a thin wrapper over the shared transition helper; the only per-endpoint
// differences are the target RegistrationState and the typed response envelope.
type Service struct {
	store    ports.Registry            `gocell:"required"`
	txRunner persistence.CellTxManager `gocell:"required" gocellErr:"registryadmin: TxRunner required; use WithTxManager"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	logger   *slog.Logger
}

// NewService constructs the admin service. store and txRunner are required;
// validateRequired fail-fasts on nil. logger is optional.
func NewService(store ports.Registry, opts ...Option) (*Service, error) {
	s := &Service{
		store:  store,
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// Approve implements approve.Service: advance a pending-approval registration to
// approved, recording the authenticated admin as the Approver.
func (s *Service) Approve(ctx context.Context, req *approve.Request) (approve.ApproveResponseObject, error) {
	reg, err := s.doTransition(ctx, req.ID, registry.StateApproved(), req.Reason)
	if err == nil {
		return approve.Approve200JSONResponse{Data: toApproveData(reg)}, nil
	}
	kind, ce := classifyTransitionErr(err)
	switch kind {
	case errTenant:
		return approve.Approve403ErrorResponse{Body: *ce}, nil
	case errNotFound:
		return approve.Approve404ErrorResponse{Body: *ce}, nil
	case errConflict:
		return approve.Approve409ErrorResponse{Body: *ce}, nil
	case errValidation:
		return approve.Approve400ErrorResponse{Body: *ce}, nil
	default:
		s.logTransitionFailure(ctx, "approve", req.ID, err)
		return nil, err
	}
}

// Reject implements reject.Service: advance a probing or pending-approval
// registration to the terminal rejected state.
func (s *Service) Reject(ctx context.Context, req *reject.Request) (reject.RejectResponseObject, error) {
	reg, err := s.doTransition(ctx, req.ID, registry.StateRejected(), req.Reason)
	if err == nil {
		return reject.Reject200JSONResponse{Data: toRejectData(reg)}, nil
	}
	kind, ce := classifyTransitionErr(err)
	switch kind {
	case errTenant:
		return reject.Reject403ErrorResponse{Body: *ce}, nil
	case errNotFound:
		return reject.Reject404ErrorResponse{Body: *ce}, nil
	case errConflict:
		return reject.Reject409ErrorResponse{Body: *ce}, nil
	case errValidation:
		return reject.Reject400ErrorResponse{Body: *ce}, nil
	default:
		s.logTransitionFailure(ctx, "reject", req.ID, err)
		return nil, err
	}
}

// Retire implements retire.Service: advance an active registration to the
// terminal retired state.
func (s *Service) Retire(ctx context.Context, req *retire.Request) (retire.RetireResponseObject, error) {
	reg, err := s.doTransition(ctx, req.ID, registry.StateRetired(), req.Reason)
	if err == nil {
		return retire.Retire200JSONResponse{Data: toRetireData(reg)}, nil
	}
	kind, ce := classifyTransitionErr(err)
	switch kind {
	case errTenant:
		return retire.Retire403ErrorResponse{Body: *ce}, nil
	case errNotFound:
		return retire.Retire404ErrorResponse{Body: *ce}, nil
	case errConflict:
		return retire.Retire409ErrorResponse{Body: *ce}, nil
	case errValidation:
		return retire.Retire400ErrorResponse{Body: *ce}, nil
	default:
		s.logTransitionFailure(ctx, "retire", req.ID, err)
		return nil, err
	}
}

// doTransition is the shared core of approve/reject/retire: extract tenant
// (fail-closed) and the authenticated admin actor, then advance the registration
// to the target state inside an L1 transaction. State-machine legality is enforced
// by the kernel (registry.Transition via ports.Registry.Transition); this method
// does not interpret the current state.
func (s *Service) doTransition(
	ctx context.Context,
	id string,
	to registry.RegistrationState,
	reason string,
) (registry.ContractRegistration, error) {
	var zero registry.ContractRegistration
	tnt, err := tenant.FromContext(ctx)
	if err != nil {
		// Missing/invalid tenant → ErrAuthForbidden (403), classified below.
		return zero, err
	}

	// Actor attribution: the authenticated admin subject. A blank actor (an
	// upstream auth-wiring anomaly — the registry:* gate guarantees a principal in
	// production) is caught fail-closed by AdvanceInput.Validate (→ 400).
	actor := ""
	if p, ok := auth.FromContext(ctx); ok && p != nil {
		actor = p.Subject
	}

	var reg registry.ContractRegistration
	if txErr := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		var e error
		reg, e = s.store.Transition(txCtx, tnt, registry.AdvanceInput{
			ID:     id,
			To:     to,
			Actor:  actor,
			Reason: reason,
		})
		return e
	}); txErr != nil {
		return zero, txErr
	}
	return reg, nil
}

// transitionErr classifies a transition failure into the typed-response bucket
// each endpoint maps to its own envelope (the Approve/Reject/Retire response sets
// are structurally identical; only the Go types differ).
type transitionErr int

const (
	errInternal   transitionErr = iota // undeclared 5xx (return raw err)
	errTenant                          // missing/invalid tenant → 403
	errNotFound                        // registration id absent → 404
	errConflict                        // illegal state transition → 409
	errValidation                      // missing/invalid advance input → 400
)

// classifyTransitionErr maps a store/tenant error to its transitionErr bucket and
// the underlying *errcode.Error for the wire body. A non-errcode or unrecognized
// error classifies as errInternal (nil body), surfaced as a framework 5xx.
func classifyTransitionErr(err error) (transitionErr, *errcode.Error) {
	var ce *errcode.Error
	if !errors.As(err, &ce) {
		return errInternal, nil
	}
	switch ce.Code {
	case errcode.ErrAuthForbidden:
		return errTenant, ce
	case errcode.ErrRegistrationNotFound:
		return errNotFound, ce
	case errcode.ErrRegistrationInvalidTransition:
		return errConflict, ce
	case errcode.ErrValidationFailed:
		return errValidation, ce
	default:
		return errInternal, nil
	}
}

// logTransitionFailure records an unexpected (undeclared 5xx) transition error
// server-side (observability.md §Warn=降级运行); the raw error never reaches the
// wire (the handler's WriteError fallback derives a redacted 5xx body).
func (s *Service) logTransitionFailure(ctx context.Context, op, id string, err error) {
	s.logger.WarnContext(ctx, "registryadmin: unexpected store error on contract transition",
		slog.String("op", op),
		slog.String("registration_id", id),
		slog.String("error", err.Error()),
	)
}

// toApproveData / toRejectData / toRetireData project the post-transition
// ContractRegistration onto each contract's generated wire DTO. State is the
// sealed RegistrationState spelling; timestamps are RFC3339 UTC. The projection is
// identical across the three; the DTO Go types differ per contract package.
func toApproveData(reg registry.ContractRegistration) *approve.ResponseData {
	return &approve.ResponseData{
		ID:        reg.ID,
		Kind:      reg.Kind,
		State:     reg.State.String(),
		Submitter: reg.Submitter,
		Approver:  reg.Approver,
		CreatedAt: reg.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: reg.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func toRejectData(reg registry.ContractRegistration) *reject.ResponseData {
	return &reject.ResponseData{
		ID:        reg.ID,
		Kind:      reg.Kind,
		State:     reg.State.String(),
		Submitter: reg.Submitter,
		Approver:  reg.Approver,
		CreatedAt: reg.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: reg.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func toRetireData(reg registry.ContractRegistration) *retire.ResponseData {
	return &retire.ResponseData{
		ID:        reg.ID,
		Kind:      reg.Kind,
		State:     reg.State.String(),
		Submitter: reg.Submitter,
		Approver:  reg.Approver,
		CreatedAt: reg.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: reg.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// Handler wires the three generated admin handlers behind the cell-derived
// authz.MethodPolicyResolver (contract-derived PDP gate per endpoint).
type Handler struct {
	approveH *approve.Handler
	rejectH  *reject.Handler
	retireH  *retire.Handler
}

// NewHandler builds the route handler. resolver is the cell-level
// authz.MethodPolicyResolver (cellgen-built from the three endpoints'
// endpoints.http.permission overlays), injected by the composition root.
func NewHandler(svc *Service, resolver authz.MethodPolicyResolver) *Handler {
	return &Handler{
		approveH: approve.NewHandler(svc, resolver),
		rejectH:  reject.NewHandler(svc, resolver),
		retireH:  retire.NewHandler(svc, resolver),
	}
}

// RegisterRoutes mounts all three admin contracts on mux.
func (h *Handler) RegisterRoutes(mux cell.RouteHandler) error {
	if err := h.approveH.RegisterRoutes(mux); err != nil {
		return err
	}
	if err := h.rejectH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.retireH.RegisterRoutes(mux)
}
