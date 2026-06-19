// Package registrywrite serves the runtime contract-registration submit contract
// http.registry.contract.submit.v1 (303-US6, #2237). Submit decodes a full
// contract declaration, runs the governance gate (US3 curated rule set), and on
// success persists the registration via the durable ports.Registry (US5). The
// submitter is the authenticated principal, never a body field.
package registrywrite

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/governance"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	submit "github.com/ghbvf/gocell/generated/contracts/http/registry/contract/submit/v1"
)

// const messages — MESSAGE-CONST-LITERAL-01: errcode.New message must be a literal.
const (
	msgGateValidationFailed = "contract declaration failed governance validation"
	msgGateTenantInvalid    = "request carries an invalid or missing tenant scope"
	msgGateUnavailable      = "governance validator is temporarily unavailable"
	msgTenantRequired       = "tenant scope required to submit a contract"
)

// Option configures a registrywrite Service.
type Option func(*Service)

// WithTxManager sets the CellTxManager for transactional guarantees (L1
// atomicity). Callers obtain the sealed marker via persistence.WrapForCell
// from a composition root.
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

// Service implements the generated submit.Service: it decodes the wire Request
// into a ContractMeta, runs the governance gate (US3), and on success persists
// via ports.Registry (US5).
//
// MVP boundary: ownerCell / endpoints / schemaRefs are used by the gate for
// governance validation only and are NOT persisted to the durable store in this
// slice. The store only records id / kind / payloadSchema / submitter. Full
// declaration persistence is a subsequent issue (decl-store activation).
type Service struct {
	store    ports.Registry               `gocell:"required"`
	gate     *governance.RegistrationGate `gocell:"required"`
	txRunner persistence.CellTxManager    `gocell:"required" gocellErr:"registrywrite: TxRunner required; use WithTxManager"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	logger   *slog.Logger
}

// NewService constructs the submit service. store, gate, and txRunner are
// required; validateRequired fail-fasts on nil. logger is optional.
func NewService(store ports.Registry, gate *governance.RegistrationGate, opts ...Option) (*Service, error) {
	s := &Service{
		store:  store,
		gate:   gate,
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

// Submit implements submit.Service:
//  1. Extract tenant from context (missing tenant → 400/403 per FR-002).
//  2. Extract authenticated principal as submitter (defensive: gate guarantees one).
//  3. Decode wire Request → ContractMeta + NormalizeRuntimeContract.
//  4. governance.RegistrationGate.Check (dry-run): denied → typed 400 with
//     per-finding details (MESSAGE-CONST-LITERAL-01 satisfied: msg is const,
//     runtime data is in WithDetails).
//  5. store.Create inside txRunner (L1 atomicity).
//  6. Project ContractRegistration → Submit201JSONResponse.
func (s *Service) Submit(ctx context.Context, req *submit.Request) (submit.SubmitResponseObject, error) {
	// Step 1: tenant extraction (fail-closed).
	tnt, tntErr := tenant.FromContext(ctx)
	if tntErr != nil {
		// Missing or invalid tenant is a client error (cell-patterns.md §Typed
		// response envelope): return typed 400, not a Go error.
		return submit.Submit400ErrorResponse{Body: *errcode.New( //nolint:nilerr // typed 4xx per cell-patterns §Typed response envelope
			errcode.KindInvalid, errcode.ErrValidationFailed, msgTenantRequired,
		)}, nil
	}

	// Step 2: submitter from authenticated principal.
	submitter := ""
	if p, ok := auth.FromContext(ctx); ok && p != nil {
		submitter = p.Subject
	} else {
		// Defensive: the registry:submit gate guarantees a principal in production.
		// Reaching here means an upstream auth-wiring anomaly.
		s.logger.WarnContext(ctx, "registrywrite: submit reached without an authenticated principal",
			"contract", "http.registry.contract.submit.v1")
	}

	// Step 3: decode Request → ContractMeta.
	meta := decodeRequestToMeta(req)
	metadata.NormalizeRuntimeContract(&meta)

	// Step 4: governance gate (dry-run check only; submitter handled separately).
	res := s.gate.Check(ctx, tnt, &meta)
	if !res.Allowed {
		return gateErrorResponse(res, submitter, s.logger, ctx)
	}

	// Step 5: persist via durable store in a transaction.
	var reg registry.ContractRegistration
	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		var storeErr error
		reg, storeErr = s.store.Create(txCtx, tnt, registry.SubmitInput{
			ID:            meta.ID,
			Kind:          meta.Kind,
			PayloadSchema: req.PayloadSchema,
			Submitter:     submitter,
		})
		return storeErr
	}); err != nil {
		return submitErrorResponse(err, s.logger, ctx)
	}

	return submit.Submit201JSONResponse{Data: toSubmitData(reg)}, nil
}

// decodeRequestToMeta converts the wire Request into a ContractMeta suitable
// for gate validation. Rich declaration fields (ownerCell / endpoints /
// schemaRefs / lifecycle) are mapped so the curated rule set (CH-01/03,
// FMT-01/08/09, REG-01) can evaluate the candidate.
func decodeRequestToMeta(req *submit.Request) metadata.ContractMeta {
	meta := metadata.ContractMeta{
		ID:               req.ID,
		Kind:             string(req.Kind),
		OwnerCell:        req.OwnerCell,
		Lifecycle:        string(req.Lifecycle),
		ConsistencyLevel: string(req.ConsistencyLevel),
		Transports:       req.Transports,
	}
	if req.Endpoints != nil {
		meta.Endpoints = metadata.EndpointsMeta{
			Server:           req.Endpoints.Server,
			Clients:          req.Endpoints.Clients,
			Publisher:        req.Endpoints.Publisher,
			ActorSubscribers: req.Endpoints.ActorSubscribers,
			Handler:          req.Endpoints.Handler,
			Invokers:         req.Endpoints.Invokers,
			Provider:         req.Endpoints.Provider,
			Readers:          req.Endpoints.Readers,
		}
	}
	if req.SchemaRefs != nil {
		meta.SchemaRefs = metadata.SchemaRefsMeta{
			Request:  req.SchemaRefs.Request,
			Response: req.SchemaRefs.Response,
		}
	}
	return meta
}

// gateErrorResponse maps a denied GovernanceGateResult to the submit
// contract's typed 4xx envelope. Per MESSAGE-CONST-LITERAL-01, the errcode
// message is always a const literal; structured bounded fields (rule code,
// field) flow through WithDetails typed channels. Free-form finding messages
// are kept server-side only (slog Warn) — never placed on the wire (S1).
func gateErrorResponse(
	res governance.GovernanceGateResult,
	submitter string,
	logger *slog.Logger,
	ctx context.Context,
) (submit.SubmitResponseObject, error) {
	switch res.Reason {
	case governance.ReasonValidationFailed(), governance.ReasonInvalidInput():
		// Log full findings (including Message) server-side as the primary diagnostic
		// channel (observability.md §日志 Warn=降级运行). Never forwarded to the wire.
		for i, f := range res.Result {
			logger.WarnContext(ctx, "registrywrite: contract declaration failed governance validation",
				slog.String("finding_rule", fmt.Sprintf("[%d] %s", i, string(f.Code))),
				slog.String("finding_message", fmt.Sprintf("[%d] %s", i, f.Message)),
				slog.String("finding_field", f.Field),
			)
		}
		var opts []errcode.Option
		for _, f := range res.Result {
			// Wire details carry only bounded structured fields (MESSAGE-CONST-LITERAL-01,
			// S1): rule code and field path. Free-form Message is never sent to clients.
			opts = append(opts,
				errcode.WithDetails(
					errcode.PublicString("rule", string(f.Code)),
					errcode.PublicString("field", f.Field),
				),
			)
		}
		if res.Reason == governance.ReasonInvalidInput() && submitter == "" {
			opts = append(opts, errcode.WithDetails(
				errcode.PublicString("field", "submitter"),
			))
		}
		return submit.Submit400ErrorResponse{Body: *errcode.New(
			errcode.KindInvalid, errcode.ErrValidationFailed, msgGateValidationFailed, opts...,
		)}, nil
	case governance.ReasonTenantInvalid():
		return submit.Submit400ErrorResponse{Body: *errcode.New(
			errcode.KindInvalid, errcode.ErrValidationFailed, msgGateTenantInvalid,
		)}, nil
	default:
		// ReasonValidatorUnavailable or unknown: 5xx degraded-mode, log and bubble.
		// ErrServiceUnavailable is the correct 503-class code (KindUnavailable →
		// HTTP 503); ErrRegistrationRepoQuery (repo query failure) would misrepresent
		// a governance validator outage as a storage error.
		logger.WarnContext(ctx, "registrywrite: governance validator unavailable or unknown gate reason",
			slog.String("reason", res.Reason.String()))
		return nil, errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, msgGateUnavailable)
	}
}

// submitErrorResponse maps a store.Create error to the contract's typed 4xx
// envelope: duplicate id → 409, validation failure → 400. Anything else
// bubbles as an undeclared framework 5xx (cell-patterns.md §Typed response
// envelope). Unknown 5xx errors are logged server-side (observability.md §Warn).
func submitErrorResponse(err error, logger *slog.Logger, ctx context.Context) (submit.SubmitResponseObject, error) {
	var ce *errcode.Error
	if errors.As(err, &ce) {
		switch ce.Code {
		case errcode.ErrRegistrationDuplicate:
			return submit.Submit409ErrorResponse{Body: *ce}, nil
		case errcode.ErrValidationFailed:
			return submit.Submit400ErrorResponse{Body: *ce}, nil
		}
	}
	// Unknown store error: 5xx degraded-mode (observability.md §Warn=降级运行).
	logger.WarnContext(ctx, "registrywrite: unexpected store error on contract create",
		slog.String("error", err.Error()))
	return nil, err
}

// toSubmitData projects a ContractRegistration onto the generated wire DTO.
// State is the sealed RegistrationState spelling; timestamps are RFC3339 UTC.
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
