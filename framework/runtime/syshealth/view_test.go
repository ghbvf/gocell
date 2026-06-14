package syshealth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/healthz"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// Site-specific probe latencies for the projection tests. TEST-TIME-LITERAL-01
// requires test-time durations to live in package-level consts, not inline literals.
const (
	latency1ms = 1 * time.Millisecond
	latency2ms = 2 * time.Millisecond
	latency3ms = 3 * time.Millisecond
)

// --- fakes -------------------------------------------------------------------

type fakeAgg struct{ snap healthz.Snapshot }

func (f fakeAgg) Register(healthz.Probe) error              { return nil }
func (f fakeAgg) Deregister(healthz.ProbeName)              {}
func (f fakeAgg) Evaluate(context.Context) healthz.Snapshot { return f.snap }

// stubCell embeds a validly-constructed BaseCell and overrides Ready() so the
// view test can control per-cell readiness independent of lifecycle state.
type stubCell struct {
	*cell.BaseCell
	ready bool
}

func (c *stubCell) Ready() bool { return c.ready }

func newStubCell(t *testing.T, id string, ready bool) *stubCell {
	t.Helper()
	m := &metadata.CellMeta{Type: "support", ConsistencyLevel: "L1"}
	m.ID = id
	return &stubCell{BaseCell: cell.MustNewBaseCell(m), ready: ready}
}

type fakeAsm struct {
	ids    []string
	snaps  map[string]cell.RegistrySnapshot
	health map[string]cell.HealthStatus
	cells  map[string]cell.Cell
}

func (f fakeAsm) CellIDs() []string                           { return f.ids }
func (f fakeAsm) Snapshots() map[string]cell.RegistrySnapshot { return f.snaps }
func (f fakeAsm) Health() map[string]cell.HealthStatus        { return f.health }
func (f fakeAsm) Cell(id string) cell.Cell                    { return f.cells[id] }

func mustName(t *testing.T, s string) healthz.ProbeName {
	t.Helper()
	n, err := healthz.NewProbeName(s)
	if err != nil {
		t.Fatalf("NewProbeName(%q): %v", s, err)
	}
	return n
}

func mustProbe(t *testing.T, s string) healthz.Probe {
	t.Helper()
	return healthz.NewProbe(mustName(t, s), func(context.Context) error { return nil })
}

// --- tests -------------------------------------------------------------------

