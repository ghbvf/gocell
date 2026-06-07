package tenant

import (
	"fmt"
	"regexp"
)

// RowVisibility is the self-contained row-level authorization obligation that a
// tenant-scoped list/get repo (the PEP) enforces: a RowScope paired with the
// subject identity that "self"/"device" resolve to. It is the GoCell typed
// projection of the XACML <Obligation> (an ObligationId + its AttributeAssignments
// travel as ONE value from the decision point to the enforcement point) — and the
// structural analog of Oso data filtering's Filter value object, which is a
// returned value the data layer translates rather than a live query handle. That
// shape is mandatory here because pkg/tenant has
// no layer dependencies (stdlib + google/uuid only): RowVisibility can emit a SQL
// fragment string + arg, never hold a *sql.DB / ORM query.
//
// # Sealed construction
//
// RowVisibility's fields are unexported and the only constructor is
// NewRowVisibility, which validates the obligation. A populated RowVisibility{…}
// literal is not expressible outside this package, so a caller cannot FORGE a
// RowScopeAll (cross-tenant) obligation by struct literal — mirroring the
// errcode.PublicDetail / outbox.Entry sealed-construction pattern. Combined with
// RowVisibility being a mandatory typed positional parameter on the repo
// interfaces (ROWSCOPE-REPO-PARAM-FUNNEL-01), "forget the obligation" and "forge
// the obligation" are both compile-time impossible.
//
// # Canonical form
//
// The {scope, subject} pair has a canonical validity invariant enforced at
// construction (and re-checked by Validate): RowScopeSelf / RowScopeDevice REQUIRE
// a non-empty subject (the owner the row must belong to), while RowScopeTenant /
// RowScopeAll REQUIRE an EMPTY subject (they carry no owner identity). A tenant/all
// obligation with a non-empty subject is rejected fail-closed rather than silently
// ignored — so two semantically-equal obligations are byte-equal values and
// Subject() is always "" for tenant/all. This keeps the value object usable as a
// scope key (e.g. the auditquery cursor fingerprint) without a non-canonical
// subject leaking into the scope on one page and not another.
//
// # Owner dimension only
//
// RowVisibility enforces the OWNER dimension of row visibility:
//   - RowScopeSelf / RowScopeDevice → owner column = subject (the row's owner).
//   - RowScopeTenant / RowScopeAll → no owner predicate (every owner is visible).
//
// The TENANT boundary is a SEPARATE, per-table layer that this obligation does
// NOT touch (PG RLS via app.tenant_id for accesscore tables; AuditFilters.TenantID
// for audit_entries, which carries no RLS). RowScopeAll's cross-tenant bypass of
// that boundary is therefore a tenant-boundary concern, wired at the super-admin
// path in PR-5 (身份→RowScope 收窄 + RowScope=all 强制审计).
//
// SQLPredicate and Allows below are PURE OWNER-DIMENSION TRANSLATORS: for the
// owner dimension RowScopeAll genuinely means "no owner predicate" (== tenant),
// so they translate it as such. As of PR-5 (epic #1337), audit ledger stores are
// pure PEPs that APPLY the obligation without fail-closing RowScopeAll; whether
// the caller is PERMITTED to use RowScopeAll is an authentication/authorization
// concern wired at the super-admin path in PR-5 (身份→RowScope 收窄 +
// RowScope=all 强制審計). The TENANT boundary remains orthogonal (see above).
type RowVisibility struct {
	scope   RowScope
	subject string
}

// ownerColumnPattern restricts SQLPredicate's ownerColumn to a SQL identifier
// (lowercase snake_case). The owner column is always a code-level constant
// supplied by the repo, never user input; this guard is fail-fast defense in
// depth against a future caller threading an untrusted string into the column
// slot (the column name is interpolated into SQL; the subject value is always a
// bound parameter, never interpolated).
var ownerColumnPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// NewRowVisibility constructs a validated RowVisibility obligation. It is the
// sole constructor (the fields are unexported). scope must be a defined RowScope;
// RowScopeSelf and RowScopeDevice require a non-empty subject (the owner the row
// must belong to) — an empty subject under a self/device scope would silently
// widen visibility to every row. RowScopeTenant and RowScopeAll require an EMPTY
// subject (they carry no owner identity); a non-empty subject is a malformed
// obligation and is rejected. Both rules are the canonical-form invariant checked
// by Validate (see type doc).
func NewRowVisibility(scope RowScope, subject string) (RowVisibility, error) {
	v := RowVisibility{scope: scope, subject: subject}
	if err := v.Validate(); err != nil {
		return RowVisibility{}, err
	}
	return v, nil
}

