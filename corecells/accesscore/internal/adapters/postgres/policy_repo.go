package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/adapters/postgres/internal/pgexec"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/state/cas"
)

// Compile-time assertion: PGPolicyRepo implements ports.PolicyRepository.
var _ ports.PolicyRepository = (*PGPolicyRepo)(nil)

// msgPolicyInvalidTenant is the single-source error message shared via the
// ports package (#6). Unexported local alias for conciseness.
const msgPolicyInvalidTenant = ports.MsgInvalidTenant

// pgConflictCode is the PostgreSQL SQLSTATE for unique-constraint violations.
const pgConflictCode = "23505"

// PGPolicyRepo is the cell-private PostgreSQL implementation of
// ports.PolicyRepository (#1346 PR-8, #1347 PR-9). It reads/writes the
// `policies` table (migrations 059+060), storing each policy's rule list as a
// string-coded JSONB document (see policy_codec.go) and an integer `version`
// column for optimistic-concurrency control.
//
// Tenant scoping is application-level (every method carries tenant.TenantID and
// every query includes `WHERE tenant_id = $N`), exactly as the mem store
// partitions by tenant — this is what the cross-implementation conformance suite
// asserts.
//
// Write surface (PR-9):
//   - Create: plain INSERT; surfaces PG unique-constraint violation as
//     ErrAuthPolicyDuplicate (KindConflict).
//   - Update: CAS UPDATE ... version=version+1 ... WHERE tenant_id AND id AND
//     version=$expected. 0 rows affected → disambiguate not-found vs
//     version-mismatch with a follow-up existence check.
//   - Delete: CAS DELETE ... WHERE tenant_id AND id AND version=$expected,
//     returning the deleted row. 0 rows affected → same disambiguation.
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
	// insertPolicySQL: $1=tenant_id, $2=id, $3=name, $4=description, $5=rules,
	// $6=now (created_at AND updated_at). version is set to 1 (DEFAULT).
	insertPolicySQL = `
INSERT INTO policies (tenant_id, id, name, description, rules, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $6)`

	// updatePolicyCASSQL: CAS update. $1=name, $2=description, $3=rules,
	// $4=now (updated_at), $5=tenant_id, $6=id, $7=expectedVersion.
	// Returns the new version on success (zero rows → not-found or version
	// mismatch, disambiguated by a follow-up existence check).
	updatePolicyCASSQL = `
UPDATE policies
   SET name        = $1,
       description = $2,
       rules       = $3,
       updated_at  = $4,
       version     = version + 1
 WHERE tenant_id = $5
   AND id        = $6
   AND version   = $7
RETURNING version`

	// deletePolicyCASSQL: CAS delete. $1=tenant_id, $2=id, $3=expectedVersion.
	// Returns the deleted row's columns so the caller can reconstruct the
	// deleted Policy (for event emission). Zero rows → not-found or version
	// mismatch, disambiguated by an existence check.
	deletePolicyCASSQL = `
DELETE FROM policies
 WHERE tenant_id = $1
   AND id        = $2
   AND version   = $3
RETURNING id, name, description, rules, version`

	// selectPolicyByIDSQL: $1=tenant_id, $2=id. TenantID is reconstructed from
	// the query parameter (the predicate guarantees the row belongs to t).
	selectPolicyByIDSQL = `
SELECT id, name, description, rules, version
FROM policies
WHERE tenant_id = $1 AND id = $2`

	// listPoliciesByTenantSQL: $1=tenant_id.
	listPoliciesByTenantSQL = `
SELECT id, name, description, rules, version
FROM policies
WHERE tenant_id = $1`

	// existsPolicySQL: lightweight existence probe used to disambiguate 0-row
	// CAS outcomes (not-found vs version-mismatch). $1=tenant_id, $2=id.
	existsPolicySQL = `SELECT 1 FROM policies WHERE tenant_id = $1 AND id = $2`

	// policyRepoReadySQL is the readiness probe — table reachability only. The
	// `WHERE false` predicate returns zero rows without a scan (mirrors the
	// session/ledger PG stores).
	policyRepoReadySQL = `SELECT 1 FROM policies WHERE false`
)

// Create inserts a new policy for the tenant. Validates the tenant identity,
// checks p.TenantID == t (programmer-error guard), then runs Policy.Validate
// before encoding and writing. Version is set to 1 by the INSERT DEFAULT.
// Returns the persisted clone with Version=1 set (symmetry with Update/Delete).
// Returns ErrAuthPolicyDuplicate (KindConflict) when the (tenant_id, id)
// composite PK already exists.
func (r *PGPolicyRepo) Create(ctx context.Context, t tenant.TenantID, p *abac.Policy) (*abac.Policy, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgPolicyInvalidTenant, err)
	}
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "policy_repo: policy must not be nil")
	}
	if p.TenantID != t {
		return nil, errcode.New(
			errcode.KindInvalid, errcode.ErrValidationFailed,
			"policy_repo: policy TenantID does not match the provided tenant",
			errcode.WithInternal(
				errcode.InternalAttr("policyTenantId", string(p.TenantID)),
				errcode.InternalAttr("tenantId", string(t)),
			))
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	rulesJSON, err := marshalRules(p.Rules)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "policy_repo: marshal rules", err)
	}
	now := r.clock.Now()
	if _, err := r.db.Exec(ctx, insertPolicySQL, string(t), p.ID, p.Name, p.Description, rulesJSON, now); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgConflictCode {
			return nil, errcode.New(errcode.KindConflict, errcode.ErrAuthPolicyDuplicate, "policy already exists",
				errcode.WithInternal(errcode.InternalAttr("policy_id", p.ID)))
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "policy_repo: create", err)
	}
	created := p.Clone()
	created.Version = 1
	return created, nil
}

