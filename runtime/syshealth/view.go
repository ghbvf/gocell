// Package syshealth projects the runtime's existing health primitives — the
// kernel [healthz.Aggregator] probe snapshot and the [assembly.CoreAssembly]
// per-cell view — into an operator-facing, per-cell aggregate (#1860). It is the
// read-side facade behind the http.admin.health.cells.v1 contract served by the
// syscore cell.
//
// It does NOT run its own aggregation: Report reuses Aggregator.Evaluate and the
// assembly's CellIDs/Snapshots/Health/Ready, then performs ONE projection layer —
// bucketing probes per cell and computing the assembly-global adapter bucket by
// structural set-difference (a probe not owned by any cell's RegistrySnapshot is
// an infrastructure/framework probe). There is no "postgres"→"postgres_ready"
// string mapping: the cell↔probe relationship is taken structurally from the
// per-cell RegistrySnapshot.Probes accumulated at cell Init, never inferred from
// probe names.
//
// HealthView is injected into the primary listener request context by bootstrap
// (mirroring the ABAC Authorizer funnel); the syscore handler reads it via
// HealthViewFromContext and fails closed (503) on absence.
package syshealth

import (
	"context"
	stderrors "errors"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/healthz"
)

// Probe wire-status strings. The set is closed here (the contract schema documents
// it in prose since the codegen jsonschema subset has no enum); cell `status` and
// `overall` use the first three, probes additionally use timeout.
const (
	statusHealthy   = "healthy"
	statusDegraded  = "degraded"
	statusUnhealthy = "unhealthy"
	statusTimeout   = "timeout"
)

// ProbeHealth is one probe's projected outcome (a cell dep or an adapter probe).
type ProbeHealth struct {
	Name       string
	Status     string
	DurationMs int64
}

// CellHealth is one cell's projected health.
type CellHealth struct {
	ID     string
	Live   bool
	Ready  bool
	Status string
	Deps   []ProbeHealth
}

// Report is the full aggregate: an overall status, per-cell health, and the
// assembly-global adapter/infrastructure probes.
type Report struct {
	Overall  string
	Cells    []CellHealth
	Adapters []ProbeHealth
}

// HealthView is the read-side facade the syscore handler consumes from request
// context. A single method keeps the injected surface minimal and the funnel honest.
type HealthView interface {
	// Report runs a fresh aggregation pass and projects it per cell + adapters.
	Report(ctx context.Context) Report
}

// assemblySource is the narrow read surface the view needs from the assembly.
// *assembly.CoreAssembly satisfies it; tests use a fake. Cell(id) returns a
// cell.Cell whose embedded CellStatus.Ready reports per-cell readiness.
type assemblySource interface {
	CellIDs() []string
	Snapshots() map[string]cell.RegistrySnapshot
	Health() map[string]cell.HealthStatus
	Cell(id string) cell.Cell
}

// view is the default HealthView backed by the assembly + aggregator that
// bootstrap already owns.
type view struct {
	asm assemblySource
	agg healthz.Aggregator
}

// New builds a HealthView over the runtime assembly + healthz aggregator.
// Both are non-nil in the bootstrap wiring path (the composition root owns them);
// absence of the view in request context — not a nil view — is the fail-closed
// signal the handler checks.
func New(asm assemblySource, agg healthz.Aggregator) HealthView {
	return &view{asm: asm, agg: agg}
}

