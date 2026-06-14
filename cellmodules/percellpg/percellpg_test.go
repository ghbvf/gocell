package percellpg

import (
	"context"
	"errors"
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
			"auditcore":  {DSN: "postgres://host1/db1"},
			"configcore": {DSN: "postgres://host2/db2"},
		},
	}
	_, _, err := resolveAgreedConfig(topo, cfg)
	if err == nil {
		t.Fatal("resolveAgreedConfig(distinct DSNs) = nil error, want fail-closed startup error")
	}
	// Error() shows WithInternal attrs (distinct_dsn_count, cell_ids) instead of the
	// message when internal details are attached. Check that the diagnostic context is
	// present and the error code is correct.
	if !strings.Contains(err.Error(), "distinct_dsn_count") {
		t.Errorf("error = %q, want internal diagnostic 'distinct_dsn_count'", err)
	}
	if !strings.Contains(err.Error(), "ERR_VALIDATION_FAILED") {
		t.Errorf("error = %q, want ERR_VALIDATION_FAILED code", err)
	}
}

// TestResolveAgreedConfig_SameDSN verifies that cells sharing the same DSN
// resolve successfully to a single agreed config with ok=true.
func TestResolveAgreedConfig_SameDSN(t *testing.T) {
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
	const dsn = "postgres://host/db"
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

// TestResolveAgreedConfig_RequireRestrictedRoleOverride verifies that the caller's
// Config.RequireRestrictedRole value always wins over the individual cell's
// adapterpg.Config.RequireRestrictedRole (percellpg.go:145: agreed.RequireRestrictedRole
// = cfg.RequireRestrictedRole). The caller (composition root) controls this flag for
// the shared pool; per-cell values are intentionally overridden.
func TestResolveAgreedConfig_RequireRestrictedRoleOverride(t *testing.T) {
	t.Parallel()
	topo := mkTopo(t, "real", "postgres", true)
	const dsn = "postgres://host/db"

	tests := []struct {
		name           string
		cellRole       bool // adapterpg.Config.RequireRestrictedRole for the cell
		cfgRole        bool // Config.RequireRestrictedRole (caller override)
		wantAgreedRole bool
	}{
		{
			name:           "cell=true cfg=false: caller false wins",
			cellRole:       true,
			cfgRole:        false,
			wantAgreedRole: false,
		},
		{
			name:           "cell=false cfg=true: caller true wins",
			cellRole:       false,
			cfgRole:        true,
			wantAgreedRole: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{
				Cells: map[string]adapterpg.Config{
					"accesscore": {DSN: dsn, RequireRestrictedRole: tc.cellRole},
				},
				RequireRestrictedRole: tc.cfgRole,
			}
			agreed, ok, err := resolveAgreedConfig(topo, cfg)
			if err != nil {
				t.Fatalf("resolveAgreedConfig: unexpected error: %v", err)
			}
			if !ok {
				t.Fatal("resolveAgreedConfig: want ok=true")
			}
			if agreed.RequireRestrictedRole != tc.wantAgreedRole {
				t.Errorf("agreed.RequireRestrictedRole = %v, want %v", agreed.RequireRestrictedRole, tc.wantAgreedRole)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Resolve — top-level, impure (newPool override required for unit tests)
// ---------------------------------------------------------------------------

// TestResolve_MemoryTopology verifies that Resolve returns empty Deps (no
// Provider, no Resources) for memory topology without calling newPool.
// NOT parallel: mutates the package-level newPool seam.
func TestResolve_MemoryTopology(t *testing.T) {
	// Override newPool to fail if called — it must NOT be called in memory topology.
	orig := newPool
	t.Cleanup(func() { newPool = orig })
	newPool = func(_ context.Context, _ adapterpg.Config) (*adapterpg.Pool, error) {
		t.Fatal("newPool must not be called in memory topology")
		panic("unreachable: t.Fatal above aborts the test goroutine")
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

// TestResolve_OpenPoolError verifies that a pool-open failure (unreachable DB,
// bad credentials) surfaces as a wrapped fail-closed error and no partial Deps —
// not a silent degrade. NOT parallel: it mutates the package-level newPool seam.
//
// The success path (open → verifyPGPreconditions → wrap into capability.PGProvider)
// requires a live postgres (adapterpg.NewPool pings eagerly; *adapterpg.Pool cannot
// be faked), so it is covered by the real-PG integration test
// cmd/corebundle/corebundle_pg_env_integration_test.go — the same coverage the
// relocated verify* helpers had while they lived in cap_wiring.go.
func TestResolve_OpenPoolError(t *testing.T) {
	orig := newPool
	t.Cleanup(func() { newPool = orig })
	wantErr := errors.New("dial tcp 127.0.0.1:1: connection refused")
	newPool = func(_ context.Context, _ adapterpg.Config) (*adapterpg.Pool, error) {
		return nil, wantErr
	}

	topo := mkTopo(t, "real", "postgres", true)
	cfg := Config{Cells: map[string]adapterpg.Config{"configcore": {DSN: "postgres://host/db"}}}
	deps, err := Resolve(context.Background(), newClk(), topo, cfg)
	if err == nil {
		t.Fatal("Resolve(open error) = nil error, want wrapped fail-closed error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want it to wrap %v", err, wantErr)
	}
	if deps.Provider != nil || len(deps.Resources) != 0 {
		t.Error("Resolve(open error): Deps must be zero-valued, no partial wiring")
	}
}
