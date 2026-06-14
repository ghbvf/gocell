package percellpg

import (
	"context"
	"strings"
	"testing"
	"time"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
)

func newClk() *clockmock.FakeClock {
	return clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
}

func mkTopo(t *testing.T, adapterMode, storageBackend string, singlePod bool) bootstrap.Topology {
	t.Helper()
	topo, err := bootstrap.NewTopology(adapterMode, storageBackend, singlePod)
	if err != nil {
		t.Fatalf("NewTopology(%q,%q,%v): %v", adapterMode, storageBackend, singlePod, err)
	}
	return topo
}

// ---------------------------------------------------------------------------
// resolveAgreedConfig — pure function, white-box tests
// ---------------------------------------------------------------------------

// TestResolveAgreedConfig_MemoryTopology verifies that a non-postgres topology
// (memory) returns ok=false with no error: no pool needed for in-memory mode.
func TestResolveAgreedConfig_MemoryTopology(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "", "memory", false)
	cfg := Config{
		Cells: map[string]adapterpg.Config{
			"cellA": {DSN: "postgres://x/db"},
		},
	}
	_, ok, err := resolveAgreedConfig(topo, cfg)
	if err != nil {
		t.Fatalf("resolveAgreedConfig(memory): unexpected error: %v", err)
	}
	if ok {
		t.Fatal("resolveAgreedConfig(memory): want ok=false (no pool in memory mode)")
	}
}

// TestResolveAgreedConfig_MissingDSN verifies that a postgres-requiring cell
// with an empty DSN is fail-closed with an error mentioning the cell and its
// expected env var pattern.
func TestResolveAgreedConfig_MissingDSN(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "real", "postgres", true)
	cfg := Config{
		Cells: map[string]adapterpg.Config{
			"configcore": {DSN: ""},
		},
	}
	_, _, err := resolveAgreedConfig(topo, cfg)
	if err == nil {
		t.Fatal("resolveAgreedConfig(missing DSN) = nil error, want fail-closed startup error")
	}
	// Error must mention the cell ID and the env var pattern (in Internal attrs,
	// not string-interpolated into the const message — but the error.Error() string
	// includes the internal context for test assertion purposes).
	if !strings.Contains(err.Error(), "configcore") {
		t.Errorf("error = %q, want it to mention cell ID 'configcore'", err)
	}
	if !strings.Contains(err.Error(), "GOCELL_CONFIGCORE_DATABASE_URL") {
		t.Errorf("error = %q, want it to mention GOCELL_CONFIGCORE_DATABASE_URL env var pattern", err)
	}
}

// TestResolveAgreedConfig_DistinctDSNs verifies that two cells with different
// DSNs cause a fail-closed error pointing to split topology and US4 #1963.
func TestResolveAgreedConfig_DistinctDSNs(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "real", "postgres", true)
	cfg := Config{
		Cells: map[string]adapterpg.Config{
			"auditcore":  {DSN: "postgres://user:pass@host1/db1"},
			"configcore": {DSN: "postgres://user:pass@host2/db2"},
		},
	}
	_, _, err := resolveAgreedConfig(topo, cfg)
	if err == nil {
		t.Fatal("resolveAgreedConfig(distinct DSNs) = nil error, want fail-closed startup error")
	}
	if !strings.Contains(err.Error(), "distinct") {
		t.Errorf("error = %q, want mention of 'distinct'", err)
	}
	if !strings.Contains(err.Error(), "#1963") {
		t.Errorf("error = %q, want mention of US4 #1963", err)
	}
}

// TestResolveAgreedConfig_SameDSN verifies that cells sharing the same DSN
// resolve successfully to a single agreed config with ok=true.
func TestResolveAgreedConfig_SameDSN(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "real", "postgres", true)
	const dsn = "postgres://user:pass@host/db"
	cfg := Config{
		Cells: map[string]adapterpg.Config{
			"auditcore":  {DSN: dsn},
			"configcore": {DSN: dsn},
			"accesscore": {DSN: dsn},
		},
		RequireRestrictedRole: true,
	}
	agreed, ok, err := resolveAgreedConfig(topo, cfg)
	if err != nil {
		t.Fatalf("resolveAgreedConfig(same DSN): unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("resolveAgreedConfig(same DSN): want ok=true")
	}
	if agreed.DSN != dsn {
		t.Errorf("agreed.DSN = %q, want %q", agreed.DSN, dsn)
	}
	if !agreed.RequireRestrictedRole {
		t.Error("agreed.RequireRestrictedRole should be true (passed via Config)")
	}
}

