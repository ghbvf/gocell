// Package registryread serves the runtime contract-registration list contract
// http.registry.contract.list.v1 (303-US4, #2235) over the durable
// ports.Registry store (303-US5, #2236). It returns a tenant-scoped,
// id-ordered, HMAC-cursor-paginated view of all registrations. The route is
// gated by the registry:read permission.
//
// # Durable store + tenant scope (303-US6, #2237)
//
// List runs ports.Registry.List inside a tenant-scoped transaction via
// scopedread.Do. The store receives the tenant extracted from the request context
// so cross-tenant rows are structurally unreachable. The mem implementation
// (NewRegistry) is used in the demo/no-PG topology; the PG implementation is wired
// at composition root.
//
// # RLS-safe scoped reads (#2392)
//
// Under migration 066 FORCE ROW LEVEL SECURITY a SELECT on contract_registrations
// returns 0 rows unless the app.tenant_id GUC is set, and that GUC is injected
// (SET LOCAL) only inside TxManager.RunInTx. List therefore wraps its read in
// scopedread.Do (tenant.WithScope + RunInTx) — the sole production caller of
// tenant.WithScope in registrycore, pinned by TENANT-TXSCOPE-WRITE-CALLER-01. The
// mem store ignores the ambient tx; the PG serving role reads correctly under RLS.
package registryread

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/ghbvf/gocell/corecells/registrycore/internal/ports"
	"github.com/ghbvf/gocell/corecells/registrycore/internal/scopedread"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/projection"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	list "github.com/ghbvf/gocell/generated/contracts/http/registry/contract/list/v1"
)

const msgInvalidStateParam = "invalid state query parameter"

// registrySort is the fixed keyset sort: id ASC (only supported column).
var registrySort = []query.SortColumn{
	{Name: "id", Direction: query.SortASC},
}

