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
// the N pools + verifies schema from the resolved per-instance Configs; that I/O
// path is covered by the real-PG integration test corebundle_pg_env_integration_test.go.
//
// Post-#2341 Resolve returns a Resolution (Instances keyed by InfraInstanceKey +
// CellToInstance map) and NO LONGER fail-closes on >1 distinct DSN: colocated
// yields a single instance keyed DefaultInstanceKey(); split yields one instance
// per DSN group keyed NewInfraInstanceKey(rep). The empty-cell-set and empty-DSN
// fail-closed gates are retained.

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

// TestResolve_Colocated: all cells sharing one DSN dedup to a SINGLE instance keyed
// by DefaultInstanceKey() (preserving bare relay probe names), with every cell
// mapped to that instance and RequireRestrictedRole applied to its Config.
func TestResolve_Colocated(t *testing.T) {
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
	res, ok, err := Resolve(topo, cfg)
	if err != nil {
		t.Fatalf("Resolve(colocated): unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("Resolve(colocated): want ok=true")
	}
	if len(res.Instances) != 1 {
		t.Fatalf("len(Instances) = %d, want 1 (colocated dedup)", len(res.Instances))
	}
	def := bootstrap.DefaultInstanceKey()
	inst, has := res.Instances[def]
	if !has {
		t.Fatalf("Instances missing DefaultInstanceKey(); keys=%v", instanceKeys(res))
	}
	if inst.DSN != dsn {
		t.Errorf("instance.DSN = %q, want %q", inst.DSN, dsn)
	}
	if !inst.RequireRestrictedRole {
		t.Error("instance.RequireRestrictedRole should be true (applied from Config)")
	}
	for _, c := range []string{"accesscore", "auditcore", "configcore"} {
		if got := res.CellToInstance[c]; got != def {
			t.Errorf("CellToInstance[%q] = %v, want DefaultInstanceKey()", c, got)
		}
	}
}

// TestResolve_FullSplit: three distinct DSNs yield three instances, each keyed by
// NewInfraInstanceKey(cellID) and each cell mapped to its own instance. This is the
// #2341 unlock — no longer fail-closed.
func TestResolve_FullSplit(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "real", "postgres", true)
	cfg := Config{
		Cells: map[string]adapterpg.Config{
			"accesscore": {DSN: "postgres://host/access"},
			"auditcore":  {DSN: "postgres://host/audit"},
			"configcore": {DSN: "postgres://host/config"},
		},
		RequireRestrictedRole: true,
	}
	res, ok, err := Resolve(topo, cfg)
	if err != nil {
		t.Fatalf("Resolve(full split): unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("Resolve(full split): want ok=true")
	}
	if len(res.Instances) != 3 {
		t.Fatalf("len(Instances) = %d, want 3 (full split)", len(res.Instances))
	}
	for cell, dsn := range map[string]string{
		"accesscore": "postgres://host/access",
		"auditcore":  "postgres://host/audit",
		"configcore": "postgres://host/config",
	} {
		key := bootstrap.NewInfraInstanceKey(cell)
		if got := res.CellToInstance[cell]; got != key {
			t.Errorf("CellToInstance[%q] = %v, want NewInfraInstanceKey(%q)", cell, got, cell)
		}
		inst, has := res.Instances[key]
		if !has {
			t.Fatalf("Instances missing key for cell %q", cell)
		}
		if inst.DSN != dsn {
			t.Errorf("instance[%q].DSN = %q, want %q", cell, inst.DSN, dsn)
		}
		if !inst.RequireRestrictedRole {
			t.Errorf("instance[%q].RequireRestrictedRole should be true", cell)
		}
	}
}

// TestResolve_PartialSplit: two cells share DSN-A and one cell has DSN-B. The shared
// group collapses to a SINGLE instance keyed by its alphabetically-first cell (rep),
// while the lone cell gets its own instance. Both shared cells map to the rep's key.
func TestResolve_PartialSplit(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "real", "postgres", true)
	const shared = "postgres://host/shared"
	cfg := Config{
		Cells: map[string]adapterpg.Config{
			"auditcore":  {DSN: shared},
			"accesscore": {DSN: shared},
			"configcore": {DSN: "postgres://host/config"},
		},
	}
	res, ok, err := Resolve(topo, cfg)
	if err != nil {
		t.Fatalf("Resolve(partial split): unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("Resolve(partial split): want ok=true")
	}
	if len(res.Instances) != 2 {
		t.Fatalf("len(Instances) = %d, want 2 (1 shared group + 1 lone)", len(res.Instances))
	}
	// rep of {accesscore, auditcore} is alphabetically-first: accesscore.
	repKey := bootstrap.NewInfraInstanceKey("accesscore")
	if got := res.CellToInstance["accesscore"]; got != repKey {
		t.Errorf("CellToInstance[accesscore] = %v, want rep key NewInfraInstanceKey(accesscore)", got)
	}
	if got := res.CellToInstance["auditcore"]; got != repKey {
		t.Errorf("CellToInstance[auditcore] = %v, want shared rep key (same pool as accesscore)", got)
	}
	if inst := res.Instances[repKey]; inst.DSN != shared {
		t.Errorf("shared instance.DSN = %q, want %q", inst.DSN, shared)
	}
	configKey := bootstrap.NewInfraInstanceKey("configcore")
	if got := res.CellToInstance["configcore"]; got != configKey {
		t.Errorf("CellToInstance[configcore] = %v, want its own key", got)
	}
}

// TestResolve_SingleCell: one postgres cell yields one instance keyed by
// DefaultInstanceKey() (1 distinct DSN), carrying its pool knobs.
func TestResolve_SingleCell(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "real", "postgres", true)
	const dsn = "postgres://host/db"
	cfg := Config{Cells: map[string]adapterpg.Config{"configcore": {DSN: dsn, MaxConns: 10}}}
	res, ok, err := Resolve(topo, cfg)
	if err != nil {
		t.Fatalf("Resolve(single cell): unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("Resolve(single cell): want ok=true")
	}
	if len(res.Instances) != 1 {
		t.Fatalf("len(Instances) = %d, want 1", len(res.Instances))
	}
	inst := res.Instances[bootstrap.DefaultInstanceKey()]
	if inst.DSN != dsn || inst.MaxConns != 10 {
		t.Errorf("instance = %+v, want DSN=%q MaxConns=10", inst, dsn)
	}
}

func instanceKeys(res Resolution) []bootstrap.InfraInstanceKey {
	keys := make([]bootstrap.InfraInstanceKey, 0, len(res.Instances))
	for k := range res.Instances {
		keys = append(keys, k)
	}
	return keys
}
