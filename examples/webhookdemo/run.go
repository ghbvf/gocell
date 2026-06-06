// run.go is the hand-written runtime half behind the generated assembly
// entrypoint for webhookdemo. The generated main.go owns the assembly ID, the
// cell order, and the slog seal; this file owns the bootstrap wiring that mounts
// the inbound webhook receiver.
//
// What this demo proves end-to-end:
//   - a contract (kind: webhook, direction: inbound) + a slice with
//     contractUsages[role=webhook-receive] derive reg.RegisterWebhookReceiver
//     (cells/hooks/cell_gen.go) via cellgen;
//   - the receiver mounts on the dedicated cell.WebhookListener (auth = AuthNone,
//     because the HMAC signature IS the application-layer auth — a JWT chain on
//     the primary listener would 401 the request before the verifier runs);
//   - WithWebhookSourceStore seeds the per-sender HMAC secret and
//     WithWebhookClaimer provides idempotency, so a signed request → 200 and a
//     forged request → 401 (see webhook_e2e_test.go).
//
// Demo mode: a single in-memory source secret + in-memory idempotency claimer.
// A real deployment loads per-sender secrets from a secret manager and uses a
// distributed claimer (Redis / PostgreSQL).
//
// Usage:
//
//	go run ./examples/webhookdemo
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	hooks "github.com/ghbvf/gocell/examples/webhookdemo/cells/hooks"
	"github.com/ghbvf/gocell/kernel/assembly"
	kauth "github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/outbox"
	kwh "github.com/ghbvf/gocell/kernel/webhook"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

const (
	// demoSourceID is the single webhook sender this demo accepts. It must match
	// contract.endpoints.inbound.sourceID (webhook.demo.events.v1).
	demoSourceID = "demosource"

	// demoWebhookSecret is a DEMO-ONLY shared HMAC secret (>= 24 bytes, the
	// kwh.NewSource floor). A real deployment loads each sender's secret from a
	// secret manager and seeds it into the SourceStore — it never hardcodes one.
	demoWebhookSecret = "webhookdemo-demo-hmac-secret-key" // #nosec G101 -- demo-only secret (see comment above)
)

// listenerBinding pairs a bind address with an optional pre-bound net.Listener.
// Production passes addr only; the e2e test injects a 127.0.0.1:0 listener so it
// can learn the ephemeral port and send a real HTTP request (mirrors ssobff).
type listenerBinding struct {
	addr string
	ln   net.Listener
}

// webhookdemoAddrs carries the two listener bindings this assembly declares:
// the dedicated webhook receiver listener and the mandatory health listener
// (#673). There is no primary/internal listener — this assembly serves only the
// inbound webhook path, and bootstrap supports an assembly with no primary.
type webhookdemoAddrs struct {
	webhook listenerBinding
	health  listenerBinding
}

// defaultWebhookdemoAddrs returns the fixed demo listener ports used by the
// generated main.go entrypoint (go run ./examples/webhookdemo).
func defaultWebhookdemoAddrs() webhookdemoAddrs {
	return webhookdemoAddrs{
		// Loopback-only by default: the webhook listener is AuthNone (HMAC is the
		// auth) and the demo HMAC secret is public in this repo, so binding all
		// interfaces would let anyone on the network craft a valid signed request.
		// A real deployment exposes it explicitly with a non-demo secret. Port 8084
		// is distinct from the other examples (todoorder :8082, iotdevice :8083) so
		// several demos can run side by side.
		webhook: listenerBinding{addr: "127.0.0.1:8084"},
		health:  listenerBinding{addr: "127.0.0.1:9099"},
	}
}

// listenerOption builds a bootstrap.WithListener option, plumbing a pre-bound
// net.Listener when the binding carries one (test seam).
func listenerOption(ref cell.ListenerRef, b listenerBinding, authChain []kauth.ListenerAuth) bootstrap.Option {
	var opts []bootstrap.ListenerOption
	if b.ln != nil {
		opts = append(opts, bootstrap.WithListenerNet(b.ln))
	}
	return bootstrap.WithListener(ref, b.addr, authChain, opts...)
}

// runWebhookdemo is the hand-written runtime helper called by the generated
// main.go. It owns listener addresses and bootstrap wiring.
func runWebhookdemo(ctx context.Context, assemblyID string, assemblyCellIDs []string) error {
	addrs := defaultWebhookdemoAddrs()
	app, err := buildWebhookdemoBootstrap(assemblyID, assemblyCellIDs, addrs)
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "webhookdemo: starting; POST HMAC-signed webhooks to the webhook listener",
		slog.String("webhook_addr", addrs.webhook.addr),
		slog.String("health_addr", addrs.health.addr))
	return app.Run(ctx)
}