// TestView_Report_PerCellAndAdapterBucketing is the core projection test: per-cell
// deps come from each cell's own RegistrySnapshot.Probes (structural), and the
// adapter bucket is exactly the probes owned by no cell (set-difference). It also
// pins live/ready/status mapping, timeout classification, and overall = worst.
func TestView_Report_PerCellAndAdapterBucketing(t *testing.T) {
	v := New(fakeAsm{
		ids: []string{"cellone", "celltwo"},
		snaps: map[string]cell.RegistrySnapshot{
			"cellone": {Probes: []healthz.Probe{mustProbe(t, "cellone_repo_ready")}},
			"celltwo": {Probes: []healthz.Probe{mustProbe(t, "celltwo_repo_ready")}},
		},
		health: map[string]cell.HealthStatus{
			"cellone": {Status: "healthy"},
			"celltwo": {Status: "degraded"},
		},
		cells: map[string]cell.Cell{
			"cellone": newStubCell(t, "cellone", true),
			"celltwo": newStubCell(t, "celltwo", false),
		},
	}, fakeAgg{snap: healthz.Snapshot{Probes: []healthz.ProbeResult{
		{Name: mustName(t, "cellone_repo_ready"), Status: healthz.StatusUp, Latency: latency2ms},
		{Name: mustName(t, "celltwo_repo_ready"), Status: healthz.StatusDegraded, Latency: latency3ms},
		{Name: mustName(t, "postgres_ready"), Status: healthz.StatusUp, Latency: latency1ms},
		{Name: mustName(t, "redis_ready"), Status: healthz.StatusDown, Err: context.DeadlineExceeded},
	}}})

	rep := v.Report(context.Background())

	// overall: redis_ready timeout ⇒ rank 2 ⇒ unhealthy.
	if rep.Overall != "unhealthy" {
		t.Fatalf("Overall = %q, want unhealthy", rep.Overall)
	}

	if len(rep.Cells) != 2 {
		t.Fatalf("Cells len = %d, want 2", len(rep.Cells))
	}
	// cellone: live, ready, healthy, one dep (its own repo probe — NOT an adapter).
	c0 := rep.Cells[0]
	if c0.ID != "cellone" || !c0.Live || !c0.Ready || c0.Status != "healthy" {
		t.Fatalf("cellone projection wrong: %+v", c0)
	}
	if len(c0.Deps) != 1 || c0.Deps[0].Name != "cellone_repo_ready" ||
		c0.Deps[0].Status != "healthy" || c0.Deps[0].DurationMs != 2 {
		t.Fatalf("cellone deps wrong: %+v", c0.Deps)
	}
	// celltwo: not ready, degraded.
	c1 := rep.Cells[1]
	if c1.ID != "celltwo" || !c1.Live || c1.Ready || c1.Status != "degraded" {
		t.Fatalf("celltwo projection wrong: %+v", c1)
	}

	// adapters = probes owned by NO cell: postgres_ready (healthy), redis_ready (timeout).
	gotAdapters := map[string]ProbeHealth{}
	for _, a := range rep.Adapters {
		gotAdapters[a.Name] = a
	}
	if len(gotAdapters) != 2 {
		t.Fatalf("Adapters = %+v, want exactly postgres_ready + redis_ready", rep.Adapters)
	}
	if gotAdapters["postgres_ready"].Status != "healthy" {
		t.Fatalf("postgres_ready adapter status = %q, want healthy", gotAdapters["postgres_ready"].Status)
	}
	if gotAdapters["redis_ready"].Status != "timeout" {
		t.Fatalf("redis_ready adapter status = %q, want timeout (deadline)", gotAdapters["redis_ready"].Status)
	}
	// Cell probes must NOT leak into the adapter bucket.
	if _, leaked := gotAdapters["cellone_repo_ready"]; leaked {
		t.Fatal("cell probe leaked into adapter bucket — set-difference broken")
	}
}

// TestView_Report_FailClosedEdges pins the defensive paths: a nil cell ⇒ not
// ready, an unknown/empty health status ⇒ unhealthy, and a started-but-empty
// snapshot map ⇒ no deps + no panic.
func TestView_Report_FailClosedEdges(t *testing.T) {
	v := New(fakeAsm{
		ids:    []string{"ghost"},
		snaps:  nil, // assembly not started / no snapshots
		health: map[string]cell.HealthStatus{"ghost": {Status: "bogus"}},
		cells:  map[string]cell.Cell{"ghost": nil}, // no live cell object
	}, fakeAgg{snap: healthz.Snapshot{}})

	rep := v.Report(context.Background())
	if len(rep.Cells) != 1 {
		t.Fatalf("Cells len = %d, want 1", len(rep.Cells))
	}
	g := rep.Cells[0]
	if g.Ready {
		t.Fatal("nil cell must project ready=false (fail-closed)")
	}
	if g.Status != "unhealthy" {
		t.Fatalf("unknown health status must normalize to unhealthy, got %q", g.Status)
	}
	if len(g.Deps) != 0 {
		t.Fatalf("no snapshot ⇒ no deps, got %+v", g.Deps)
	}
	if len(rep.Adapters) != 0 {
		t.Fatalf("no probes ⇒ no adapters, got %+v", rep.Adapters)
	}
	// All-healthy/empty ⇒ overall healthy (worst of nothing is healthy), but the
	// single cell normalized to unhealthy, so overall is unhealthy.
	if rep.Overall != "unhealthy" {
		t.Fatalf("Overall = %q, want unhealthy (the ghost cell)", rep.Overall)
	}
}