// Scope returns the RowScope obligation.
func (v RowVisibility) Scope() RowScope { return v.scope }

// Subject returns the subject identity that self/device scopes resolve to. It is
// always "" for tenant/all (the canonical form rejects a non-empty subject there;
// see type doc).
func (v RowVisibility) Subject() string { return v.subject }

// Validate returns an error if the obligation is not a well-formed value: zero
// value / invalid scope, a self/device scope with an empty subject, or a
// tenant/all scope with a NON-empty subject (the canonical-form invariant — see
// type doc). Repo methods receive RowVisibility as a typed positional parameter;
// calling Validate is the runtime fail-closed backstop to the compile-time "漏传 =
// 编译失败" guarantee.
func (v RowVisibility) Validate() error {
	if err := v.scope.Validate(); err != nil {
		return err
	}
	switch v.scope {
	case RowScopeSelf, RowScopeDevice:
		if v.subject == "" {
			return fmt.Errorf("tenant: RowScope %s requires a non-empty subject", v.scope)
		}
	case RowScopeTenant, RowScopeAll:
		if v.subject != "" {
			return fmt.Errorf("tenant: RowScope %s must carry an empty subject (no owner identity)", v.scope)
		}
	}
	return nil
}

// RowSQLPredicate is the parameterized owner-predicate fragment produced by
// RowVisibility.SQLPredicate. It is shaped to compose with a parameterized query
// builder that assigns the placeholder number, e.g.
// pgquery.Builder.AppendIf(p.Apply, p.Prefix, p.Arg). The zero value (Apply
// false) means "no owner predicate".
type RowSQLPredicate struct {
	// Apply reports whether the owner predicate should be appended at all.
	Apply bool
	// Prefix is the WHERE fragment up to (but excluding) the placeholder, e.g.
	// " AND actor_id = "; the builder appends the "$N" placeholder after it.
	Prefix string
	// Arg is the bound subject value for the placeholder (always a bound
	// parameter, never interpolated).
	Arg any
}

// SQLPredicate translates the obligation's OWNER dimension into a parameterized
// SQL WHERE fragment for the given owner column:
//
//   - RowScopeSelf / RowScopeDevice → Apply=true, Prefix=" AND <ownerColumn> = ",
//     Arg=subject. The builder appends the "$N" placeholder after Prefix, so the
//     subject is always a bound parameter (never interpolated).
//   - RowScopeTenant / RowScopeAll → zero RowSQLPredicate (Apply=false, no owner
//     predicate). The tenant boundary is enforced separately (see type doc).
//
// It returns an error for an invalid obligation (zero value / self|device without
// subject) or an ownerColumn that is not a SQL identifier (defense in depth — the
// column is a repo-supplied constant, not user input).
func (v RowVisibility) SQLPredicate(ownerColumn string) (RowSQLPredicate, error) {
	if err := v.Validate(); err != nil {
		return RowSQLPredicate{}, err
	}
	if !ownerColumnPattern.MatchString(ownerColumn) {
		return RowSQLPredicate{}, fmt.Errorf("tenant: RowVisibility owner column %q is not a SQL identifier", ownerColumn)
	}
	switch v.scope {
	case RowScopeSelf, RowScopeDevice:
		return RowSQLPredicate{Apply: true, Prefix: " AND " + ownerColumn + " = ", Arg: v.subject}, nil
	default:
		// RowScopeTenant / RowScopeAll: no owner predicate (Validate above already
		// rejected any non-defined scope).
		return RowSQLPredicate{}, nil
	}
}

// Allows reports whether a row whose owner column holds ownerValue is visible
// under this obligation's OWNER dimension. It is the in-memory mirror of
// SQLPredicate, used by mem-backed repos and as a post-fetch check:
//
//   - RowScopeSelf / RowScopeDevice → ownerValue == subject.
//   - RowScopeTenant / RowScopeAll → always true (no owner restriction).
//
// As with SQLPredicate, the tenant boundary is enforced separately; Allows only
// covers the owner dimension.
func (v RowVisibility) Allows(ownerValue string) bool {
	switch v.scope {
	case RowScopeSelf, RowScopeDevice:
		return ownerValue == v.subject
	default:
		return true
	}
}
