package bootstrap

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestTopologyFromEnv_PostgresRequiresReal(t *testing.T) {
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "postgres")
	// GOCELL_ADAPTER_MODE is not set (empty = dev mode)

	_, err := TopologyFromEnv()
	if err == nil {
		t.Fatal("expected error when postgres storage requires real adapter mode, got nil")
	}
}

func TestTopologyFromEnv_MemoryAllowsAnyMode(t *testing.T) {
	tests := []struct {
		name        string
		adapterMode string
	}{
		{"memory with empty adapter mode", ""},
		{"memory with real adapter mode", "real"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOCELL_CELL_ADAPTER_MODE", "memory")
			t.Setenv("GOCELL_ADAPTER_MODE", tc.adapterMode)

			topo, err := TopologyFromEnv()
			if err != nil {
				t.Fatalf("expected no error for memory + %q adapter mode, got %v", tc.adapterMode, err)
			}
			if topo.StorageBackend != "memory" {
				t.Errorf("expected StorageBackend=memory, got %q", topo.StorageBackend)
			}
		})
	}
}

func TestTopologyFromEnv_EmptyCellAdapterModeDefaultsToMemory(t *testing.T) {
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "")
	t.Setenv("GOCELL_ADAPTER_MODE", "")

	topo, err := TopologyFromEnv()
	if err != nil {
		t.Fatalf("expected no error for empty modes (memory default), got %v", err)
	}
	if topo.StorageBackend != "memory" {
		t.Errorf("expected StorageBackend=memory, got %q", topo.StorageBackend)
	}
}

func TestTopologyFromEnv_UnknownStorageBackend(t *testing.T) {
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "unknownbackend")
	t.Setenv("GOCELL_ADAPTER_MODE", "")

	_, err := TopologyFromEnv()
	if err == nil {
		t.Fatal("expected error for unknown storage backend, got nil")
	}
}

// TestTopologyFromEnv_UnknownAdapterMode guards the adapter-mode allowlist:
// illegal GOCELL_ADAPTER_MODE values (typos, unknown modes) must fail-fast
// with an unambiguous error instead of silently taking the dev path.
//
// ref: kubernetes/kubernetes cmd/kube-apiserver/app/server.go — Validate
// rejects unknown flag values before Run starts.
func TestTopologyFromEnv_UnknownAdapterMode(t *testing.T) {
	cases := []string{"production", "fake", "test", "REAL"}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			t.Setenv("GOCELL_CELL_ADAPTER_MODE", "memory")
			t.Setenv("GOCELL_ADAPTER_MODE", raw)

			_, err := TopologyFromEnv()
			if err == nil {
				t.Fatalf("expected error for illegal GOCELL_ADAPTER_MODE=%q, got nil", raw)
			}
		})
	}
}

func TestTopologyFromEnv_RequireProductionControlPlane(t *testing.T) {
	tests := []struct {
		name        string
		cellMode    string
		adapterMode string
		wantRequire bool
	}{
		{"postgres+real requires production", "postgres", "real", true},
		{"memory+real requires production (real keys still need guards)", "memory", "real", true},
		{"memory+dev does not require production", "memory", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOCELL_CELL_ADAPTER_MODE", tc.cellMode)
			t.Setenv("GOCELL_ADAPTER_MODE", tc.adapterMode)

			topo, err := TopologyFromEnv()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := topo.RequireProductionControlPlane()
			if got != tc.wantRequire {
				t.Errorf("RequireProductionControlPlane() = %v, want %v", got, tc.wantRequire)
			}
		})
	}
}

func TestTopologyFromEnv_AdapterInfo(t *testing.T) {
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "memory")
	t.Setenv("GOCELL_ADAPTER_MODE", "")

	topo, err := TopologyFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	info := topo.AdapterInfo()
	if info == nil {
		t.Fatal("AdapterInfo() must not return nil")
	}
	if _, ok := info["storage"]; !ok {
		t.Error("AdapterInfo() must contain 'storage' key")
	}
}

// TestTopologyValidate_AllErrorsAreErrcode guards the classifier-drift invariant:
// every validate() error path must return an errcode.Error so callers (startup
// runner, /readyz, operators) see a uniform ERR_VALIDATION_FAILED classification
// instead of a mix of errcode and bare fmt.Errorf.
func TestTopologyValidate_AllErrorsAreErrcode(t *testing.T) {
	cases := []struct {
		name    string
		cellEnv string
		mode    string
	}{
		{"unknown adapter mode", "memory", "bogus"},
		{"postgres without real", "postgres", ""},
		{"unknown storage backend", "bogusbackend", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOCELL_CELL_ADAPTER_MODE", tc.cellEnv)
			t.Setenv("GOCELL_ADAPTER_MODE", tc.mode)

			_, err := TopologyFromEnv()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) {
				t.Fatalf("error is not *errcode.Error: %T %v", err, err)
			}
			if ec.Code != errcode.ErrValidationFailed {
				t.Errorf("expected code %s, got %s", errcode.ErrValidationFailed, ec.Code)
			}
		})
	}
}

func TestTopologyAdapterInfo_PostgresMode(t *testing.T) {
	topo := Topology{
		StorageBackend: "postgres",
		AdapterMode:    "real",
	}

	info := topo.AdapterInfo()
	if info == nil {
		t.Fatal("AdapterInfo() must not return nil")
	}
	if got := info["storage"]; got != "postgres" {
		t.Errorf("expected storage=postgres, got %q", got)
	}
	if got := info["outbox_storage"]; got != "postgres" {
		t.Errorf("expected outbox_storage=postgres, got %q", got)
	}
}
