package percellpg

import (
	"strings"
	"testing"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
)

func mkTopo(t *testing.T, adapterMode, storageBackend string, singlePod bool) bootstrap.Topology {
	t.Helper()
	topo, err := bootstrap.NewTopology(adapterMode, storageBackend, singlePod)
	if err != nil {
		t.Fatalf("NewTopology(%q,%q,%v): %v", adapterMode, storageBackend, singlePod, err)
	}
	return topo
}

// Resolve is a pure decision function (no I/O), so every branch is exhaustively
// unit-testable here. The composition root (cmd/corebundle/cap_wiring.go) opens
// the pool + verifies schema from the agreed Config; that I/O path is covered by
// the real-PG integration test corebundle_pg_env_integration_test.go.

// TestResolve_MemoryTopology: non-postgres topology returns ok=false, no error —
// no pool is needed and the per-cell DSNs are never read.
func TestResolve_MemoryTopology(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "", "memory", false)
	cfg := Config{Cells: map[string]adapterpg.Config{"cellA": {DSN: "postgres://x/db"}}}
	_, ok, err := Resolve(topo, cfg)
	if err != nil {
		t.Fatalf("Resolve(memory): unexpected error: %v", err)
	}
	if ok {
		t.Fatal("Resolve(memory): want ok=false (no pool in memory mode)")
	}
}

// TestResolve_EmptyCells_Postgres: postgres topology with an empty Cells map is a
// fail-closed misconfiguration (and must not panic on the cellIDs[0] index).
func TestResolve_EmptyCells_Postgres(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "real", "postgres", true)
	_, ok, err := Resolve(topo, Config{Cells: nil})
	if err == nil {
		t.Fatal("Resolve(postgres, empty Cells) = nil error, want fail-closed startup error")
	}
	if ok {
		t.Error("Resolve(postgres, empty Cells): want ok=false")
	}
	if !strings.Contains(err.Error(), "empty cell set") {
		t.Errorf("error = %q, want it to mention 'empty cell set'", err)
	}
}

// TestResolve_MissingDSN: a postgres-requiring cell with an empty DSN is fail-closed
// with the cell ID + expected env var in the diagnostic context.
func TestResolve_MissingDSN(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "real", "postgres", true)
	cfg := Config{Cells: map[string]adapterpg.Config{"configcore": {DSN: ""}}}
	_, _, err := Resolve(topo, cfg)
	if err == nil {
		t.Fatal("Resolve(missing DSN) = nil error, want fail-closed startup error")
	}
	if !strings.Contains(err.Error(), "configcore") {
		t.Errorf("error = %q, want it to mention cell ID 'configcore'", err)
	}
	if !strings.Contains(err.Error(), "GOCELL_CONFIGCORE_DATABASE_URL") {
		t.Errorf("error = %q, want it to mention the GOCELL_CONFIGCORE_DATABASE_URL env var", err)
	}
}

// TestResolve_DistinctDSNs: two cells with different DSNs fail-closed, pointing to
// split topology (US4 #1963 / #2152) with diagnostic distinct_dsn_count.
func TestResolve_DistinctDSNs(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "real", "postgres", true)
	cfg := Config{Cells: map[string]adapterpg.Config{
		"auditcore":  {DSN: "postgres://host1/db1"},
		"configcore": {DSN: "postgres://host2/db2"},
	}}
	_, _, err := Resolve(topo, cfg)
	if err == nil {
		t.Fatal("Resolve(distinct DSNs) = nil error, want fail-closed startup error")
	}
	// errcode.Error() renders the WithInternal attrs (not the const message) when
	// internal details are attached, so assert on the diagnostic context + code.
	if !strings.Contains(err.Error(), "distinct_dsn_count=2") {
		t.Errorf("error = %q, want internal diagnostic 'distinct_dsn_count=2'", err)
	}
	if !strings.Contains(err.Error(), "ERR_VALIDATION_FAILED") {
		t.Errorf("error = %q, want ERR_VALIDATION_FAILED code", err)
	}
}