// TestProbeStatus pins every probeStatus branch (the StatusDown non-deadline →
// "unhealthy" and nil-err paths were previously only exercised indirectly).
func TestProbeStatus(t *testing.T) {
	connErr := errors.New("connection refused")
	cases := []struct {
		name string
		s    healthz.Status
		err  error
		want string
	}{
		{"up", healthz.StatusUp, nil, "healthy"},
		{"degraded", healthz.StatusDegraded, nil, "degraded"},
		{"down_deadline", healthz.StatusDown, context.DeadlineExceeded, "timeout"},
		{"down_other_err", healthz.StatusDown, connErr, "unhealthy"},
		{"down_nil_err", healthz.StatusDown, nil, "unhealthy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := probeStatus(c.s, c.err); got != c.want {
				t.Fatalf("probeStatus(%v, %v) = %q, want %q", c.s, c.err, got, c.want)
			}
		})
	}
}

// TestView_Report_OverallRanks pins statusFromRank's healthy(0) and degraded(1)
// paths (the bucketing test only reached unhealthy(2)). all-healthy ⇒ overall
// healthy; a single degraded cell with all-healthy probes ⇒ overall degraded.
func TestView_Report_OverallRanks(t *testing.T) {
	mk := func(cellStatus string) HealthView {
		return New(fakeAsm{
			ids:    []string{"only"},
			snaps:  map[string]cell.RegistrySnapshot{"only": {Probes: []healthz.Probe{mustProbe(t, "only_repo_ready")}}},
			health: map[string]cell.HealthStatus{"only": {Status: cellStatus}},
			cells:  map[string]cell.Cell{"only": newStubCell(t, "only", true)},
		}, fakeAgg{snap: healthz.Snapshot{Probes: []healthz.ProbeResult{
			{Name: mustName(t, "only_repo_ready"), Status: healthz.StatusUp, Latency: latency1ms},
		}}})
	}
	if got := mk("healthy").Report(context.Background()).Overall; got != "healthy" {
		t.Fatalf("all-healthy Overall = %q, want healthy", got)
	}
	if got := mk("degraded").Report(context.Background()).Overall; got != "degraded" {
		t.Fatalf("one-degraded-cell Overall = %q, want degraded", got)
	}
}

// TestView_Report_SnapshotProbeMissingFromEvaluate pins the defensive path: a
// cell snapshot probe with NO aggregator result is omitted from deps (not shown
// with an unknown status) AND does not leak into the adapter bucket (it stays
// cell-owned via the set-difference).
func TestView_Report_SnapshotProbeMissingFromEvaluate(t *testing.T) {
	v := New(fakeAsm{
		ids:    []string{"only"},
		snaps:  map[string]cell.RegistrySnapshot{"only": {Probes: []healthz.Probe{mustProbe(t, "only_repo_ready")}}},
		health: map[string]cell.HealthStatus{"only": {Status: "healthy"}},
		cells:  map[string]cell.Cell{"only": newStubCell(t, "only", true)},
	}, fakeAgg{snap: healthz.Snapshot{Probes: []healthz.ProbeResult{
		// aggregator returns ONLY an unrelated adapter probe, NOT only_repo_ready.
		{Name: mustName(t, "postgres_ready"), Status: healthz.StatusUp, Latency: latency1ms},
	}}})

	rep := v.Report(context.Background())
	if len(rep.Cells[0].Deps) != 0 {
		t.Fatalf("snapshot probe without aggregator result must be omitted from deps, got %+v", rep.Cells[0].Deps)
	}
	for _, a := range rep.Adapters {
		if a.Name == "only_repo_ready" {
			t.Fatal("cell-owned probe leaked into adapter bucket despite missing aggregator result")
		}
	}
}

