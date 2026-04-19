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
//
// Two independent gates:
//  1. AdapterMode allowlist ("" | "real") — illegal values fail-fast so a
//     typo in GOCELL_ADAPTER_MODE cannot silently degrade to the dev path.
//  2. StorageBackend coupling — postgres requires AdapterMode=real so real
//     persistence demands production key loading, token-guarded /metrics,
//     and token-guarded /readyz?verbose.
//
// All errors are returned via errcode.New(ErrValidationFailed, ...) so callers
// can classify startup failures uniformly (Classify → 4xx / IsInfraError →
// false). The bare fmt.Errorf path has been removed to prevent classifier drift.
//
// ref: kubernetes/kubernetes cmd/kube-apiserver/app/server.go —
// Complete → Validate → Run; illegal flag values aggregate into a single
// startup error before any component starts.
// ref: go-zero core/conf/config.go validate(v) — single validation gate at
// the unmarshal boundary, not deferred to downstream consumers.
func (t Topology) validate() error {
	switch t.AdapterMode {
	case "", "real":
		// allowlisted; proceed to storage coupling check.
	default:
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("unknown adapter mode %q (GOCELL_ADAPTER_MODE); known values: \"\" (unset = dev) or \"real\"", t.AdapterMode))
	}

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
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("unknown GOCELL_CELL_ADAPTER_MODE %q; known values: \"\" (unset = memory) or \"postgres\"", t.StorageBackend))
	}
}

// RequireProductionControlPlane returns true when the runtime has opted into
// real (production) keys and therefore requires production-grade control-plane
// guards: token-authenticated /metrics, token-authenticated /readyz?verbose,
// HMAC-guarded /internal/v1/*, and strict (fail-fast) secret loading.
//
// The gate is AdapterMode=="real". Postgres storage implies AdapterMode=="real"
// via the coupling rule enforced in validate(), so postgres topologies also
// return true; memory+real is likewise covered — whenever an operator has
// asked for real keys, the control plane must be protected.
//
// PGResource wiring is a separate concern (see StorageBackend) — the storage
// backend determines whether a PG pool is owned, while this predicate
// determines whether anonymous control-plane access is rejected.
func (t Topology) RequireProductionControlPlane() bool {
	return t.AdapterMode == "real"
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
