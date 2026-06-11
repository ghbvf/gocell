package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/adapters/postgres/internal/pgexec"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Compile-time assertion: PGPolicyRepo implements ports.PolicyRepository.
var _ ports.PolicyRepository = (*PGPolicyRepo)(nil)

const msgPolicyInvalidTenant = "policy_repo: invalid tenant"

// PGPolicyRepo is the cell-private PostgreSQL implementation of
// ports.PolicyRepository (#1346 PR-8). It reads/writes the `policies` table
// (migration 059), storing each policy's rule list as a string-coded JSONB
// document (see policy_codec.go).
//
// Tenant scoping is application-level (every method carries tenant.TenantID and
// every query includes `WHERE tenant_id = $N`), exactly as the mem store
// partitions by tenant — this is what the cross-implementation conformance suite
// asserts. The FORCE ROW LEVEL SECURITY policy on the table (migration 059) is an
// independent DB-kernel backstop, not the primary isolation mechanism.
//
// The txRunner field is a construction-time policy declaration (fail-fast on a
// missing TxRunner), mirroring PGUserRepo/PGRoleRepo; the methods are
// single-statement and pick up any ambient pgx.Tx from ctx via pgexec, so they
// participate in a caller's tenant-scoped transaction when one is open (the PR-7
// evaluator reads policies inside a scoped tx) and fall through to the pool
// otherwise.
type PGPolicyRepo struct {
	db       pgexec.PGExecutor
	txRunner persistence.TxRunner
	clock    clock.Clock
}

// NewPGPolicyRepo constructs a PGPolicyRepo. Fails fast on nil dependencies.
func NewPGPolicyRepo(
	pool *pgxpool.Pool,
	txRunner persistence.TxRunner,
	clk clock.Clock,
) (*PGPolicyRepo, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore.NewPGPolicyRepo: pool must not be nil")
	}
	if validation.IsNilInterface(txRunner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore.NewPGPolicyRepo: txRunner must not be nil")
	}
	if validation.IsNilInterface(clk) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore.NewPGPolicyRepo: clock must not be nil")
	}
	return &PGPolicyRepo{
		db:       pgexec.New(pool),
		txRunner: txRunner,
		clock:    clk,
	}, nil
}

const (
	// upsertPolicySQL: $1=tenant_id, $2=id, $3=name, $4=description, $5=rules,
	// $6=now (created_at AND updated_at on insert). ON CONFLICT preserves
	// created_at and advances updated_at — Save is an upsert by the composite PK.
	upsertPolicySQL = `
INSERT INTO policies (tenant_id, id, name, description, rules, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $6)
ON CONFLICT (tenant_id, id) DO UPDATE
  SET name        = EXCLUDED.name,
      description  = EXCLUDED.description,
      rules        = EXCLUDED.rules,
      updated_at   = EXCLUDED.updated_at`

	// selectPolicyByIDSQL: $1=tenant_id, $2=id. TenantID is reconstructed from
	// the query parameter (the predicate guarantees the row belongs to t).
	selectPolicyByIDSQL = `
SELECT id, name, description, rules
FROM policies
WHERE tenant_id = $1 AND id = $2`

	// listPoliciesByTenantSQL: $1=tenant_id.
	listPoliciesByTenantSQL = `
SELECT id, name, description, rules
FROM policies
WHERE tenant_id = $1`

	// deletePolicySQL: $1=tenant_id, $2=id.
	deletePolicySQL = `DELETE FROM policies WHERE tenant_id = $1 AND id = $2`

	// policyRepoReadySQL is the readiness probe — table reachability only. The
	// `WHERE false` predicate returns zero rows without a scan (mirrors the
	// session/ledger PG stores).
	policyRepoReadySQL = `SELECT 1 FROM policies WHERE false`
)

// Save persists or replaces the policy within the tenant (upsert by the
// composite PK). Validates the tenant identity, checks p.TenantID == t
// (programmer-error guard), then runs Policy.Validate before encoding and
// writing. Returns KindInvalid for any structural violation.
func (r *PGPolicyRepo) Save(ctx context.Context, t tenant.TenantID, p *abac.Policy) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgPolicyInvalidTenant, err)
	}
	if p == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "policy_repo: policy must not be nil")
	}
	if p.TenantID != t {
		// A TenantID mismatch is a programmer error, not user input — the tenant
		// identifiers go to the server log only (WithInternal), never the wire,
		// so a future HTTP caller (PR-9) cannot read back an isolation-domain id.
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "policy_repo: policy TenantID does not match the provided tenant",
			errcode.WithInternal(
				errcode.InternalAttr("policyTenantId", string(p.TenantID)),
				errcode.InternalAttr("tenantId", string(t)),
			))
	}
	if err := p.Validate(); err != nil {
		return err
	}
	rulesJSON, err := marshalRules(p.Rules)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "policy_repo: marshal rules", err)
	}
	now := r.clock.Now()
	if _, err := r.db.Exec(ctx, upsertPolicySQL, string(t), p.ID, p.Name, p.Description, rulesJSON, now); err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "policy_repo: save", err)
	}
	return nil
}