// TestResolveAgreedConfig_DeterministicOrder verifies that when multiple cells
// are missing DSNs, the error is reported for the alphabetically first cell
// (sorted cellID order), ensuring deterministic error messages.
func TestResolveAgreedConfig_DeterministicOrder(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "real", "postgres", true)
	cfg := Config{
		Cells: map[string]adapterpg.Config{
			"zebracell":  {DSN: ""},
			"alphacell":  {DSN: ""},
			"middlecell": {DSN: ""},
		},
	}
	_, _, err := resolveAgreedConfig(topo, cfg)
	if err == nil {
		t.Fatal("want fail-closed error, got nil")
	}
	// alphacell sorts first; error must name it
	if !strings.Contains(err.Error(), "alphacell") {
		t.Errorf("error = %q, want 'alphacell' (first sorted cell with missing DSN)", err)
	}
}

// TestResolveAgreedConfig_WhitespaceOnlyDSN verifies that a DSN consisting of
// only whitespace is treated as empty (missing) and triggers the fail-closed gate.
func TestResolveAgreedConfig_WhitespaceOnlyDSN(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "real", "postgres", true)
	cfg := Config{
		Cells: map[string]adapterpg.Config{
			"configcore": {DSN: "   "},
		},
	}
	_, _, err := resolveAgreedConfig(topo, cfg)
	if err == nil {
		t.Fatal("whitespace-only DSN should be rejected as empty (fail-closed)")
	}
}

// TestResolveAgreedConfig_SingleCell verifies that a single cell with a valid
// DSN resolves successfully.
func TestResolveAgreedConfig_SingleCell(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "real", "postgres", true)
	const dsn = "postgres://user:pass@host/db"
	cfg := Config{
		Cells: map[string]adapterpg.Config{
			"configcore": {DSN: dsn, MaxConns: 10},
		},
	}
	agreed, ok, err := resolveAgreedConfig(topo, cfg)
	if err != nil {
		t.Fatalf("resolveAgreedConfig(single cell): unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("resolveAgreedConfig(single cell): want ok=true")
	}
	if agreed.DSN != dsn {
		t.Errorf("agreed.DSN = %q, want %q", agreed.DSN, dsn)
	}
}

// ---------------------------------------------------------------------------
// Resolve — top-level, impure (newPool override required for unit tests)
// ---------------------------------------------------------------------------

// TestResolve_MemoryTopology verifies that Resolve returns empty Deps (no
// Provider, no Resources) for memory topology without calling newPool.
func TestResolve_MemoryTopology(t *testing.T) {
	t.Parallel()
	// Override newPool to fail if called — it must NOT be called in memory topology.
	orig := newPool
	t.Cleanup(func() { newPool = orig })
	newPool = func(_ context.Context, _ adapterpg.Config) (*adapterpg.Pool, error) {
		t.Fatal("newPool must not be called in memory topology")
		return nil, nil
	}

	topo := mkTopo(t, "", "memory", false)
	cfg := Config{
		Cells: map[string]adapterpg.Config{
			"cellA": {DSN: "postgres://x/db"},
		},
	}
	deps, err := Resolve(context.Background(), newClk(), topo, cfg)
	if err != nil {
		t.Fatalf("Resolve(memory): unexpected error: %v", err)
	}
	if deps.Provider != nil {
		t.Error("Resolve(memory): Provider must be nil (no pool in memory mode)")
	}
	if len(deps.Resources) != 0 {
		t.Errorf("Resolve(memory): Resources must be empty, got %d", len(deps.Resources))
	}
}