// Update atomically replaces the policy and bumps version if expectedVersion
// matches the stored version (CAS guard). Returns ErrAuthPolicyNotFound when the
// policy does not exist in t, or ErrVersionConflict when expectedVersion
// mismatches. On success, returns the stored clone with the incremented version.
func (r *PGPolicyRepo) Update(
	ctx context.Context, t tenant.TenantID, id string, expectedVersion int, p *abac.Policy,
) (*abac.Policy, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgPolicyInvalidTenant, err)
	}
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "policy_repo: policy must not be nil")
	}
	if p.TenantID != t {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"policy_repo: policy TenantID does not match the provided tenant",
			errcode.WithInternal(
				errcode.InternalAttr("policyTenantId", string(p.TenantID)),
				errcode.InternalAttr("tenantId", string(t)),
			))
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	rulesJSON, err := marshalRules(p.Rules)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "policy_repo: marshal rules", err)
	}
	now := r.clock.Now()
	var newVersion int
	err = r.db.QueryRow(ctx, updatePolicyCASSQL,
		p.Name, p.Description, rulesJSON, now, string(t), id, expectedVersion,
	).Scan(&newVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, r.disambiguateCASMiss(ctx, t, id)
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "policy_repo: update", err)
	}
	result := clonePolicyFromFields(t, id, p.Name, p.Description, p.Rules, newVersion)
	return result, nil
}

// Delete removes the policy if expectedVersion matches the stored version (CAS
// guard). Returns ErrAuthPolicyNotFound when absent, ErrVersionConflict on
// mismatch. On success, returns the deleted policy.
func (r *PGPolicyRepo) Delete(ctx context.Context, t tenant.TenantID, id string, expectedVersion int) (*abac.Policy, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgPolicyInvalidTenant, err)
	}
	row := r.db.QueryRow(ctx, deletePolicyCASSQL, string(t), id, expectedVersion)
	p, err := scanPolicy(row, t)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, r.disambiguateCASMiss(ctx, t, id)
		}
		var ec *errcode.Error
		if errors.As(err, &ec) && ec.Code == errcode.ErrPGSchemaShape {
			return nil, err
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "policy_repo: delete", err)
	}
	return p, nil
}

// disambiguateCASMiss is called when a CAS UPDATE or DELETE returned zero rows.
// It performs a fast existence check to decide whether the policy is absent
// (KindNotFound) or the version was wrong (KindConflict / ErrVersionConflict).
func (r *PGPolicyRepo) disambiguateCASMiss(ctx context.Context, t tenant.TenantID, id string) error {
	var exists int
	err := r.db.QueryRow(ctx, existsPolicySQL, string(t), id).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFoundPolicy(id)
	}
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "policy_repo: existence check", err)
	}
	// Row exists but version didn't match.
	return cas.CheckVersionMatch(0, "policy", id)
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

// RepoReady verifies that the policies table is reachable via a cheap
// non-transactional probe (`SELECT 1 FROM policies WHERE false` returns zero rows
// without a scan; success means the relation is reachable). Under FORCE RLS with
// no app.tenant_id GUC set the predicate is already empty, so readiness does not
// depend on a tenant scope and the fail-closed isolation stays intact.
func (r *PGPolicyRepo) RepoReady(ctx context.Context) error {
	if _, err := r.db.Exec(ctx, policyRepoReadySQL); err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "policy_repo: readiness probe", err)
	}
	return nil
}

// policyRowScanner is satisfied by both pgx.Row (QueryRow) and pgx.Rows (Query),
// letting GetByID, Delete, and ListByTenant share one scan path.
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
	var version int
	if err := s.Scan(&id, &name, &description, &rulesJSON, &version); err != nil {
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
		Version:     version,
	}
	if err := p.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrPGSchemaShape, "policy_repo: reconstructed policy invalid", err)
	}
	return p, nil
}

// clonePolicyFromFields builds a fresh Policy from discrete field values,
// used by Update to construct the returned post-update aggregate without a
// redundant SELECT. Deep-clones via Policy.Clone so the caller's rule slice
// reference is not retained.
func clonePolicyFromFields(t tenant.TenantID, id, name, description string, rules []abac.Rule, version int) *abac.Policy {
	p := &abac.Policy{
		ID:          id,
		TenantID:    t,
		Name:        name,
		Description: description,
		Rules:       rules,
		Version:     version,
	}
	return p.Clone()
}

// notFoundPolicy returns a KindNotFound error matching the mem store's shape so
// the conformance suite asserts a single error code across implementations.
func notFoundPolicy(id string) error {
	return errcode.New(errcode.KindNotFound, errcode.ErrAuthPolicyNotFound, "policy not found",
		errcode.WithCategory(errcode.CategoryDomain),
		errcode.WithInternal(errcode.InternalAttr("policy_id", id)))
}
