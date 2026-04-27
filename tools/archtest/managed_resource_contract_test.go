package archtest

import (
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
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
	"adapters/oidc.Adapter":                    "stateless-adapter: wraps OIDC provider calls without owned lifecycle",
	"adapters/oidc.Config":                     "config: construction input value",
	"adapters/otel.MessagingChannelStatter":    "value-object: statter config for external metric collection",
	"adapters/otel.MetricProvider":             "stateless-adapter: emits metrics through caller-owned SDK/provider",
	"adapters/otel.Tracer":                     "stateless-adapter: tracer facade, provider lifecycle is caller-owned",
	"adapters/otel.TracerConfig":               "config: construction input value",
	"adapters/postgres.Config":                 "config: construction input value",
	"adapters/postgres.InvalidIndex":           "value-object: schema validation diagnostic",
	"adapters/postgres.MigrationDirection":     "value-object: migration enum",
	"adapters/postgres.MigrationStatus":        "value-object: migration diagnostic snapshot",
	"adapters/postgres.Migrator":               "subresource-not-owner: uses caller-owned pool, no independent lifecycle",
	"adapters/postgres.OutboxWriter":           "stateless-adapter: writes through ctx-bound transaction",
	"adapters/postgres.PGOutboxStore":          "subresource-not-owner: storage facade over caller-owned pool",
	"adapters/postgres.PGRefreshStore":         "subresource-not-owner: storage facade over caller-owned pool",
	"adapters/postgres.Pool":                   "subresource-not-owner: wrapped by PGResource for ManagedResource wiring",
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
	"adapters/rabbitmq.PoolStats":              "value-object: channel-pool diagnostic snapshot",
	"adapters/rabbitmq.Publisher":              "subresource-not-owner: uses caller-owned Connection",
	"adapters/rabbitmq.Subscriber":             "subresource-not-owner: uses caller-owned Connection",
	"adapters/rabbitmq.SubscriberConfig":       "config: construction input value",
	"adapters/ratelimit.Config":                "config: construction input value",
	"adapters/ratelimit.Limiter":               "close-only-resource: in-memory limiter has no readiness or worker surface",
	"adapters/redis.Cache":                     "subresource-not-owner: uses caller-owned Redis Client",
	"adapters/redis.Client":                    "close-only-resource: health/close are wired explicitly; no worker/checker bundle yet",
	"adapters/redis.Config":                    "config: construction input value",
	"adapters/redis.IdempotencyClaimer":        "subresource-not-owner: uses caller-owned Redis Client",
	"adapters/redis.Mode":                      "value-object: Redis topology enum",
	"adapters/redis.NonceStore":                "subresource-not-owner: uses caller-owned Redis Client",
	"adapters/redis.PoolStats":                 "value-object: pool diagnostic snapshot",
	"adapters/redis.RedisDriver":               "subresource-not-owner: distlock driver over caller-owned Redis Client",
	"adapters/s3.Client":                       "stateless-adapter: AWS SDK client has no GoCell readiness/worker contract",
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
	pkgs := loadTypedPackages(t, root, "./kernel/lifecycle", "./adapters/...")
	managedResource := managedResourceInterface(t, pkgs, modulePath)
	adapterTypes := collectAdapterExportedTypes(pkgs, modulePath)

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

func collectAdapterExportedTypes(pkgs []*packages.Package, modulePath string) []adapterExportedType {
	adapterPrefix := modulePath + "/adapters/"
	var out []adapterExportedType
	for _, pkg := range pkgs {
		adapterPkg, ok := strings.CutPrefix(pkg.PkgPath, adapterPrefix)
		if !ok || strings.Contains(adapterPkg, "/") {
			continue
		}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			obj, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || !obj.Exported() {
				continue
			}
			out = append(out, adapterExportedType{
				ID:   "adapters/" + adapterPkg + "." + name,
				Name: name,
				Type: obj.Type(),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

func managedResourceInterface(t *testing.T, pkgs []*packages.Package, modulePath string) *types.Interface {
	t.Helper()
	for _, pkg := range pkgs {
		if pkg.PkgPath != modulePath+"/kernel/lifecycle" {
			continue
		}
		obj := pkg.Types.Scope().Lookup("ManagedResource")
		require.NotNil(t, obj, "kernel/lifecycle.ManagedResource not found")
		named, ok := obj.Type().(*types.Named)
		require.True(t, ok, "ManagedResource type = %T, want *types.Named", obj.Type())
		iface, ok := named.Underlying().(*types.Interface)
		require.True(t, ok, "ManagedResource underlying type = %T, want *types.Interface", named.Underlying())
		return iface.Complete()
	}
	require.FailNow(t, "kernel/lifecycle package not loaded")
	return nil
}

func implementsManagedResource(typ types.Type, managedResource *types.Interface) bool {
	if types.Implements(typ, managedResource) {
		return true
	}
	return types.Implements(types.NewPointer(typ), managedResource)
}
