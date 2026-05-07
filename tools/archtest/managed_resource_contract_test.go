// INVARIANT: MANAGED-RESOURCE-CONTRACT-01: adapter exported types with owned lifecycle must implement ManagedResource or be opted-out
package archtest

import (
	"go/ast"
	"go/constant"
	"go/types"
	"regexp"
	"sort"
	"strconv"
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
	"adapters/redis.Client":                    "close-only-resource: health/close are wired explicitly; no worker/checker bundle yet",
	"adapters/redis.Config":                    "config: construction input value",
	"adapters/redis.IdempotencyClaimer":        "subresource-not-owner: uses caller-owned Redis Client",
	"adapters/redis.KeyNamespace":              "value-object: typed string for cell-scoped Redis key prefix",
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
	_, allPkgs := loadModule(t, root)
	pkgs := filterPkgsByPathPrefix(allPkgs,
		modulePath+"/kernel/lifecycle",
		modulePath+"/adapters/")
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

func TestAdapterManagedResourceCheckerNamesUseReadySuffix(t *testing.T) {
	root := findModuleRoot(t)
	modulePath := readModulePath(t, root)
	_, allPkgs := loadModule(t, root)
	pkgs := filterPkgsByPathPrefix(allPkgs,
		modulePath+"/adapters/",
		modulePath+"/cmd/corebundle")

	var violations []string
	for _, pkg := range pkgs {
		adapterPkg, ok := strings.CutPrefix(pkg.PkgPath, modulePath+"/adapters/")
		if !ok || strings.Contains(adapterPkg, "/") {
			if pkg.PkgPath == modulePath+"/cmd/corebundle" {
				violations = append(violations, healthCheckerCallNameViolations(pkg, "cmd/corebundle")...)
			}
			continue
		}
		violations = append(violations, adapterCheckerNameViolations(pkg, "adapters/"+adapterPkg)...)
	}

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
	_, allPkgs := loadModule(t, root)
	pkgs := filterPkgsByPathPrefix(allPkgs, modulePath+"/runtime/websocket")

	var violations []string
	for _, pkg := range pkgs {
		if pkg.PkgPath != modulePath+"/runtime/websocket" {
			continue
		}
		violations = append(violations, adapterCheckerNameViolations(pkg, "runtime/websocket")...)
	}

	sort.Strings(violations)
	assert.Empty(t, violations, "runtime/websocket ManagedResource probe names must be snake_case and end with _ready")
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

var adapterReadyProbeNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*_ready$`)

func adapterCheckerNameViolations(pkg *packages.Package, rel string) []string {
	var violations []string
	for _, file := range pkg.Syntax {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "Checkers" || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			recv := receiverTypeName(fn.Recv.List[0].Type)
			if recv == "" || !ast.IsExported(recv) {
				continue
			}
			for _, name := range checkerNamesFromFunc(pkg.TypesInfo, fn) {
				if !adapterReadyProbeNamePattern.MatchString(name) {
					violations = append(violations, rel+"."+recv+" Checkers probe "+strconv.Quote(name)+" must be snake_case and end with _ready")
				}
			}
		}
	}
	return violations
}

func checkerNamesFromFunc(info *types.Info, fn *ast.FuncDecl) []string {
	var names []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		tv, ok := info.Types[kv.Key]
		if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
			return true
		}
		names = append(names, constant.StringVal(tv.Value))
		return true
	})
	return names
}

func healthCheckerCallNameViolations(pkg *packages.Package, rel string) []string {
	var violations []string
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || selectorName(call.Fun) != "WithHealthChecker" || len(call.Args) == 0 {
				return true
			}
			name, ok := constStringValue(pkg.TypesInfo, call.Args[0])
			if !ok {
				return true
			}
			if !adapterReadyProbeNamePattern.MatchString(name) {
				violations = append(violations, rel+" bootstrap.WithHealthChecker probe "+strconv.Quote(name)+" must be snake_case and end with _ready")
			}
			return true
		})
	}
	return violations
}

func constStringValue(info *types.Info, expr ast.Expr) (string, bool) {
	tv, ok := info.Types[expr]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(tv.Value), true
}
