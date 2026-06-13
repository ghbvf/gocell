// Package policymanage implements the policy-manage slice: CRUD operations for
// ABAC Policies, publishing event.policy.updated.v1 on every mutation
// (consistency level L2: local transaction + outbox publish).
package policymanage

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/scopedtx"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/auth"
)

// TopicPolicyUpdated is the stable outbox topic for event.policy.updated.v1,
// re-exported here so test files in this package can reference it without
// importing internal/dto directly.
const TopicPolicyUpdated = dto.TopicPolicyUpdated

// policySort defines the stable sort order for the in-memory list operation.
// Sorted by ID ascending so that cursor pagination is deterministic.
// Effectively immutable: do not append or replace elements at runtime;
// query.Sort and query.ApplyCursor both read this value concurrently.
var policySort = []query.SortColumn{
	{Name: "id", Direction: query.SortASC},
}

// Option configures a policymanage Service.
type Option func(*Service)

// WithEmitter sets the event emitter.
func WithEmitter(e outbox.CellEmitter) Option {
	return func(s *Service) {
		if e != nil {
			s.emitter = e
		}
	}
}

// WithTxManager sets the CellTxManager for transactional guarantees (L2
// atomicity). Callers obtain the sealed marker via persistence.WrapForCell
// from a composition root.
func WithTxManager(tx persistence.CellTxManager) Option {
	return func(s *Service) {
		if tx != nil {
			s.txRunner = tx
		}
	}
}

// Service implements ABAC policy write business logic for the policymanage
// slice. All mutations (Create/Update/Delete) run inside a transaction with
// an outbox emit, providing L2 OutboxFact atomicity.
type Service struct {
	policyRepo ports.PolicyRepository    `gocell:"required"`
	txRunner   persistence.CellTxManager `gocell:"required" gocellErr:"policymanage: TxRunner required; use WithTxManager"`                                                //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	codec      *query.CursorCodec        `gocell:"required" gocellKind:"KindInternal" gocellCode:"ErrCellMissingCodec" gocellErr:"policymanage: cursor codec is required"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	emitter    outbox.CellEmitter
	clk        clock.Clock
	logger     *slog.Logger
	runMode    query.RunMode
}

// NewService creates a policymanage Service.
// clk must be non-nil; pass clock.Real() in production and clockmock.New() in tests.
// policyRepo and codec must be non-nil; TxRunner must be provided via WithTxManager;
// nil txRunner is rejected to prevent silent loss of L2 atomicity guarantees.
// codec signs the list cursor (opaque, tamper-evident); runMode selects fail-closed
// (prod) vs stale-key fallback (demo) cursor handling, matching rbaccheck.
func NewService(
	clk clock.Clock,
	policyRepo ports.PolicyRepository,
	codec *query.CursorCodec,
	logger *slog.Logger,
	runMode query.RunMode,
	opts ...Option,
) (*Service, error) {
	clock.MustHaveClock(clk, "policymanage.NewService")
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		policyRepo: policyRepo,
		codec:      codec,
		emitter:    outbox.DemoCellEmitter(),
		clk:        clk,
		logger:     logger,
		runMode:    runMode,
	}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// CreateInput holds parameters for creating a policy.
type CreateInput struct {
	Name        string
	Description string
	Rules       []abac.Rule
}

// UpdateInput holds parameters for updating a policy.
type UpdateInput struct {
	ID              string
	Name            string
	Description     string
	Rules           []abac.Rule
	ExpectedVersion int
}

// Create creates a new policy and publishes a policy.updated event.
func (s *Service) Create(ctx context.Context, input CreateInput) (*abac.Policy, error) {
	actor, err := actorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	tid, err := tenant.FromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("policymanage: create: tenant: %w", err)
	}

	p := buildPolicy(tid, input.Name, input.Description, input.Rules)
	if err := p.Validate(); err != nil {
		return nil, err
	}

	var created *abac.Policy
	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		var err error
		created, err = s.policyRepo.Create(txCtx, tid, p)
		if err != nil {
			return fmt.Errorf("policymanage: create: %w", err)
		}
		return s.emitPolicyUpdated(txCtx, created.ID, created.Version, dto.PolicyActionCreated, actor)
	}); err != nil {
		return nil, err
	}

	s.logger.Info("policy created", slog.String("policy_id", created.ID))
	return created, nil
}

// Update modifies an existing policy and publishes a policy.updated event.
// Returns ErrVersionConflict (409) if expectedVersion does not match.
func (s *Service) Update(ctx context.Context, input UpdateInput) (*abac.Policy, error) {
	actor, err := actorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	tid, err := tenant.FromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("policymanage: update: tenant: %w", err)
	}

	patch := &abac.Policy{
		ID:          input.ID,
		TenantID:    tid,
		Name:        input.Name,
		Description: input.Description,
		Rules:       input.Rules,
	}
	if err := patch.Validate(); err != nil {
		return nil, err
	}

	var updated *abac.Policy
	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		var err error
		updated, err = s.policyRepo.Update(txCtx, tid, input.ID, input.ExpectedVersion, patch)
		if err != nil {
			return fmt.Errorf("policymanage: update: %w", err)
		}
		return s.emitPolicyUpdated(txCtx, updated.ID, updated.Version, dto.PolicyActionUpdated, actor)
	}); err != nil {
		return nil, err
	}

	s.logger.Info("policy updated", slog.String("policy_id", updated.ID), slog.Int("version", updated.Version))
	return updated, nil
}

