package bootstrap

import (
	"testing"
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

func TestTopologyFromEnv_RequireProductionControlPlane(t *testing.T) {
	tests := []struct {
		name        string
		cellMode    string
		adapterMode string
		wantRequire bool
	}{
		{"postgres requires production", "postgres", "real", true},
		{"memory does not require production (empty mode)", "memory", "", false},
		{"memory does not require production (real mode)", "memory", "real", false},
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
