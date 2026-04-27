package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type adapterExportedType struct {
	ID      string
	Name    string
	Methods map[string]struct{}
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
	types := collectAdapterExportedTypes(t, filepath.Join(root, "adapters"))

	var violations []string
	for _, typ := range types {
		if reason := adapterManagedResourceOptOut[typ.ID]; reason != "" {
			continue
		}
		if !hasMethods(typ.Methods, "Checkers", "Worker", "Close") {
			violations = append(violations, typ.ID+" must implement lifecycle.ManagedResource or be listed in adapterManagedResourceOptOut")
		}
	}

	sort.Strings(violations)
	assert.Empty(t, violations, "A54 ManagedResource contract violations")
}

func collectAdapterExportedTypes(t *testing.T, adapterRoot string) []adapterExportedType {
	t.Helper()
	entries, err := os.ReadDir(adapterRoot)
	require.NoError(t, err)

	var out []adapterExportedType
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pkgDir := filepath.Join(adapterRoot, entry.Name())
		rel := filepath.ToSlash(filepath.Join("adapters", entry.Name()))
		types := map[string]*adapterExportedType{}

		files, err := os.ReadDir(pkgDir)
		require.NoError(t, err)
		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".go") || strings.HasSuffix(file.Name(), "_test.go") {
				continue
			}
			parseAdapterTypeFile(t, filepath.Join(pkgDir, file.Name()), rel, types)
		}
		for _, typ := range types {
			out = append(out, *typ)
		}
	}
	return out
}

func parseAdapterTypeFile(t *testing.T, path, rel string, types map[string]*adapterExportedType) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err)

	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ast.IsExported(ts.Name.Name) {
					continue
				}
				id := rel + "." + ts.Name.Name
				if types[id] == nil {
					types[id] = &adapterExportedType{ID: id, Name: ts.Name.Name, Methods: map[string]struct{}{}}
				}
			}
		case *ast.FuncDecl:
			if d.Recv == nil || len(d.Recv.List) == 0 || !d.Name.IsExported() {
				continue
			}
			recv := receiverTypeName(d.Recv.List[0].Type)
			if recv == "" || !ast.IsExported(recv) {
				continue
			}
			id := rel + "." + recv
			if types[id] == nil {
				types[id] = &adapterExportedType{ID: id, Name: recv, Methods: map[string]struct{}{}}
			}
			types[id].Methods[d.Name.Name] = struct{}{}
		}
	}
}

func hasMethods(methods map[string]struct{}, names ...string) bool {
	for _, name := range names {
		if _, ok := methods[name]; !ok {
			return false
		}
	}
	return true
}
