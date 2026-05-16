package cell

import (
	"context"
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

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
// name MUST end in "_ready" (READYZ-PROBE-NAMING-01). This is now
// runtime-enforced: passing a name without the "_ready" suffix panics with a
// B-class programmer-error (panicregister.Approved funnel, PANIC-REGISTERED-01
// compliant). The probe is forwarded verbatim to reg.Health; first-wins
// duplicate semantics and concurrent-call safety are inherited from
// Registry.Health.
//
// AI-HARD funnel dual-lock (charter §"Funnel 双向锁评级"):
//   - downstream Hard: form uniqueness — archtest CELL-REPO-READYZ-PROBE-01
//     makes any other repo-readiness registration shape fail CI (range
//     example: panic(panicregister.Approved(...))).
//   - upstream Medium: the same archtest enforces every RepoHealthProber
//     implementation is exercised by RunRepoReadinessConformance, but this is
//     archtest-bound, not compile-time (Go cannot require a test to exist).
//     Transitional form per charter; Hard-ization tracked by backlog
//     REPO-READYZ-UPSTREAM-FUNNEL-HARD-01 (cap-13).
//
// Differentiated behavior itself is enforced by the real-failure-injection
// conformance harness (RunRepoReadinessConformance), not by this funnel.
func RegisterRepoReadiness(reg Registry, name string, p RepoHealthProber) {
	if !strings.HasSuffix(name, "_ready") {
		panic(panicregister.Approved("repo-readiness-name-suffix",
			errcode.Assertion("RegisterRepoReadiness: probe name must end in _ready (READYZ-PROBE-NAMING-01)")))
	}
	reg.Health(name, p.RepoReady)
}
