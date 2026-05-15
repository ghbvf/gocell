package cell

import "context"

// RepoHealthProber is implemented by a Cell's primary repository/store to
// expose a *differentiated* readiness check.
//
// "Differentiated" means the check MUST exercise a failure domain distinct
// from the pool-level postgres_ready probe (a bare pool ping registered by
// adapters/postgres.*Pool). A RepoReady implementation backed by SQL is
// expected to issue a representative query against the cell's own
// relation(s) — e.g. SELECT 1 FROM <table> WHERE false — so it surfaces
// schema/migration drift, missing tables, and table-level permission loss
// that a connection ping cannot detect. In-memory implementations return nil
// (always ready), matching the MemStore convention.
//
// This is intentionally NOT the same interface as HealthProber: HealthProber
// (Probes()) is the emitter fail-open-rate concern; RepoHealthProber is the
// cell repository readiness concern. They are separate semantic categories so
// CELL-REPO-READYZ-PROBE-01 can lock one canonical registration form per
// category without cross-contamination.
type RepoHealthProber interface {
	RepoReady(ctx context.Context) error
}

// RegisterRepoReadiness is the single typed funnel for cell-level repository
// readiness probes. Cells MUST register repo probes through this function and
// MUST NOT call reg.Health(...) directly for a repository/store, nor use an
// anonymous interface{ Health(context.Context) error } duck-type assertion
// (the latter is a dead-code class — see ADR
// docs/architecture/*-adr-cell-repo-readyz-probe.md and the accesscore
// session_store_ready regression it caused).
//
// name MUST end in "_ready" (READYZ-PROBE-NAMING-01). The probe is forwarded
// verbatim to reg.Health; first-wins duplicate semantics and concurrent-call
// safety are inherited from Registry.Health.
//
// AI-HARD: this funnel gives form uniqueness — archtest
// CELL-REPO-READYZ-PROBE-01 makes any other repo-readiness registration shape
// fail CI (range example: panic(panicregister.Approved(...))). Differentiated
// behavior itself is enforced by the real-failure-injection conformance
// harness (RunRepoReadinessConformance), not by this funnel.
func RegisterRepoReadiness(reg Registry, name string, p RepoHealthProber) {
	reg.Health(name, p.RepoReady)
}
