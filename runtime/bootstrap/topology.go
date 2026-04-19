package bootstrap

import (
	"fmt"
	"os"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Topology captures the resolved runtime topology derived from environment
// variables. It is the single source of truth for adapter-mode / storage-backend
// coupling checks used by bootstrap, health probes, and test harnesses.
//
// ref: uber-go/fx fx.Provide(NewConfig) — single-constructor singleton that
// validates once and is passed everywhere.
// ref: go-kratos/kratos config.Config — full-lifecycle configuration object
// passed through the entire runtime stack.
type Topology struct {
	// AdapterMode mirrors GOCELL_ADAPTER_MODE: "" (dev) or "real" (production).
	AdapterMode string

	// StorageBackend mirrors GOCELL_CELL_ADAPTER_MODE: "memory" or "postgres".
	StorageBackend string
}

// TopologyFromEnv reads GOCELL_CELL_ADAPTER_MODE and GOCELL_ADAPTER_MODE,
// validates their combination, and returns a Topology.
//
// Coupling rule (mirrors validateModeCoupling in cmd/core-bundle/main.go):
// postgres storage requires GOCELL_ADAPTER_MODE=real so production key loading,
// token-guarded /metrics, and token-guarded /readyz?verbose are all enforced.
//
// ref: go-zero serviceconf — single config drives all gates; misalignment is fatal.
func TopologyFromEnv() (Topology, error) {
	storageBackend := os.Getenv("GOCELL_CELL_ADAPTER_MODE")
	if storageBackend == "" {
		storageBackend = "memory"
	}

	adapterMode := os.Getenv("GOCELL_ADAPTER_MODE")

	topo := Topology{
		AdapterMode:    adapterMode,
		StorageBackend: storageBackend,
	}

	if err := topo.validate(); err != nil {
		return Topology{}, err
	}

	return topo, nil
}

// validate checks that the topology is self-consistent.
func (t Topology) validate() error {
	switch t.StorageBackend {
	case "memory":
		// memory allows any adapter mode
		return nil
	case "postgres":
		if t.AdapterMode != "real" {
			return errcode.New(errcode.ErrValidationFailed,
				"GOCELL_CELL_ADAPTER_MODE=postgres requires GOCELL_ADAPTER_MODE=real "+
					"(real persistence demands production key loading, token-guarded "+
					"/metrics, and token-guarded /readyz?verbose)")
		}
		return nil
	default:
		return fmt.Errorf("unknown GOCELL_CELL_ADAPTER_MODE %q; known values: \"\" (unset = memory) or \"postgres\"", t.StorageBackend)
	}
}

// RequireProductionControlPlane returns true when the storage backend demands
// production-grade key loading and control-plane token guards. Currently
// returns true only for the postgres backend.
//
// Use this to gate /metrics auth, /readyz?verbose auth, and secret loading
// strictness in the composition root.
func (t Topology) RequireProductionControlPlane() bool {
	return t.StorageBackend == "postgres"
}

// AdapterInfo returns a map of topology metadata for the /readyz?verbose
// response. Operators can confirm which backends are active without reading
// logs.
//
// ref: go-micro service metadata — mode changes must be visible to observers.
func (t Topology) AdapterInfo() map[string]string {
	storageMode := "in-memory"
	outboxStorage := "in-memory"
	if t.StorageBackend == "postgres" {
		storageMode = "postgres"
		outboxStorage = "postgres"
	}

	effectiveMode := "in-memory"
	if t.AdapterMode == "real" {
		effectiveMode = "real-keys-" + storageMode + "-storage"
	}

	return map[string]string{
		"mode":           effectiveMode,
		"storage":        storageMode,
		"event_bus":      "in-memory", // in-process eventbus; relay forwards PG outbox entries into it
		"outbox_storage": outboxStorage,
	}
}
