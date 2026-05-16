// INVARIANT: MANAGED-RESOURCE-COMPLETENESS-01: adapter exported types
//
//	with owned lifecycle must implement ManagedResource or be opted-out.
//	The 4 external-dependency owner types (postgres.Pool / redis.Client /
//	s3.Client / oidc.Adapter) are deliberately excluded from the opt-out
//	table and MUST implement ManagedResource directly.
package archtest

import (
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type adapterExportedType struct {
	ID   string
	Name string
	Type types.Type
}

var adapterManagedResourceOptOut = map[string]string{
	"adapters/circuitbreaker.Adapter":          "stateless-adapter: no owned external connection or background worker",
	"adapters/circuitbreaker.Config":           "config: construction input value",
	"adapters/circuitbreaker.Counts":           "value-object: breaker statistics snapshot",
	"adapters/circuitbreaker.State":            "value-object: breaker enum",
	"adapters/oidc.Config":                     "config: construction input value",
	"adapters/oidc.NoopRefreshCollector":       "stateless-collector: no-op observability sink, no owned lifecycle",
	"adapters/oidc.RefreshCollector":           "interface: observability collector contract, not a resource",
	"adapters/otel.MessagingChannelStatter":    "value-object: statter config for external metric collection",
	"adapters/otel.MetricProvider":             "stateless-adapter: emits metrics through caller-owned SDK/provider",
	"adapters/otel.Tracer":                     "stateless-adapter: tracer facade, provider lifecycle is caller-owned",
	"adapters/otel.TracerConfig":               "config: construction input value",
	"adapters/postgres.Config":                 "config: construction input value",
	"adapters/postgres.DestructiveDownPermit":  "value-object: explicit migration rollback permit",
	"adapters/postgres.InvalidIndex":           "value-object: schema validation diagnostic",
	"adapters/postgres.LedgerStore":            "subresource-not-owner: storage facade over caller-owned pool; no independent lifecycle",
	"adapters/postgres.MigrationDirection":     "value-object: migration enum",
	"adapters/postgres.MigrationStatus":        "value-object: migration diagnostic snapshot",
	"adapters/postgres.Migrator":               "subresource-not-owner: uses caller-owned pool, no independent lifecycle",
	"adapters/postgres.OutboxWriter":           "stateless-adapter: writes through ctx-bound transaction",
	"adapters/postgres.PGOutboxStore":          "subresource-not-owner: storage facade over caller-owned pool",
	"adapters/postgres.PGRefreshStore":         "subresource-not-owner: storage facade over caller-owned pool",
	"adapters/postgres.PoolStats":              "value-object: pool diagnostic snapshot",
	"adapters/postgres.RowScanner":             "interface: query row abstraction, not a resource",
	"adapters/postgres.TxManager":              "subresource-not-owner: transaction facade over caller-owned pool",
	"adapters/prometheus.HookObserver":         "stateless-adapter: observes lifecycle events through caller-owned registry",
	"adapters/prometheus.HookObserverConfig":   "config: construction input value",
	"adapters/prometheus.MetricProvider":       "stateless-adapter: metrics facade over caller-owned registry",
	"adapters/prometheus.MetricProviderConfig": "config: construction input value",
	"adapters/rabbitmq.AMQPChannel":            "interface: amqp channel abstraction, not a top-level resource",
	"adapters/rabbitmq.AMQPConnection":         "interface: amqp connection abstraction, not exported owner type",
	"adapters/rabbitmq.Config":                 "config: construction input value",
	"adapters/rabbitmq.ConnectionOption":       "config: functional option",
	"adapters/rabbitmq.ConnectionPhase":        "value-object: lifecycle enum",
	"adapters/rabbitmq.ConnectionState":        "value-object: structured diagnostic snapshot",
	"adapters/rabbitmq.DialFunc":               "config: injected dial function",
	"adapters/rabbitmq.NoopPublisherCollector": "stateless-collector: no-op observability sink, no owned lifecycle",
	"adapters/rabbitmq.PoolStats":              "value-object: channel-pool diagnostic snapshot",
	"adapters/rabbitmq.PublishFailureReason":   "value-object: failure-reason enum",
	"adapters/rabbitmq.Publisher":              "subresource-not-owner: uses caller-owned Connection",
	"adapters/rabbitmq.PublisherCollector":     "interface: observability collector contract, not a resource",
	"adapters/rabbitmq.PublisherOption":        "config: functional option",
	"adapters/rabbitmq.Subscriber":             "subresource-not-owner: uses caller-owned Connection",
	"adapters/rabbitmq.SubscriberConfig":       "config: construction input value",
	"adapters/ratelimit.Config":                "config: construction input value",
	"adapters/ratelimit.Limiter":               "close-only-resource: in-memory limiter has no readiness or worker surface",
	"adapters/redis.Cache":                     "subresource-not-owner: uses caller-owned Redis Client",
	"adapters/redis.Config":                    "config: construction input value",
	"adapters/redis.IdempotencyClaimer":        "subresource-not-owner: uses caller-owned Redis Client",
	"adapters/redis.KeyNamespace":              "value-object: typed string for cell-scoped Redis key prefix",
	"adapters/redis.Mode":                      "value-object: Redis topology enum",
	"adapters/redis.NonceStore":                "subresource-not-owner: uses caller-owned Redis Client",
	"adapters/redis.PoolStats":                 "value-object: pool diagnostic snapshot",
	"adapters/redis.RedisDriver":               "subresource-not-owner: distlock driver over caller-owned Redis Client",
	"adapters/s3.Config":                       "config: construction input value",
	"adapters/vault.AuthMethod":                "interface: authentication strategy, not a resource",
	"adapters/vault.AuthResult":                "value-object: auth result",
	"adapters/vault.Method":                    "value-object: auth method enum",
	"adapters/vault.SecretIDProvider":          "interface: secret-id source, not a resource",
	"adapters/vault.TokenRenewer":              "interface: optional renewal capability, not a resource",
	"adapters/vault.VaultClient":               "interface: Vault API subset, not an owner type",
	"adapters/websocket.Conn":                  "subresource-not-owner: individual accepted connection lifecycle is handler-owned",
	"adapters/websocket.UpgradeConfig":         "config: construction input value",
}

func TestAdaptersExportedTypesManagedResourceOrOptOut(t *testing.T) {
	root := findModuleRoot(t)
	modulePath := readModulePath(t, root)
	adapterPrefix := modulePath + "/adapters/"
	lifecyclePkg := modulePath + "/kernel/lifecycle"

	var managedResource *types.Interface
	var adapterTypes []adapterExportedType

	RunTypedProduction(t, TypedOpts{Tests: false},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			pkgPath := p.Pkg.Path()

			// Capture kernel/lifecycle.ManagedResource interface.
			if pkgPath == lifecyclePkg {
				obj := p.Pkg.Scope().Lookup("ManagedResource")
				if obj != nil {
					named, ok := obj.Type().(*types.Named)
					if ok {
						iface, ok := named.Underlying().(*types.Interface)
						if ok {
							managedResource = iface.Complete()
						}
					}
				}
				return nil
			}

			// Collect exported adapter types (direct sub-packages of adapters/ only).
			adapterPkg, ok := strings.CutPrefix(pkgPath, adapterPrefix)
			if !ok || strings.Contains(adapterPkg, "/") {
				return nil
			}
			scope := p.Pkg.Scope()
			for _, name := range scope.Names() {
				obj, ok := scope.Lookup(name).(*types.TypeName)
				if !ok || !obj.Exported() {
					continue
				}
				adapterTypes = append(adapterTypes, adapterExportedType{
					ID:   "adapters/" + adapterPkg + "." + name,
					Name: name,
					Type: obj.Type(),
				})
			}
			return nil
		})

	require.NotNil(t, managedResource, "kernel/lifecycle.ManagedResource not found in production packages")
	sort.Slice(adapterTypes, func(i, j int) bool {
		return adapterTypes[i].ID < adapterTypes[j].ID
	})

	var violations []string
	for _, typ := range adapterTypes {
		if reason := adapterManagedResourceOptOut[typ.ID]; reason != "" {
			continue
		}
		if !implementsManagedResource(typ.Type, managedResource) {
			violations = append(violations, typ.ID+" must implement lifecycle.ManagedResource or be listed in adapterManagedResourceOptOut")
		}
	}

	sort.Strings(violations)
	assert.Empty(t, violations, "A54 ManagedResource contract violations")
}

