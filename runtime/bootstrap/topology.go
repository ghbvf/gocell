package bootstrap

import (
	"fmt"
	"os"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// adapterInfoInMemory is the label reported in AdapterInfo() for components
// that run in-process (no external backend). Extracted so the three mode /
// storage / event_bus keys agree on one spelling and any rename updates all
// call sites at once.
const adapterInfoInMemory = "in-memory"

// Topology captures the resolved runtime topology derived from environment
// variables. It is the single source of truth for adapter-mode / storage-backend
// coupling checks used by bootstrap, health probes, and test harnesses.
//
// ref: uber-go/fx fx.Provide(NewConfig) — single-constructor singleton that
// validates once and is passed everywhere.
// ref: go-kratos/kratos config.Config — full-lifecycle configuration object
// passed through the entire runtime stack.
//
// Sealed construction: all fields are unexported, so a package-external struct
// literal cannot populate (let alone mis-populate) a Topology. The only ways to
// obtain a non-zero Topology are NewTopology / TopologyFromEnv, both of which run
// validate(); the illegal postgres+non-real combination is therefore
// unconstructable outside this package. The zero value Topology{} reads as
// dev/memory (adapterMode "", storageBackend "" → treated as memory) and is safe.
// Field set frozen by TestTopologyZeroExportedFields (TOPOLOGY-SEALED-FIELD-FROZEN-01).
type Topology struct {
	// adapterMode mirrors GOCELL_ADAPTER_MODE: "" (dev) or "real" (production).
	adapterMode string

	// storageBackend mirrors GOCELL_CELL_ADAPTER_MODE: "memory" or "postgres".
	storageBackend string

	// singlePodReplayProtection is set when GOCELL_SINGLE_POD=1, acknowledging
	// that the deployment is single-pod and in-memory replay protection is
	// sufficient. In real adapter mode, an in-memory NonceStore is rejected at
	// startup unless this field is true or a distributed store is injected.
	// Multi-pod deployments must leave this unset and inject a distributed
	// NonceStore via auth.WithServiceTokenNonceStore.
	singlePodReplayProtection bool
}

// AdapterMode returns the resolved GOCELL_ADAPTER_MODE: "" (dev) or "real".
func (t Topology) AdapterMode() string { return t.adapterMode }

// StorageBackend returns the resolved GOCELL_CELL_ADAPTER_MODE: "memory" or
// "postgres". A zero-value Topology returns "" (treated as memory by consumers).
func (t Topology) StorageBackend() string { return t.storageBackend }

// SinglePodReplayProtection reports whether the deployment opted into single-pod
// in-memory replay protection (GOCELL_SINGLE_POD=1).
func (t Topology) SinglePodReplayProtection() bool { return t.singlePodReplayProtection }

// RequiresDistributedReplay reports whether the topology demands a distributed
// (cross-pod) replay-defense posture for the /internal/v1/* service-token guard:
// real adapter mode without the single-pod acknowledgement. In this posture an
// in-memory (single-process) NonceStore is insufficient — only a distributed
// store coordinates replay defense across pods.
//
// This is the single source for the predicate previously copied into
// composition.SharedDeps.requiresDistributedReplay and cmd/corebundle/redis.go;
// both now delegate here so the rule lives in exactly one place.
func (t Topology) RequiresDistributedReplay() bool {
	return t.RequireProductionControlPlane() && !t.singlePodReplayProtection
}

// NewTopology validates an adapter-mode / storage-backend / single-pod
// combination and returns the sealed Topology. An empty storageBackend is
// normalized to "memory" (the dev default). The postgres+non-real coupling and
// the AdapterMode allowlist are enforced via validate(); invalid combinations
// return an error and a zero Topology — they cannot be constructed.
//
// This is the single validating constructor; TopologyFromEnv delegates here so
// the normalization and coupling rules live in exactly one place.
func NewTopology(adapterMode, storageBackend string, singlePod bool) (Topology, error) {
	if storageBackend == "" {
		storageBackend = "memory"
	}
	t := Topology{
		adapterMode:               adapterMode,
		storageBackend:            storageBackend,
		singlePodReplayProtection: singlePod,
	}
	if err := t.validate(); err != nil {
		return Topology{}, err
	}
	return t, nil
}

// TopologyFromEnv reads GOCELL_CELL_ADAPTER_MODE and GOCELL_ADAPTER_MODE,
// validates their combination, and returns a Topology.
//
// Coupling rule: postgres storage requires GOCELL_ADAPTER_MODE=real so
// production key loading, token-guarded /metrics, and token-guarded
// /readyz?verbose are all enforced. The check is implemented in
// Topology.validate below and is the sole authoritative enforcement.
//
// ref: go-zero serviceconf — single config drives all gates; misalignment is fatal.
func TopologyFromEnv() (Topology, error) {
	singlePod := os.Getenv("GOCELL_SINGLE_POD")
	return NewTopology(
		os.Getenv("GOCELL_ADAPTER_MODE"),
		os.Getenv("GOCELL_CELL_ADAPTER_MODE"),
		singlePod == "1" || singlePod == "true",
	)
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
	switch t.adapterMode {
	case "", "real":
		// allowlisted; proceed to storage coupling check.
	default:
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"unknown GOCELL_ADAPTER_MODE; known values: \"\" (unset = dev) or \"real\"",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("mode=%q", t.adapterMode))))
	}

	switch t.storageBackend {
	case "memory":
		// memory allows any adapter mode
		return nil
	case "postgres":
		if t.adapterMode != "real" {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"GOCELL_CELL_ADAPTER_MODE=postgres requires GOCELL_ADAPTER_MODE=real "+
					"(real persistence demands production key loading, "+
					"token-guarded /metrics, and token-guarded /readyz?verbose)")
		}
		return nil
	default:
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"unknown GOCELL_CELL_ADAPTER_MODE; known values: \"\" (unset = memory) or \"postgres\"",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("backend=%q", t.storageBackend))))
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
	return t.adapterMode == "real"
}

// AdapterInfo returns a map of topology metadata for the /readyz?verbose
// response. Operators can confirm which backends are active without reading
// logs.
//
// ref: go-micro service metadata — mode changes must be visible to observers.
func (t Topology) AdapterInfo() map[string]string {
	storageMode := adapterInfoInMemory
	outboxStorage := adapterInfoInMemory
	if t.storageBackend == "postgres" {
		storageMode = "postgres"
		outboxStorage = "postgres"
	}

	effectiveMode := adapterInfoInMemory
	if t.adapterMode == "real" {
		effectiveMode = "real-keys-" + storageMode + "-storage"
	}

	return map[string]string{
		"mode":           effectiveMode,
		"storage":        storageMode,
		"event_bus":      adapterInfoInMemory, // in-process eventbus; relay forwards PG outbox entries into it
		"outbox_storage": outboxStorage,
	}
}