// GetByID returns the policy identified by id within the tenant. Returns
// KindNotFound when the policy does not exist in t (the WHERE tenant_id=$1 AND
// id=$2 predicate collapses "absent" and "other tenant" into a single
// pgx.ErrNoRows, so no cross-tenant existence leaks).
func (r *PGPolicyRepo) GetByID(ctx context.Context, t tenant.TenantID, id string) (*abac.Policy, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgPolicyInvalidTenant, err)
	}
	row := r.db.QueryRow(ctx, selectPolicyByIDSQL, string(t), id)
	p, err := scanPolicy(row, t)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, notFoundPolicy(id)
		}
		var ec *errcode.Error
		if errors.As(err, &ec) && ec.Code == errcode.ErrPGSchemaShape {
			return nil, err
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "policy_repo: get-by-id", err)
	}
	return p, nil
}

// ListByTenant returns all policies owned by the tenant. Returns an empty
// (non-nil) slice when the tenant has no policies.
func (r *PGPolicyRepo) ListByTenant(ctx context.Context, t tenant.TenantID) ([]*abac.Policy, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgPolicyInvalidTenant, err)
	}
	rows, err := r.db.Query(ctx, listPoliciesByTenantSQL, string(t))
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "policy_repo: list-by-tenant", err)
	}
	defer rows.Close()

	result := make([]*abac.Policy, 0)
	for rows.Next() {
		p, scanErr := scanPolicy(rows, t)
		if scanErr != nil {
			// Preserve ErrPGSchemaShape (corrupt/forward-incompatible rules row)
			// instead of flattening it to ErrInternal — ListByTenant is the
			// evaluator's hot path, so the decode-failure classification must
			// survive here exactly as it does in GetByID.
			var ec *errcode.Error
			if errors.As(scanErr, &ec) && ec.Code == errcode.ErrPGSchemaShape {
				return nil, scanErr
			}
			return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "policy_repo: scan policy", scanErr)
		}
		result = append(result, p)
	}
	if rows.Err() != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "policy_repo: rows err", rows.Err())
	}
	return result, nil
}

// Delete removes the policy identified by id from the tenant. Returns
// KindNotFound when the policy does not exist in t.
func (r *PGPolicyRepo) Delete(ctx context.Context, t tenant.TenantID, id string) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgPolicyInvalidTenant, err)
	}
	tag, err := r.db.Exec(ctx, deletePolicySQL, string(t), id)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "policy_repo: delete", err)
	}
	if tag.RowsAffected() == 0 {
		return notFoundPolicy(id)
	}
	return nil
}

// RepoReady verifies that the policies table is reachable via a cheap
// non-transactional probe (`SELECT 1 FROM policies WHERE false` returns zero rows
// without a scan; success means the relation is reachable). Under FORCE RLS with
// no app.tenant_id GUC set the predicate is already empty, so readiness does not
// depend on a tenant scope and the fail-closed isolation stays intact. Errors are
// wrapped through errcode for uniform classification (mirrors the session/ledger
// PG stores). Registered into the cell-level readiness probe via the composite
// RepoProber in cell_init (#1346 PR-8, T8.4).
func (r *PGPolicyRepo) RepoReady(ctx context.Context) error {
	if _, err := r.db.Exec(ctx, policyRepoReadySQL); err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "policy_repo: readiness probe", err)
	}
	return nil
}

// policyRowScanner is satisfied by both pgx.Row (QueryRow) and pgx.Rows (Query),
// letting GetByID and ListByTenant share one scan path.
type policyRowScanner interface {
	Scan(dest ...any) error
}

// scanPolicy scans a single policy row and reconstructs the aggregate. TenantID
// is supplied by the caller (the WHERE predicate guarantees the row belongs to
// t). The decoded policy is re-validated defensively: a row that no longer
// satisfies Policy.Validate (e.g. a forward-incompatible rule shape) surfaces as
// ErrPGSchemaShape rather than flowing a corrupt aggregate into the evaluator.
func scanPolicy(s policyRowScanner, t tenant.TenantID) (*abac.Policy, error) {
	var id, name, description string
	var rulesJSON []byte
	if err := s.Scan(&id, &name, &description, &rulesJSON); err != nil {
		return nil, err
	}
	rules, err := unmarshalRules(rulesJSON)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrPGSchemaShape, "policy_repo: decode rules", err)
	}
	p := &abac.Policy{
		ID:          id,
		TenantID:    t,
		Name:        name,
		Description: description,
		Rules:       rules,
	}
	if err := p.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrPGSchemaShape, "policy_repo: reconstructed policy invalid", err)
	}
	return p, nil
}

// notFoundPolicy returns a KindNotFound error matching the mem store's shape so
// the conformance suite asserts a single error code across implementations.
func notFoundPolicy(id string) error {
	return errcode.New(errcode.KindNotFound, errcode.ErrAuthPolicyNotFound, "policy not found",
		errcode.WithCategory(errcode.CategoryDomain),
		errcode.WithInternal(errcode.InternalAttr("policy_id", id)))
}