// Service implements list.Service over the durable ports.Registry store.
// It sources tenant from the request context and delegates pagination to
// query.ExecutePagedQuery with the shared HMAC CursorCodec.
type Service struct {
	store    ports.Registry            `gocell:"required"`
	txRunner persistence.CellTxManager `gocell:"required" gocellErr:"registryread: TxRunner required (RLS reads run in a tenant-scoped tx)"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	codec    *query.CursorCodec        `gocell:"required"`
	runMode  query.RunMode
	logger   *slog.Logger
}

// NewService constructs the list service. store, txRunner, and codec are required;
// runMode controls cursor-decode fail-open vs fail-closed (pass
// query.RunModeForDemo(true) for demo/in-mem topology, RunModeProd otherwise);
// validateRequired returns a structured error on nil so cell Init() can
// propagate it instead of panicking. logger is optional (nil → slog.Default()).
// txRunner scopes each read in a tenant tx (scopedread.Do) so PG FORCE RLS sees
// SET LOCAL app.tenant_id; demo topology passes outbox.DemoCellTxManager().
func NewService(
	store ports.Registry,
	txRunner persistence.CellTxManager,
	codec *query.CursorCodec,
	runMode query.RunMode,
	logger *slog.Logger,
) (*Service, error) {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		store:    store,
		txRunner: txRunner,
		codec:    codec,
		runMode:  runMode,
		logger:   logger,
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// List implements list.Service: a tenant-scoped, id-ordered, cursor-paginated
// view of contract registrations. Tenant is sourced from ctx via
// tenant.FromContext (fail-closed: missing tenant → 403). Cursor pagination
// uses an HMAC-signed opaque token so cursors are tamper-evident and cannot
// be replayed across endpoints.
//
// The optional state query parameter narrows results to a single registration
// state. An empty state means all states. The value set is not a queryParam enum
// (metadata.ParamSchema models no enum), so an unrecognized state value is
// rejected here via registry.ParseState (→ 400) — this is the sole membership guard.
func (s *Service) List(ctx context.Context, req *list.Request) (list.ListResponseObject, error) {
	t, err := tenant.FromContext(ctx)
	if err != nil {
		var ce *errcode.Error
		if errors.As(err, &ce) {
			return list.List403ErrorResponse{Body: *ce}, nil
		}
		return nil, err
	}

	filter, filterErr := parseStateFilter(req.State)
	if filterErr != nil {
		var ce *errcode.Error
		errors.As(filterErr, &ce)
		return list.List400ErrorResponse{Body: *ce}, nil
	}

	pageReq := query.PageParams{
		Cursor: req.Cursor,
		Limit:  int(req.Limit),
	}

	// RLS-safe read (#2392): run the paged query inside a tenant-scoped tx so the
	// PG serving role sees SET LOCAL app.tenant_id = t before the SELECT (else FORCE
	// RLS → 0 rows). The mem store ignores the ambient tx.
	//
	// cursor-state binding: QueryCtx includes the state value so a cursor issued
	// for state=A cannot be replayed under state=B (scope mismatch → cursor invalid).
	result, err := scopedread.Do(ctx, s.txRunner, t, func(txCtx context.Context) (query.PageResult[registry.ContractRegistration], error) {
		return query.ExecutePagedQuery(txCtx, query.PagedQueryConfig[registry.ContractRegistration]{
			Codec:      s.codec,
			PageParams: pageReq,
			Sort:       registrySort,
			QueryCtx:   query.QueryContext("endpoint", "registryread", "state", req.State),
			Fetch: func(ctx context.Context, params query.ListParams) ([]registry.ContractRegistration, error) {
				return s.store.List(ctx, t, params, filter)
			},
			Extract: func(r registry.ContractRegistration) []any {
				return []any{r.ID}
			},
			OnCursorErr: query.LogCursorError(s.logger, "registryread"),
			RunMode:     s.runMode,
		})
	})
	if err != nil {
		return nil, err
	}

	rows := make([]map[string]any, 0, len(result.Items))
	for _, r := range result.Items {
		rows = append(rows, toListItem(r).ToMap())
	}
	// identity projection (epic #1337 PR-12); masking obligation source becomes
	// the ABAC Decision in PR-10.
	data, err := projection.NewProjectionList(authz.IdentityFieldMask(), rows)
	if err != nil {
		return nil, err
	}
	return list.List200JSONResponse{
		Data:       data,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	}, nil
}

// toListItem projects a ContractRegistration onto the generated wire DTO. State
// is the sealed RegistrationState spelling; timestamps are RFC3339 UTC.
// Approver and PayloadSchema are nullable (*string): nil marshals to JSON null,
// keeping the key present in every row for the full-column-set masking funnel.
func toListItem(reg registry.ContractRegistration) list.ResponseDataItem {
	var approver *string
	if reg.Approver != "" {
		approver = &reg.Approver
	}
	var payloadSchema *string
	if reg.PayloadSchema != "" {
		payloadSchema = &reg.PayloadSchema
	}
	return list.ResponseDataItem{
		ID:            reg.ID,
		Kind:          reg.Kind,
		State:         reg.State.String(),
		Submitter:     reg.Submitter,
		Approver:      approver,
		PayloadSchema: payloadSchema,
		CreatedAt:     reg.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     reg.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// parseStateFilter converts the optional state wire value from the request into a
// ports.ListFilter. An empty string (omitted param) returns a zero filter (no
// filter). An unrecognized value returns a 400-worthy error with structured
// details (field / value / allowed) sourced from the canonical registry.StateNames
// closed set. The state value set is not declared as a queryParam enum
// (metadata.ParamSchema models no enum), so this registry.ParseState membership
// check is the sole guard for an invalid state value (→ 400).
func parseStateFilter(stateParam string) (ports.ListFilter, error) {
	if stateParam == "" {
		return ports.ListFilter{}, nil
	}
	state, ok := registry.ParseState(stateParam)
	if !ok {
		allowed := strings.Join(registry.StateNames(), ", ")
		return ports.ListFilter{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			msgInvalidStateParam,
			errcode.WithDetails(
				errcode.PublicString("field", "state"),
				errcode.PublicString("value", stateParam),
				errcode.PublicString("allowed", allowed),
			),
		)
	}
	return ports.ListFilter{State: state}, nil
}

// Handler wires the generated list.Handler with the registry:read PDP gate.
type Handler struct {
	h *list.Handler
}

// NewHandler builds the route handler. Authorization is contract-derived
// (#2205, 303-US7 migration): the cell-level authz.MethodPolicyResolver is built
// by cellgen from endpoints.http.permission (registry:read) and injected by the
// composition root, replacing the previously hand-wired
// auth.RequirePermission(authz.PermRegistryRead()) policy.
func NewHandler(svc *Service, resolver authz.MethodPolicyResolver) *Handler {
	return &Handler{h: list.NewHandler(svc, resolver)}
}

// RegisterRoutes mounts the contract on mux via the generated handler.
func (h *Handler) RegisterRoutes(mux cell.RouteHandler) error {
	return h.h.RegisterRoutes(mux)
}
