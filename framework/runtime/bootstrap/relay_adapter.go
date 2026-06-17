package bootstrap

// relay_adapter.go — single sanctioned holder of *runtime/outbox.Relay as a
// kernel/lifecycle.ManagedResource.
//
// *Relay intentionally does not implement ManagedResource directly: callers
// cannot pass it through WithManagedResource (compile-time type-mismatch). The
// only path that integrates a relay into Bootstrap's managed-resource pipeline
// is WithRelay → newRelayAdapter, which is package-private. See ADR
// docs/architecture/202605201400-adr-relay-managedresource-isolation.md and
// the §"single sanctioned holder" Hard 范本 in .claude/rules/gocell/ai-robust.md.
//
// archtest RELAY-NOT-MANAGEDRESOURCE-01 (tools/archtest) locks the downstream
// invariant; the package-private adapter constructor locks the upstream half.

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/healthz"
	kernellifecycle "github.com/ghbvf/gocell/framework/kernel/lifecycle"
	kworker "github.com/ghbvf/gocell/framework/kernel/worker"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	runtimeoutbox "github.com/ghbvf/gocell/framework/runtime/outbox"
)

// relayAdapter wraps a *runtimeoutbox.Relay so the bootstrap managed-resource
// pipeline can drive its lifecycle (Probes / Worker / Close) without exposing
// ManagedResource on the relay surface itself.
type relayAdapter struct {
	relay *runtimeoutbox.Relay
	// namespaced holds per-instance-renamed probes for a non-default infra
	// instance (#2152 PR-1). nil for the colocated default key, where Probes()
	// forwards the relay's bare-named probes live (operations contract
	// unchanged). For a fanned-out instance it is pre-built so N relays expose
	// globally-distinct probe names (expandManagedResources fails fast on a
	// duplicate name).
	namespaced []healthz.Probe
}

// Compile-time check: the adapter — and only the adapter — implements ManagedResource.
var _ kernellifecycle.ManagedResource = (*relayAdapter)(nil)

// newRelayAdapter wraps r for the infra instance identified by key so the
// bootstrap managed-resource pipeline can drive its lifecycle. Package-private
// so external callers cannot construct it; the only entry point is WithRelay.
//
// For a non-default key the relay's probe names are scoped by the instance id
// (outbox_relay_poll → outbox_relay_poll_<id>) so multiple fanned-out relays do
// not collide on the global probe-name namespace. The colocated default keeps
// the bare names.
func newRelayAdapter(key InfraInstanceKey, r *runtimeoutbox.Relay) *relayAdapter {
	a := &relayAdapter{relay: r}
	if key == DefaultInstanceKey() {
		return a
	}
	base := r.Probes()
	a.namespaced = make([]healthz.Probe, len(base))
	for i, p := range base {
		name, err := healthz.RelayInstanceProbeName(p.Name(), key.id)
		if err != nil {
			// Unreachable: key.id is a validated snake_case identifier (mint-time
			// contract of NewInfraInstanceKey), so the composed name is always valid.
			panic(panicregister.Approved("bootstrap-relay-probe-name",
				errcode.Assertion("bootstrap: relay instance probe name composition failed for a validated key")))
		}
		a.namespaced[i] = healthz.NewProbe(name, p.Check)
	}
	return a
}

// Probes forwards to the relay's typed failure-budget probes, instance-scoped
// when this adapter wraps a non-default infra instance.
func (a *relayAdapter) Probes() []healthz.Probe {
	if a.namespaced != nil {
		return a.namespaced
	}
	return a.relay.Probes()
}

// Worker returns the relay itself as the background worker; bootstrap drives
// Start/Stop through the worker contract.
func (a *relayAdapter) Worker() kworker.Worker {
	return a.relay.Worker()
}

// Close stops the relay during LIFO teardown.
func (a *relayAdapter) Close(ctx context.Context) error {
	return a.relay.Stop(ctx)
}