// TestFoldCellDeps pins the dep→cell verdict folding (the F1 fix): a hard-down dep
// (unhealthy/timeout) flips the cell out of ready AND to unhealthy; a degraded dep
// degrades status but leaves lifecycle readiness; the cell's own status floors the
// status verdict but does NOT flip ready (own-status→ready coupling is out of
// scope); lifecycle-not-ready stays not-ready regardless of healthy deps.
func TestFoldCellDeps(t *testing.T) {
	cases := []struct {
		name       string
		ready      bool
		ownStatus  string
		deps       []string
		wantReady  bool
		wantStatus string
	}{
		{"ready_no_deps", true, "healthy", nil, true, "healthy"},
		{"ready_dep_healthy", true, "healthy", []string{"healthy"}, true, "healthy"},
		{"ready_dep_degraded", true, "healthy", []string{"degraded"}, true, "degraded"},
		{"ready_dep_unhealthy", true, "healthy", []string{"unhealthy"}, false, "unhealthy"},
		{"ready_dep_timeout", true, "healthy", []string{"timeout"}, false, "unhealthy"},
		{"ready_own_degraded_dep_healthy", true, "degraded", []string{"healthy"}, true, "degraded"},
		{"ready_own_unhealthy_no_dep", true, "unhealthy", nil, true, "unhealthy"},
		{"ready_deps_degraded_then_unhealthy", true, "healthy", []string{"degraded", "unhealthy"}, false, "unhealthy"},
		{"notready_dep_healthy", false, "healthy", []string{"healthy"}, false, "healthy"},
		{"notready_dep_unhealthy", false, "healthy", []string{"unhealthy"}, false, "unhealthy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			deps := make([]ProbeHealth, len(c.deps))
			for i, s := range c.deps {
				deps[i] = ProbeHealth{Name: "d", Status: s}
			}
			gotReady, gotStatus := foldCellDeps(c.ready, c.ownStatus, deps)
			if gotReady != c.wantReady || gotStatus != c.wantStatus {
				t.Fatalf("foldCellDeps(%v, %q, %v) = (%v, %q), want (%v, %q)",
					c.ready, c.ownStatus, c.deps, gotReady, gotStatus, c.wantReady, c.wantStatus)
			}
		})
	}
}

// TestView_Report_CellDepDownFlipsReady is the F1 regression (fails on the
// pre-fix code): a cell that is lifecycle-ready with a healthy own status but
// whose OWN dependency probe is down must project ready=false / status=unhealthy
// — not a healthy/ready cell with the failure buried in Deps — and overall must
// reflect it. The dep stays listed.
func TestView_Report_CellDepDownFlipsReady(t *testing.T) {
	v := New(fakeAsm{
		ids:    []string{"only"},
		snaps:  map[string]cell.RegistrySnapshot{"only": {Probes: []healthz.Probe{mustProbe(t, "only_repo_ready")}}},
		health: map[string]cell.HealthStatus{"only": {Status: "healthy"}},
		cells:  map[string]cell.Cell{"only": newStubCell(t, "only", true)},
	}, fakeAgg{snap: healthz.Snapshot{Probes: []healthz.ProbeResult{
		{Name: mustName(t, "only_repo_ready"), Status: healthz.StatusDown, Err: errors.New("repo down"), Latency: latency1ms},
	}}})

	c0 := v.Report(context.Background()).Cells[0]
	if c0.Ready {
		t.Fatal("cell with a down dependency must project ready=false, not ready=true with the failure buried in Deps")
	}
	if c0.Status != "unhealthy" {
		t.Fatalf("cell status = %q, want unhealthy (down dep folds into the cell verdict)", c0.Status)
	}
	if len(c0.Deps) != 1 || c0.Deps[0].Status != "unhealthy" {
		t.Fatalf("dep must still be listed: %+v", c0.Deps)
	}
}

// TestHealthViewContextFunnel pins the sealed ctx funnel: round-trip + absence.
func TestHealthViewContextFunnel(t *testing.T) {
	if _, ok := HealthViewFromContext(context.Background()); ok {
		t.Fatal("absent HealthView must report ok=false (fail-closed)")
	}
	want := New(fakeAsm{}, fakeAgg{})
	ctx := WithHealthView(context.Background(), want)
	got, ok := HealthViewFromContext(ctx)
	if !ok || got != want {
		t.Fatalf("round-trip failed: ok=%v got=%v", ok, got)
	}
}