func TestAdapterManagedResourceCheckerNamesUseReadySuffix(t *testing.T) {
	root := findModuleRoot(t)
	modulePath := readModulePath(t, root)
	adapterPrefix := modulePath + "/adapters/"
	coreBundlePkg := modulePath + "/cmd/corebundle"

	var violations []string
	RunTypedProduction(t, TypedOpts{Tests: false},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			pkgPath := p.Pkg.Path()

			adapterPkg, ok := strings.CutPrefix(pkgPath, adapterPrefix)
			if ok && !strings.Contains(adapterPkg, "/") {
				violations = append(violations,
					adapterCheckerNameViolationsFromPass(p.Fset, p.Files, p.TypesInfo, "adapters/"+adapterPkg)...)
				return nil
			}
			if pkgPath == coreBundlePkg {
				violations = append(violations,
					healthCheckerCallNameViolationsFromPass(p.Fset, p.Files, p.TypesInfo, "cmd/corebundle")...)
			}
			return nil
		})

	sort.Strings(violations)
	assert.Empty(t, violations, "adapter ManagedResource ready probes must use stable snake_case names ending in _ready")
}

// TestRuntimeWebsocketCheckerNamesUseReadySuffix enforces the
// observability rule (Readyz Probe Naming) for runtime/websocket.Hub:
// all Checkers() map keys must be snake_case and end with "_ready".
//
// This extends the adapter coverage from TestAdapterManagedResourceCheckerNamesUseReadySuffix
// to the runtime/websocket package, which owns a ManagedResource
// but lives in runtime/ rather than adapters/.
func TestRuntimeWebsocketCheckerNamesUseReadySuffix(t *testing.T) {
	root := findModuleRoot(t)
	modulePath := readModulePath(t, root)
	websocketPkg := modulePath + "/runtime/websocket"

	var violations []string
	RunTypedProduction(t, TypedOpts{Tests: false},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != websocketPkg {
				return nil
			}
			violations = append(violations,
				adapterCheckerNameViolationsFromPass(p.Fset, p.Files, p.TypesInfo, "runtime/websocket")...)
			return nil
		})

	sort.Strings(violations)
	assert.Empty(t, violations, "runtime/websocket ManagedResource probe names must be snake_case and end with _ready")
}

func implementsManagedResource(typ types.Type, managedResource *types.Interface) bool {
	if types.Implements(typ, managedResource) {
		return true
	}
	return types.Implements(types.NewPointer(typ), managedResource)
}