// TestResolve_SameDSN: all cells sharing one DSN dedup to a single agreed Config,
// ok=true, with the caller's RequireRestrictedRole applied.
func TestResolve_SameDSN(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "real", "postgres", true)
	const dsn = "postgres://host/db"
	cfg := Config{
		Cells: map[string]adapterpg.Config{
			"auditcore":  {DSN: dsn},
			"configcore": {DSN: dsn},
			"accesscore": {DSN: dsn},
		},
		RequireRestrictedRole: true,
	}
	agreed, ok, err := Resolve(topo, cfg)
	if err != nil {
		t.Fatalf("Resolve(same DSN): unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("Resolve(same DSN): want ok=true")
	}
	if agreed.DSN != dsn {
		t.Errorf("agreed.DSN = %q, want %q", agreed.DSN, dsn)
	}
	if !agreed.RequireRestrictedRole {
		t.Error("agreed.RequireRestrictedRole should be true (applied from Config)")
	}
}

// TestResolve_DeterministicOrder: when multiple cells are missing DSNs, the error
// names the alphabetically-first cell (stable sorted order).
func TestResolve_DeterministicOrder(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "real", "postgres", true)
	cfg := Config{Cells: map[string]adapterpg.Config{
		"zebracell":  {DSN: ""},
		"alphacell":  {DSN: ""},
		"middlecell": {DSN: ""},
	}}
	_, _, err := Resolve(topo, cfg)
	if err == nil {
		t.Fatal("want fail-closed error, got nil")
	}
	if !strings.Contains(err.Error(), "alphacell") {
		t.Errorf("error = %q, want 'alphacell' (first sorted cell with missing DSN)", err)
	}
}

// TestResolve_WhitespaceOnlyDSN: a whitespace-only DSN is treated as empty (missing).
func TestResolve_WhitespaceOnlyDSN(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "real", "postgres", true)
	cfg := Config{Cells: map[string]adapterpg.Config{"configcore": {DSN: "   "}}}
	_, _, err := Resolve(topo, cfg)
	if err == nil {
		t.Fatal("whitespace-only DSN should be rejected as empty (fail-closed)")
	}
}

// TestResolve_SingleCell: one cell with a valid DSN resolves, carrying its pool knobs.
func TestResolve_SingleCell(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "real", "postgres", true)
	const dsn = "postgres://host/db"
	cfg := Config{Cells: map[string]adapterpg.Config{"configcore": {DSN: dsn, MaxConns: 10}}}
	agreed, ok, err := Resolve(topo, cfg)
	if err != nil {
		t.Fatalf("Resolve(single cell): unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("Resolve(single cell): want ok=true")
	}
	if agreed.DSN != dsn || agreed.MaxConns != 10 {
		t.Errorf("agreed = %+v, want DSN=%q MaxConns=10", agreed, dsn)
	}
}

// TestResolve_RequireRestrictedRoleOverride: the caller's Config.RequireRestrictedRole
// always wins over the individual cell's adapterpg.Config value (the composition root
// controls the serving-pool flag; per-cell values are intentionally overridden).
func TestResolve_RequireRestrictedRoleOverride(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "real", "postgres", true)
	const dsn = "postgres://host/db"
	tests := []struct {
		name     string
		cellRole bool
		cfgRole  bool
		want     bool
	}{
		{"cell=true cfg=false: caller false wins", true, false, false},
		{"cell=false cfg=true: caller true wins", false, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{
				Cells:                 map[string]adapterpg.Config{"configcore": {DSN: dsn, RequireRestrictedRole: tc.cellRole}},
				RequireRestrictedRole: tc.cfgRole,
			}
			agreed, ok, err := Resolve(topo, cfg)
			if err != nil || !ok {
				t.Fatalf("Resolve: ok=%v err=%v", ok, err)
			}
			if agreed.RequireRestrictedRole != tc.want {
				t.Errorf("agreed.RequireRestrictedRole = %v, want %v", agreed.RequireRestrictedRole, tc.want)
			}
		})
	}
}