// Report evaluates all probes once, then projects: per-cell deps from the cell's
// own RegistrySnapshot.Probes, and the adapter bucket as the probes owned by no
// cell (structural set-difference). overall is the worst status across all cells
// and adapter probes.
func (v *view) Report(ctx context.Context) Report {
	snap := v.agg.Evaluate(ctx)
	byName := make(map[healthz.ProbeName]healthz.ProbeResult, len(snap.Probes))
	for _, pr := range snap.Probes {
		byName[pr.Name] = pr
	}

	snaps := v.asm.Snapshots()   // nil when the assembly is not started
	cellStatus := v.asm.Health() // status per registered cell
	owned := make(map[healthz.ProbeName]struct{})

	worst := 0
	cells := make([]CellHealth, 0, len(v.asm.CellIDs()))
	for _, id := range v.asm.CellIDs() {
		ch := CellHealth{
			ID:   id,
			Live: true, // in-process: registered in a started assembly ⇒ live
		}
		lifecycleReady := cellReady(v.asm.Cell(id))
		ownStatus := normalizeCellStatus(cellStatus[id].Status)
		for _, p := range snaps[id].Probes {
			n := p.Name()
			// Mark the probe cell-owned BEFORE the status lookup: ownership is
			// structural (the cell registered it), independent of whether the
			// aggregator has a result for it. This keeps it out of the adapter
			// bucket below even on the rare path where it has no result yet.
			owned[n] = struct{}{}
			if pr, ok := byName[n]; ok {
				ch.Deps = append(ch.Deps, toProbeHealth(pr))
			}
			// else: a snapshot probe with no aggregator result (transient —
			// e.g. registered but not yet drained) is safely omitted from deps
			// rather than surfaced with an unknown status. In production every
			// snapshot probe is drained into the aggregator at bootstrap, so this
			// branch is defensive only.
		}
		// Fold the cell's OWN dep health back into its ready/status: a down dep is
		// not a mere annotation in Deps — it changes the cell's verdict (K8s
		// readiness parity). overall then folds the now-dep-aware cell status.
		ch.Ready, ch.Status = foldCellDeps(lifecycleReady, ownStatus, ch.Deps)
		worst = maxRank(worst, ch.Status)
		cells = append(cells, ch)
	}

	// Adapter bucket: every probe not claimed by a cell. snap.Probes is sorted by
	// name (aggregator contract), so the bucket order is deterministic.
	var adapters []ProbeHealth
	for _, pr := range snap.Probes {
		if _, isCell := owned[pr.Name]; isCell {
			continue
		}
		ad := toProbeHealth(pr)
		adapters = append(adapters, ad)
		worst = maxRank(worst, ad.Status)
	}

	return Report{
		Overall:  statusFromRank(worst),
		Cells:    cells,
		Adapters: adapters,
	}
}

// cellReady reads the cell's lifecycle readiness; a nil cell (id without a
// registered cell — not expected) fails closed to not-ready.
func cellReady(c cell.Cell) bool {
	if c == nil {
		return false
	}
	return c.Ready()
}

// toProbeHealth maps a kernel probe result to the wire projection. A StatusDown
// caused by a deadline is surfaced as "timeout" (matching the /readyz wire
// vocabulary) so dashboards can distinguish overrun from a domain error.
func toProbeHealth(pr healthz.ProbeResult) ProbeHealth {
	return ProbeHealth{
		Name:       pr.Name.String(),
		Status:     probeStatus(pr.Status, pr.Err),
		DurationMs: pr.Latency.Milliseconds(),
	}
}

func probeStatus(s healthz.Status, err error) string {
	switch s {
	case healthz.StatusUp:
		return statusHealthy
	case healthz.StatusDegraded:
		return statusDegraded
	default: // StatusDown
		if stderrors.Is(err, context.DeadlineExceeded) {
			return statusTimeout
		}
		return statusUnhealthy
	}
}

// normalizeCellStatus keeps the assembly's cell status string within the closed
// set; an unknown/empty value fails closed to unhealthy rather than echoing an
// out-of-set token onto the wire.
func normalizeCellStatus(s string) string {
	switch s {
	case statusHealthy, statusDegraded, statusUnhealthy:
		return s
	default:
		return statusUnhealthy
	}
}

// foldCellDeps folds a cell's own lifecycle status + readiness with the worst of
// its dependency probes, mirroring Kubernetes readiness semantics: a hard-down
// dependency (unhealthy/timeout — rank 2) takes the cell OUT of ready and marks
// its status unhealthy; a degraded dependency degrades the status but leaves
// lifecycle readiness intact. The cell's own Health() status flows into status
// only — the ready axis stays lifecycle+deps (own-status→ready coupling is
// intentionally out of scope; folding dep health into the verdict is the fix).
func foldCellDeps(lifecycleReady bool, ownStatus string, deps []ProbeHealth) (ready bool, status string) {
	depWorst := 0
	for _, d := range deps {
		depWorst = maxRank(depWorst, d.Status)
	}
	status = statusFromRank(maxRank(depWorst, ownStatus))
	ready = lifecycleReady && depWorst < rank(statusUnhealthy)
	return ready, status
}

// maxRank folds a status string into the running worst-rank.
func maxRank(cur int, s string) int {
	if r := rank(s); r > cur {
		return r
	}
	return cur
}

// rank orders severity: healthy(0) < degraded(1) < unhealthy/timeout(2).
func rank(s string) int {
	switch s {
	case statusHealthy:
		return 0
	case statusDegraded:
		return 1
	default: // unhealthy, timeout
		return 2
	}
}

func statusFromRank(r int) string {
	switch r {
	case 0:
		return statusHealthy
	case 1:
		return statusDegraded
	default:
		return statusUnhealthy
	}
}