// buildWebhookdemoBootstrap assembles the webhookdemo bootstrap from in-memory
// demo dependencies and the given listener bindings, returning the configured
// *bootstrap.Bootstrap without starting it. Splitting assembly from Run lets the
// e2e (webhook_e2e_test.go) boot the real wiring on an injected loopback
// listener and exercise the signed→200 / forged→401 path over real HTTP.
func buildWebhookdemoBootstrap(assemblyID string, assemblyCellIDs []string, addrs webhookdemoAddrs) (*bootstrap.Bootstrap, error) {
	// The redacting slog default is sealed by the generated main.go's run()
	// before this runs, so logger (and every slog.Default() call) is scrubbed.
	logger := slog.Default()

	// Drift check: assembly.yaml cells (assemblyCellIDs) ↔ modules_gen.go.
	if _, err := runWebhookdemoModules(assemblyID, assemblyCellIDs); err != nil {
		return nil, err
	}

	// Seed the webhook source store with the demo sender's shared HMAC secret.
	// Both the inbound receiver (verify) and any future dispatcher (sign) resolve
	// secrets from this store by source ID.
	sourceID, err := kwh.NewSourceID(demoSourceID)
	if err != nil {
		return nil, fmt.Errorf("webhookdemo: webhook source id: %w", err)
	}
	src, err := kwh.NewSource(sourceID, []byte(demoWebhookSecret))
	if err != nil {
		return nil, fmt.Errorf("webhookdemo: webhook source: %w", err)
	}
	sourceStore := kwh.NewSourceRegistry()
	if err := sourceStore.Register(src); err != nil {
		return nil, fmt.Errorf("webhookdemo: register webhook source: %w", err)
	}

	// In-memory idempotency claimer (single-process demo; production = Redis/PG).
	claimer := idempotency.NewInMemClaimer(clock.Real())

	// Construct the hooks cell and register it on the assembly. The cell holds no
	// outbox/txManager; inbound webhook receive is L0 because the slice only
	// validates, decodes, and logs an already verified delivery.
	hc := hooks.NewHooksCell(hooks.WithLogger(logger))
	asm := assembly.New(clock.Real(), assembly.Config{ID: assemblyID, DurabilityMode: outbox.DurabilityDemo})
	if err := asm.Register(hc); err != nil {
		return nil, fmt.Errorf("webhookdemo: register hooks cell: %w", err)
	}

	opts := []bootstrap.Option{
		bootstrap.WithAssembly(asm),
		// Webhook receiver listener — AuthNone because the HMAC signature is the
		// application-layer auth (a JWT chain would 401 before the verifier runs).
		listenerOption(cell.WebhookListener, addrs.webhook, []kauth.ListenerAuth{kauth.AuthNone{}}),
		// Mandatory health listener (#673): /healthz /readyz /metrics on loopback.
		listenerOption(cell.HealthListener, addrs.health, []kauth.ListenerAuth{kauth.AuthNone{}}),
		bootstrap.WithHealthRoutes(bootstrap.WithReadyzVerboseDisabled()),
		// Webhook receiver dependencies (drained in phase5): the source store
		// resolves the HMAC secret and the claimer deduplicates deliveries.
		bootstrap.WithWebhookSourceStore(sourceStore),
		bootstrap.WithWebhookClaimer(claimer),
	}
	return bootstrap.New(clock.Real(), opts...), nil
}

// runWebhookdemoModules validates that assembly.yaml cells (assemblyCellIDs)
// match the generated module list in modules_gen.go. A mismatch means
// `gocell generate assembly --id=webhookdemo` has not been re-run after an
// assembly.yaml change.
func runWebhookdemoModules(assemblyID string, cellIDs []string) ([]CellModule, error) {
	mods := generatedCellModules()
	if err := assertModuleIDsMatch(assemblyID, cellIDs, mods); err != nil {
		return nil, err
	}
	return mods, nil
}

// assertModuleIDsMatch fails-fast when assembly.yaml.cells (cellIDs) drifts from
// the generated module list. The two should be 1:1 in declaration order; any
// mismatch indicates a missing `gocell generate assembly` run.
func assertModuleIDsMatch(assemblyID string, cellIDs []string, mods []CellModule) error {
	hint := fmt.Sprintf("run `gocell generate assembly --id=%s`", assemblyID)
	if len(cellIDs) != len(mods) {
		return fmt.Errorf(
			"%s: assembly.yaml cells (%d) ↔ modules_gen.go (%d) length mismatch; %s",
			assemblyID, len(cellIDs), len(mods), hint)
	}
	for i, want := range cellIDs {
		if got := mods[i].ID(); got != want {
			return fmt.Errorf(
				"%s: assembly.yaml cells[%d]=%q ↔ modules_gen.go=%q drift; %s",
				assemblyID, i, want, got, hint)
		}
	}
	return nil
}

// CellModule is the K#10 modules_gen.go interface contract: each generated
// factory returns a value implementing ID(). The webhookdemo assembly uses stub
// module values only for drift detection; cell construction is done directly in
// buildWebhookdemoBootstrap (this example does not use the compositionAPI Provide
// pattern — see the plan's assembly-form decision).
type CellModule interface {
	ID() string
}

// HooksCellModule is the stub module for the hooks cell — returned by
// generatedCellModules() in modules_gen.go. It carries the cell ID for the
// assembly drift check and does not participate in Provide wiring.
type HooksCellModule struct{}

// ID returns the hooks cell identifier.
func (HooksCellModule) ID() string { return "hooks" }
