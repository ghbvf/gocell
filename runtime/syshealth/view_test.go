package syshealth

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/metadata"
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
		{Name: mustName(t, "cellone_repo_ready"), Status: healthz.StatusUp, Latency: 2 * time.Millisecond},
		{Name: mustName(t, "celltwo_repo_ready"), Status: healthz.StatusDegraded, Latency: 3 * time.Millisecond},
		{Name: mustName(t, "postgres_ready"), Status: healthz.StatusUp, Latency: 1 * time.Millisecond},
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
