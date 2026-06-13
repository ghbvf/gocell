package reconcile

// Tenancy is a reconciler's declared tenant stance — a sealed, closed-set value a
// reconcile Loop MUST declare at construction (the required second argument to
// reconcile.New). It exists so "didn't think about the tenant dimension of the
// emitted commands' Claimer dedup key" is unrepresentable at construction (#1954).
//
// # Why a required declaration
//
// A reconcile Loop runs under a positively-installed, TENANTLESS system producer
// identity (installSystemProducerIdentity): actor/subject="system", tenant cleared.
// So every command a reconciler emits lands under the "_notenant" Claimer-key
// namespace BY CONSTRUCTION (idemkey.DeriveCommandKey with an empty tenant). That
// is correct for a single-tenant archetype where entity ids are globally unique
// (e.g. device UUIDs), but a multi-tenant reconciler MUST encode the tenant in its
// OWN command-id derivation — otherwise two tenants sharing an entity id collide on
// one "_notenant" key and one tenant's work silently suppresses the other's. Before
// #1954 that was a pure godoc MUST (Soft, forbidden by ai-robust.md §分级). Making
// Tenancy a required positional parameter turns the silent omission into a
// conscious, type-checked, auditable choice.
//
// # Sealed construction (closed value set)
//
// The single field `mode` is unexported and the only minters are SingleTenant() and
// TenantScoped(). Outside this package there is NO way to construct a NON-ZERO
// Tenancy: reconcile.Tenancy{mode: …} is a compile error (unexported field). The
// zero value reconcile.Tenancy{} IS constructible (mode == tenancyUnset) but is
// invalid — IsUnset() reports it and Build() rejects it fail-fast (the same accepted
// residual as authz.Permission{} / reconcile.LeaseTTL{}). The two stances are
// exposed as ACCESSOR FUNCTIONS (not exported vars): a func declaration cannot be
// reassigned, so the registry values are immutable to every external package
// (reassigning authz.SingleTenant = Tenancy{} would be the F6 gap an exported var
// leaves open). The frozen membership of the minter set is the external witness
// RECONCILE-TENANCY-DECLARED-01 (tools/archtest).
//
// # AI-robust rating (four axes; full statement in the archtest godoc)
//
//   - Forging a non-zero Tenancy → type-system HARD (sealed construction).
//   - Omitting the stance → compile HARD (positional param: reconcile.New(r) does
//     not compile).
//   - Passing the zero Tenancy{} → runtime fail-fast (Build rejects it).
//   - The minter VALUE SET → MEDIUM (RECONCILE-TENANCY-DECLARED-01 archtest freeze).
//
// Residual blind spot: the guard forces a declaration but cannot verify a
// TenantScoped reconciler's body actually encodes the tenant, nor cross-check a
// SingleTenant declaration against an actual multi-tenant cell (no cell-level
// tenancy signal exists — multi-tenancy is a runtime ctx property). See the archtest
// godoc and ADR 202606101200-1821.
type Tenancy struct {
	mode tenancyMode
}

// tenancyMode is the sealed backing enum. The zero value is tenancyUnset so a
// zero-value Tenancy{} is detectably invalid.
type tenancyMode uint8

const (
	tenancyUnset tenancyMode = iota // zero value — Build rejects
	tenancySingle
	tenancyScoped
)

// singleTenant / tenantScoped are the package-private singletons backing the
// accessor funcs. Unexported so no external package can reassign them.
var (
	singleTenant = Tenancy{mode: tenancySingle}
	tenantScoped = Tenancy{mode: tenancyScoped}
)

// SingleTenant declares that the reconciler emits under the tenantless system
// principal and that "_notenant" Claimer keys are correct for it — its entity ids
// are globally unique (e.g. device UUIDs) and its tables are not tenant-partitioned.
// This is the device / cert-renewal archetype.
//
// It is an accessor function (not an exported var) so the registry value is
// immutable to external packages — reassigning a func is a compile error.
func SingleTenant() Tenancy { return singleTenant }

// TenantScoped declares that the reconciler reconciles entities across multiple
// tenants. The framework installs a TENANTLESS system identity and strips any
// ambient ctx tenant, so a tenant-scoped reconciler MUST encode the tenant in its
// OWN command-id / store derivation — it cannot rely on an ambient ctx tenant to
// color the principal tenant. Otherwise two tenants sharing an entity id collide on
// one "_notenant" Claimer key and one tenant's renewal suppresses the other's.
//
// This is an acknowledgement stance: Build() accepts it, but the framework provides
// no per-tenant driving (there is no tenant-enumeration API) — the consumer owns the
// tenant dimension end to end. The framework cannot verify the body actually does
// so (the documented residual blind spot of RECONCILE-TENANCY-DECLARED-01).
//
// It is an accessor function (not an exported var) so the registry value is
// immutable to external packages — reassigning a func is a compile error.
func TenantScoped() Tenancy { return tenantScoped }

// IsUnset reports whether t is the invalid zero value (no minter ran). Build()
// rejects an unset Tenancy fail-fast so a reconciler cannot be constructed without a
// conscious tenancy stance.
func (t Tenancy) IsUnset() bool { return t.mode == tenancyUnset }
