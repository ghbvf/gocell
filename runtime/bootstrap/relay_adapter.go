package bootstrap

// relay_adapter.go — single sanctioned holder of *runtime/outbox.Relay as a
// kernel/lifecycle.ManagedResource.
//
// *Relay intentionally does not implement ManagedResource directly: callers
// cannot pass it through WithManagedResource (compile-time type-mismatch). The
// only path that integrates a relay into Bootstrap's managed-resource pipeline
// is WithRelay → newRelayAdapter, which is package-private. See ADR
// docs/architecture/202605201400-adr-relay-managedresource-isolation.md and
// the §"single sanctioned holder" Hard 范本 in .claude/rules/gocell/ai-collab.md.
//
// archtest RELAY-NOT-MANAGEDRESOURCE-01 (tools/archtest) locks the downstream
// invariant; the package-private adapter constructor locks the upstream half.

import (
	"context"

	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	kworker "github.com/ghbvf/gocell/kernel/worker"
	runtimeoutbox "github.com/ghbvf/gocell/runtime/outbox"
)

// relayAdapter wraps a *runtimeoutbox.Relay so the bootstrap managed-resource
// pipeline can drive its lifecycle (Checkers / Worker / Close) without exposing
// ManagedResource on the relay surface itself.
type relayAdapter struct {
	relay *runtimeoutbox.Relay
}

// Compile-time check: the adapter — and only the adapter — implements ManagedResource.
var _ kernellifecycle.ManagedResource = (*relayAdapter)(nil)

// newRelayAdapter wraps r so the bootstrap managed-resource pipeline can drive
// its lifecycle. Package-private so external callers cannot construct it; the
// only entry point is WithRelay.
func newRelayAdapter(r *runtimeoutbox.Relay) *relayAdapter {
	return &relayAdapter{relay: r}
}

// Checkers forwards to the relay's failure-budget probes.
func (a *relayAdapter) Checkers() map[string]func(context.Context) error {
	return a.relay.Checkers()
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