// Delete removes a policy and publishes a policy.updated event with action=deleted.
// Returns ErrVersionConflict (409) if expectedVersion does not match.
func (s *Service) Delete(ctx context.Context, id string, expectedVersion int) (*abac.Policy, error) {
	actor, err := actorFromContext(ctx)
	if err != nil {
		return nil, err
	}
	tid, err := tenant.FromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("policymanage: delete: tenant: %w", err)
	}

	var deleted *abac.Policy
	if err := s.runInTx(ctx, func(txCtx context.Context) error {
		var err error
		deleted, err = s.policyRepo.Delete(txCtx, tid, id, expectedVersion)
		if err != nil {
			return fmt.Errorf("policymanage: delete: %w", err)
		}
		return s.emitPolicyUpdated(txCtx, deleted.ID, deleted.Version, dto.PolicyActionDeleted, actor)
	}); err != nil {
		return nil, err
	}

	s.logger.Info("policy deleted", slog.String("policy_id", id))
	return deleted, nil
}

// Get returns a single policy by ID for the caller's tenant. The read runs
// inside scopedtx.Do so the PG RLS app.tenant_id GUC is set for the policies
// table — a bare-pool read under the restricted app-serving role would
// fail-closed to 0 rows (404).
func (s *Service) Get(ctx context.Context, id string) (*abac.Policy, error) {
	tid, err := tenant.FromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("policymanage: get: tenant: %w", err)
	}
	return scopedtx.Do(ctx, s.txRunner, tid, func(txCtx context.Context) (*abac.Policy, error) {
		return s.policyRepo.GetByID(txCtx, tid, id)
	})
}

// List returns a paginated page of policies for the caller's tenant.
//
// The underlying repo returns all policies for the tenant (no DB-side
// pagination); the Fetch closure sorts and applies the decoded keyset cursor
// in-memory via pkg/query, satisfying the "列表强制分页" constraint (max 500,
// default 50). query.ExecutePagedQuery owns cursor decode/encode and validation:
// the cursor is an opaque, HMAC-signed token (query.CursorCodec) — a tampered or
// stale token returns ErrCursorInvalid (→ 400 in prod / first-page fallback in
// demo), the same idiom as rbaccheck and the other accesscore list endpoints.
//
// The read runs inside scopedtx.Do so the PG RLS app.tenant_id GUC is set for
// the policies table (fail-closed: a read without the scoped tx returns 0 rows
// under the restricted app-serving role).
func (s *Service) List(ctx context.Context, pageReq query.PageParams) (query.PageResult[*abac.Policy], error) {
	tid, err := tenant.FromContext(ctx)
	if err != nil {
		return query.PageResult[*abac.Policy]{}, fmt.Errorf("policymanage: list: tenant: %w", err)
	}
	txRunner := s.txRunner
	qctx := query.QueryContext("endpoint", "policy-list")
	return query.ExecutePagedQuery(ctx, query.PagedQueryConfig[*abac.Policy]{
		Codec:      s.codec,
		PageParams: pageReq,
		Sort:       policySort,
		QueryCtx:   qctx,
		Fetch: func(fetchCtx context.Context, params query.ListParams) ([]*abac.Policy, error) {
			return scopedtx.Do(fetchCtx, txRunner, tid, func(txCtx context.Context) ([]*abac.Policy, error) {
				all, listErr := s.policyRepo.ListByTenant(txCtx, tid)
				if listErr != nil {
					return nil, fmt.Errorf("policymanage: list: %w", listErr)
				}
				query.Sort(all, policySort, comparePolicyField)
				return query.ApplyCursor(all, params, policyFieldValue)
			})
		},
		Extract:     func(p *abac.Policy) []any { return []any{p.ID} },
		OnCursorErr: query.LogCursorError(s.logger, "policymanage"),
		RunMode:     s.runMode,
	})
}

// runInTx wraps fn in a transaction. txRunner is guaranteed non-nil by the
// constructor's fail-fast check.
func (s *Service) runInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.txRunner.RunInTx(ctx, fn)
}

// actorFromContext extracts the admin actor from the request context.
// Policy write paths are admin-only; an empty Subject is a wiring error.
func actorFromContext(ctx context.Context) (string, error) {
	p, ok := auth.FromContext(ctx)
	if !ok || p.Subject == "" {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized,
			"policymanage: actor required; admin auth must be present")
	}
	return p.Subject, nil
}

// buildPolicy constructs an *abac.Policy from flat input fields.
// ID is server-generated as "pol-" + UUID.
func buildPolicy(tid tenant.TenantID, name, description string, rules []abac.Rule) *abac.Policy {
	return &abac.Policy{
		ID:          "pol-" + uuid.NewString(),
		TenantID:    tid,
		Name:        name,
		Description: description,
		Rules:       rules,
	}
}

// emitPolicyUpdated publishes event.policy.updated.v1 to the outbox within
// the current transaction context. Called by Create, Update, and Delete.
// Metadata-only: no rule bodies on the bus.
func (s *Service) emitPolicyUpdated(ctx context.Context, policyID string, version int, action, actor string) error {
	payload := dto.PolicyUpdated{
		PolicyID: policyID,
		Version:  version,
		Action:   action,
		ActorID:  actor,
	}
	return outbox.Emit(ctx, s.clk, s.emitter, TopicPolicyUpdated, payload)
}

// comparePolicyField compares a single named field of two *abac.Policy entries.
// Used by query.Sort.
func comparePolicyField(a, b *abac.Policy, field string) int {
	switch field {
	case "id":
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	default:
		return 0
	}
}

// policyFieldValue extracts a cursor-comparable field value from a *abac.Policy.
// Used by query.ApplyCursor.
func policyFieldValue(item *abac.Policy, field string) any {
	switch field {
	case "id":
		return item.ID
	default:
		return ""
	}
}
