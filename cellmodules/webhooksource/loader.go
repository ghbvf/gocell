// Package webhooksource is the composition-root loader for the persistent,
// encrypted webhook source secret store (#1540). It reads every webhook source
// row from postgres at boot, decrypts each secret through the sealed kernel
// funnel, and returns a kwh.SourceRegistry snapshot ready to inject via
// bootstrap.WithWebhookSourceStore — so the runtime Lookup hot path stays a pure
// in-memory read, immutable until the next restart (matching the
// eager-register-at-startup contract of the in-memory default).
//
// This is a composition-root-layer package: it may import adapters/, cellmodules/
// cellsecrets, and runtime/. It must NOT be imported by cells/, runtime/, or
// adapters/. It is NOT a composition.CellModule (webhook is not a cell); it is a
// loader the assembly's composition root calls when it wires webhook receivers /
// dispatchers against a persistent store.
//
// ref: cellmodules/configcore (sibling composition module; key-provider pattern)
package webhooksource

import (
	"context"
	"fmt"
	"log/slog"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cellmodules/cellsecrets"
	"github.com/ghbvf/gocell/kernel/clock"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	kwh "github.com/ghbvf/gocell/kernel/webhook"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/composition"
	"github.com/ghbvf/gocell/runtime/crypto"
)

// LoadSourceStore builds the persistent webhook [kwh.SourceStore] from postgres.
// It builds the value transformer from env, opens the shared PG pool, loads and
// decrypts every persisted source, and returns a populated [kwh.SourceRegistry]
// snapshot.
//
// Fail-closed: the persistent store requires postgres storage AND a configured
// key provider (GOCELL_WEBHOOK_KEY_PROVIDER) — webhook secrets are never persisted
// in plaintext. Dev / demo single-process deployments that do not persist secrets
// build an in-memory [kwh.NewSourceRegistry] directly instead of calling this.
func LoadSourceStore(ctx context.Context, shared *composition.SharedDeps) (kwh.SourceStore, error) {
	vt, err := buildValueTransformer(
		shared.Topology.StorageBackend(), shared.Topology.AdapterMode(),
		shared.Clock, shared.MetricsProvider)
	if err != nil {
		return nil, err
	}
	pool, err := cellsecrets.PgxPoolFromProvider(shared.PG)
	if err != nil {
		return nil, fmt.Errorf("webhooksource: resolve pg pool: %w", err)
	}
	repo, err := adapterpg.NewWebhookSourceRepository(pool, vt)
	if err != nil {
		return nil, err
	}
	sources, err := repo.LoadAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("webhooksource: load sources: %w", err)
	}
	registry := kwh.NewSourceRegistry()
	for _, src := range sources {
		if err := registry.Register(src); err != nil {
			return nil, fmt.Errorf("webhooksource: register source %q: %w", src.ID(), err)
		}
	}
	slog.Info("webhooksource: loaded persistent webhook sources", slog.Int("count", len(sources)))
	return registry, nil
}

// buildValueTransformer gates persistence on postgres storage and builds the
// envelope-encryption transformer from the GOCELL_WEBHOOK_* key-provider env. It
// is split from [LoadSourceStore] so the storage gate and key-provider wiring are
// unit-testable from primitives, without constructing a full SharedDeps / pool.
func buildValueTransformer(
	storageBackend, adapterMode string, clk clock.Clock, metricsProvider metrics.Provider,
) (kcrypto.ValueTransformer, error) {
	if storageBackend != "postgres" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"webhooksource: persistent webhook source store requires postgres storage "+
				"(memory/demo deployments use the in-memory kwh.SourceRegistry directly)")
	}
	providerName, masterKey, prevMasterKey := cellsecrets.LoadWebhookSourceKeyProvider()
	kp, err := buildKeyProviderFromName(adapterMode, providerName, masterKey, prevMasterKey, clk, metricsProvider)
	if err != nil {
		return nil, fmt.Errorf("webhooksource: key provider: %w", err)
	}
	return crypto.NewValueTransformer(kp), nil
}
