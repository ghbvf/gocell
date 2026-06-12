package metricschema

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
	"github.com/ghbvf/gocell/tools/packagesload"
)

func TestBuild_CorebundleCapturesReachableTypedMetrics(t *testing.T) {
	root := repoRoot(t)
	project, err := metadata.NewParser(root).Parse()
	require.NoError(t, err)

	schema, err := Build(t.Context(), root, project, "corebundle")
	require.NoError(t, err)

	hookTotal := requireMetric(t, schema, "cell_hook_total")
	assert.Equal(t, []string{"cell_id", "hook", "outcome"}, hookTotal.Labels)
	assert.Equal(t, "cmd/corebundle/metrics.go", hookTotal.File)

	hookDuration := requireMetric(t, schema, "cell_hook_duration_seconds")
	assert.Equal(t, []string{".005", ".01", ".025", ".05", ".1", ".25", ".5", "1", "2.5", "5", "10", "30"}, hookDuration.Buckets)

	authVerify := requireMetric(t, schema, "auth_token_verify_total")
	assert.Equal(t, []string{"result", "reason"}, authVerify.Labels)
	assert.Equal(t, "gocell_auth_token_verify_total", authVerify.FQName)
	assert.Equal(t, "gocell", authVerify.Namespace)
	assert.Equal(t, "runtime/auth/metrics.go", authVerify.File)

	outboxRelayed := requireMetric(t, schema, "outbox_relayed_total")
	assert.Equal(t, []string{"cell", "outcome"}, outboxRelayed.Labels)
	assert.Equal(t, "gocell_outbox_relayed_total", outboxRelayed.FQName)
	assert.Equal(t, "cellmodules/configcore/storage.go", outboxRelayed.File)

	configEventProcess := requireMetric(t, schema, "config_event_process_total")
	assert.Equal(t, []string{"cell", "slice", "reason"}, configEventProcess.Labels)
	assert.Equal(t, "gocell_config_event_process_total", configEventProcess.FQName)
	assert.Equal(t, "cmd/corebundle/shared_deps_build.go", configEventProcess.File)

	configEventSettlement := requireMetric(t, schema, "config_event_settlement_total")
	assert.Equal(t, []string{"cell", "slice", "disposition", "result"}, configEventSettlement.Labels)
	assert.Equal(t, "gocell_config_event_settlement_total", configEventSettlement.FQName)
	assert.Equal(t, "cmd/corebundle/shared_deps_build.go", configEventSettlement.File)

	httpDuration := requireMetric(t, schema, "http_request_duration_seconds")
	assert.Equal(t, []string{".005", ".01", ".025", ".05", ".1", ".25", ".5", "1", "2.5", "5", "10"}, httpDuration.Buckets)
	assert.Empty(t, httpDuration.BucketSource)
	assert.Equal(t, "runtime/bootstrap/phases_http.go", httpDuration.File)

	httpBodyLimitRejections := requireMetric(t, schema, "http_request_body_limit_rejections_total")
	assert.Equal(t, []string{"cell", "route"}, httpBodyLimitRejections.Labels)
	assert.Equal(t, "gocell_http_request_body_limit_rejections_total", httpBodyLimitRejections.FQName)
	assert.Equal(t, "counter", httpBodyLimitRejections.Type)

	// #885: vault metrics build through the kernel metrics.Provider (no Subsystem
	// field), so the schema name folds the former "vault" subsystem into the Name
	// (auth_login_total → vault_auth_login_total). The FQName is unchanged.
	//
	// This is NOT a wire-identity break: FQName is the Prometheus series identity
	// (the only thing scrapers, dashboards, and alerts key on) and it is stable.
	// The schema's name/subsystem decomposition is an internal derivation with no
	// downstream consumer beyond this golden — nothing generates dashboards/alerts
	// from the subsystem field. Re-introducing Subsystem on the kernel
	// CounterOpts/GaugeOpts to "preserve" the decomposition is deliberately
	// rejected: subsystem is a Prometheus naming convention, not a metrics-model
	// concept, and leaking it into the vendor-neutral kernel Provider (which also
	// backs the OTel adapter) would be an architectural regression. FQName carries
	// the identity; the namespace+name composition is the adapter's concern.
	vaultLogin := requireMetric(t, schema, "vault_auth_login_total")
	assert.Equal(t, "gocell_vault_auth_login_total", vaultLogin.FQName)
	assert.Equal(t, []string{"method", "result", "reason"}, vaultLogin.Labels)
	assert.Empty(t, vaultLogin.Subsystem,
		"#885: kernel-Provider metrics carry no subsystem; identity lives in the stable FQName")

	for _, m := range schema.Metrics {
		for _, label := range m.Labels {
			assert.NotContains(t, label, "<", "labels must not contain unresolved placeholders")
		}
		for _, bucket := range m.Buckets {
			assert.NotContains(t, bucket, "<", "buckets must not contain unresolved placeholders")
		}
	}
}

func TestBuild_CorebundleGeneratedSchemaIsCurrent(t *testing.T) {
	root := repoRoot(t)
	project, err := metadata.NewParser(root).Parse()
	require.NoError(t, err)
	schema, err := Build(t.Context(), root, project, "corebundle")
	require.NoError(t, err)
	got, err := Marshal(schema)
	require.NoError(t, err)
	want, err := os.ReadFile(filepath.Clean(filepath.Join(root, "assemblies", "corebundle", "generated", "metrics-schema.yaml")))
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got))
}

// TestPrometheusConstructor_RecognizesPromwrapFunnel is the codegen↔funnel
// drift guard (AI-robust Medium, archtest-bound). promwrap is the sole
// sanctioned production Prometheus constructor funnel; the schema scanner must
// recognize every promwrap.New* export or the metric it constructs silently
// becomes an unresolvable literal (the very ERR_METRICS_SCHEMA_UNRESOLVED that
// the promwrap rollout first triggered). Enumerating promwrap's exported funcs
// and asserting prometheusConstructorName recognizes each turns "added a
// promwrap constructor but forgot to teach codegen" into a CI failure at PR
// time instead of a deep `gocell verify generated` 500.
//
// Sibling of archtest TestMetricsFunnel_SymbolSentinel, which locks the same
// promwrap export set for the funnel-enforcement side.
func TestPrometheusConstructor_RecognizesPromwrapFunnel(t *testing.T) {
	root := repoRoot(t)
	// promwrap now lives in the adapters/prometheus go.work satellite module
	// (#1558), invisible to a GOWORK=off ModeModule load from the repo root;
	// ModeWorkspace resolves it as a workspace member.
	pkgs, err := loadPackagesWithMode(t.Context(), root, false, packagesload.ModeWorkspace, promwrapPkg)
	require.NoError(t, err)

	var exported []string
	for _, p := range pkgs {
		if p.PkgPath != promwrapPkg {
			continue
		}
		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			fn, ok := scope.Lookup(name).(*types.Func)
			if !ok || !fn.Exported() {
				continue
			}
			exported = append(exported, fn.Name())
			if _, _, recognized := prometheusConstructorName(fn.Name()); !recognized {
				t.Errorf("promwrap.%s is an exported constructor but prometheusConstructorName "+
					"does not recognize it; add a case so the schema scanner sees through the funnel",
					fn.Name())
			}
		}
	}
	require.NotEmpty(t, exported,
		"expected promwrap to export constructor functions; package path / loader regression?")
}

func TestMarshalOmitsLineNumbers(t *testing.T) {
	schema := &Schema{
		AssemblyID: "fixture",
		Scope:      "assembly-reachable",
		Entrypoint: "cmd/fixture/main.go",
		Metrics: []Entry{{
			Name:   "requests_total",
			Type:   "counter",
			Labels: []string{"status"},
			File:   "cmd/fixture/metrics.go",
			Line:   42,
		}},
	}

	got, err := Marshal(schema)
	require.NoError(t, err)
	assert.NotContains(t, string(got), "line:")

	schema.Metrics[0].Line = 99
	gotAfterLineOnlyChange, err := Marshal(schema)
	require.NoError(t, err)
	assert.Equal(t, string(got), string(gotAfterLineOnlyChange))
}

func TestBuild_FixtureLocksReachabilityIdentityAndLabels(t *testing.T) {
	root := writeMetricsFixture(t)
	project := fixtureProject()

	schema, err := Build(t.Context(), root, project, "fixture")
	require.NoError(t, err)
	assert.Equal(t, "assembly-reachable", schema.Scope)
	assert.Equal(t, "cmd/app/main.go", schema.Entrypoint)

	provider := requireMetric(t, schema, "provider_total")
	assert.Equal(t, "fixture_provider_total", provider.FQName)
	assert.Equal(t, "fixture", provider.Namespace)
	assert.Equal(t, []string{"first", "second"}, provider.Labels)

	direct := requireMetric(t, schema, "direct_total")
	assert.Equal(t, "custom_sub_direct_total", direct.FQName)
	assert.Equal(t, []string{"first", "second", "a_const", "z_const"}, direct.Labels)
	assert.Equal(t, []string{"a_const", "z_const"}, direct.ConstLabels)

	hist := requireMetric(t, schema, "hist_seconds")
	assert.Equal(t, []string{"route", "status"}, hist.Labels)
	assert.Equal(t, []string{"0.1", "1"}, hist.Buckets)

	names := metricNames(schema)
	assert.NotContains(t, names, "unreachable_total")
}

func TestBuild_FailsClosedOnUnresolvedMetricIdentity(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/bad.go", `package reachable

import (
	"os"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
)

var _ = metrics.CounterOpts{
	Name: os.Getenv("METRIC_NAME"),
}
`)
	project := fixtureProject()

	_, err := Build(t.Context(), root, project, "fixture")
	require.ErrorIs(t, err, ErrUnresolvedMetricSchema)
	assert.Contains(t, err.Error(), "metric name must be a compile-time string")
}

func TestBuild_FailsClosedOnUnresolvedPrometheusConstructorOpts(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/bad_prom.go", `package reachable

import prom "github.com/prometheus/client_golang/prometheus"

func buildCounterOpts() prom.CounterOpts {
	return prom.CounterOpts{Name: "dynamic_total"}
}

var _ = prom.NewCounterVec(buildCounterOpts(), []string{"status"})
`)
	project := fixtureProject()

	_, err := Build(t.Context(), root, project, "fixture")
	require.ErrorIs(t, err, ErrUnresolvedMetricSchema)
	assert.Contains(t, err.Error(), "Prometheus metric opts must be a resolvable literal")
}

func TestBuild_FailsClosedOnUnresolvedExplicitPrometheusNamespace(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/bad_namespace.go", `package reachable

import (
	"os"

	prom "github.com/prometheus/client_golang/prometheus"
)

var _ = prom.NewCounter(prom.CounterOpts{
	Namespace: os.Getenv("METRIC_NAMESPACE"),
	Name: "dynamic_namespace_total",
})
`)
	project := fixtureProject()

	_, err := Build(t.Context(), root, project, "fixture")
	require.ErrorIs(t, err, ErrUnresolvedMetricSchema)
	assert.Contains(t, err.Error(), "metric namespace must be a compile-time string")
}

func TestBuild_FailsClosedOnUnresolvedPrometheusHelperOpts(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/bad_helper.go", `package reachable

import prom "github.com/prometheus/client_golang/prometheus"

func buildCounterOpts() prom.CounterOpts {
	return prom.CounterOpts{Name: "helper_total"}
}

func registerCounter(opts prom.CounterOpts) prom.Counter {
	return prom.NewCounter(opts)
}

var _ = registerCounter(buildCounterOpts())
`)
	project := fixtureProject()

	_, err := Build(t.Context(), root, project, "fixture")
	require.ErrorIs(t, err, ErrUnresolvedMetricSchema)
	assert.Contains(t, err.Error(), "Prometheus metric helper opts must be a resolvable literal")
}

func TestBuild_FailsClosedOnUnresolvedCrossPackagePrometheusHelperOpts(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "helper/helper.go", `package helper

import prom "github.com/prometheus/client_golang/prometheus"

func RegisterCounter(opts prom.CounterOpts) prom.Counter {
	return prom.NewCounter(opts)
}
`)
	writeFile(t, root, "reachable/bad_cross_package_helper.go", `package reachable

import (
	"example.com/metricsfixture/helper"
	prom "github.com/prometheus/client_golang/prometheus"
)

func buildCounterOpts() prom.CounterOpts {
	return prom.CounterOpts{Name: "helper_total"}
}

var _ = helper.RegisterCounter(buildCounterOpts())
`)
	project := fixtureProject()

	_, err := Build(t.Context(), root, project, "fixture")
	require.ErrorIs(t, err, ErrUnresolvedMetricSchema)
	assert.Contains(t, err.Error(), "Prometheus metric helper opts must be a resolvable literal")
}

func TestBuild_FailsClosedOnUnresolvedPrometheusHelperVecLabels(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/bad_helper_labels.go", `package reachable

import prom "github.com/prometheus/client_golang/prometheus"

func registerCounterVec(opts prom.CounterOpts, labels []string) *prom.CounterVec {
	return prom.NewCounterVec(opts, labels)
}

func buildLabels() []string {
	return []string{"status"}
}

var _ = registerCounterVec(prom.CounterOpts{Name: "helper_vec_total"}, buildLabels())
`)
	project := fixtureProject()

	_, err := Build(t.Context(), root, project, "fixture")
	require.ErrorIs(t, err, ErrUnresolvedMetricSchema)
	assert.Contains(t, err.Error(), "label names must be a resolvable string slice")
}

func TestBuild_EmptyKnownWrapperBucketsUseDefaults(t *testing.T) {
	cfgExprs := []string{
		`metrics.ProviderCollectorConfig{DurationBuckets: []float64{}}`,
		`metrics.ProviderCollectorConfig{DurationBuckets: nil}`,
	}
	defaultExpr, err := parser.ParseExpr(`[]float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}`)
	require.NoError(t, err)
	pkg := types.NewPackage(runtimeMetricsPkg, "metrics")
	defaultObj := types.NewVar(token.NoPos, pkg, "DefaultDurationBuckets", nil)
	sp := &scanPackage{
		fset:  token.NewFileSet(),
		inits: map[types.Object]ast.Expr{defaultObj: defaultExpr},
	}

	for _, cfg := range cfgExprs {
		cfgExpr, err := parser.ParseExpr(cfg)
		require.NoError(t, err)
		buckets, err := sp.configBuckets(
			cfgExpr.(*ast.CompositeLit), "DurationBuckets",
			runtimeMetricsPkg, "DefaultDurationBuckets", "fixture.go",
		)
		require.NoError(t, err)
		assert.Equal(t, []string{".005", ".01", ".025", ".05", ".1", ".25", ".5", "1", "2.5", "5", "10"}, buckets)
	}
}

func TestCheckOBS01DetectsDirectLocalAndHelperParamClassifiers(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"errors"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	ec "github.com/ghbvf/gocell/pkg/errcode"
)

func direct(v metrics.CounterVec, err error) {
	v.With(metrics.Labels{"reason": fmt.Sprint(ec.IsInfraError(err))}).Inc(context.Background())
}

func local(v metrics.CounterVec, err error) {
	reason := fmt.Sprint(ec.IsInfraError(err))
	v.With(metrics.Labels{"reason": reason}).Inc(context.Background())
}

func helper(v metrics.CounterVec, reason string) {
	v.With(metrics.Labels{"reason": reason}).Inc(context.Background())
}

func viaHelper(v metrics.CounterVec, err error) {
	helper(v, fmt.Sprint(ec.IsInfraError(err)))
}

func negative(v metrics.CounterVec, err error) {
	if errors.Is(err, errors.ErrUnsupported) {
		v.With(metrics.Labels{"reason": "unsupported"}).Inc(context.Background())
	}
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 3)
	for _, diag := range diagnostics {
		assert.Equal(t, "v", diag.Metric)
		assert.Equal(t, "reason", diag.Label)
	}
	assert.Contains(t, diagnostics[0].Message, "metric/label identity is not machine-resolvable")
}

func TestCheckOBS01DetectsHelperReturnClassifiers(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func classify(err error) string {
	return fmt.Sprint(errcode.IsInfraError(err))
}

func direct(err error) {
	counter.With(metrics.Labels{"reason": classify(err)}).Inc(context.Background())
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
	assert.Contains(t, diagnostics[0].Message, "metrics-migration-acks.yaml")
}

func TestCheckOBS01DetectsNamedAndMultiReturnHelperClassifiers(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func named(err error) (reason string) {
	reason = fmt.Sprint(errcode.IsInfraError(err))
	return
}

func multi(err error) (bool, string) {
	return false, fmt.Sprint(errcode.IsInfraError(err))
}

func useNamed(err error) {
	counter.With(metrics.Labels{"reason": named(err)}).Inc(context.Background())
}

func useMulti(err error) {
	_, reason := multi(err)
	counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
}

func useVarMulti(err error) {
	var _, reason = multi(err)
	counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
}

func useMultiFlag(err error) {
	flag, _ := multi(err)
	counter.With(metrics.Labels{"reason": fmt.Sprint(flag)}).Inc(context.Background())
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 3)
}

func TestCheckOBS01DetectsExpandedMultiReturnHelperArgsByPosition(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func multi(err error) (bool, string) {
	return false, fmt.Sprint(errcode.IsInfraError(err))
}

func helperReason(_ bool, reason string) {
	counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
}

func helperFlag(flag bool, _ string) {
	counter.With(metrics.Labels{"reason": fmt.Sprint(flag)}).Inc(context.Background())
}

func useReason(err error) {
	helperReason(multi(err))
}

func useFlag(err error) {
	helperFlag(multi(err))
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
}

func TestCheckOBS01DetectsBranchTaintBeforeSink(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func branched(err error, safe bool) {
	reason := "ok"
	if safe {
		reason = "ok"
	} else {
		reason = fmt.Sprint(errcode.IsInfraError(err))
	}
	counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
}

func TestCheckOBS01DetectsCommaOKTupleExpressionTaint(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func commaOK(err error) {
	_, ok := map[string]string{}[fmt.Sprint(errcode.IsInfraError(err))]
	counter.With(metrics.Labels{"reason": fmt.Sprint(ok)}).Inc(context.Background())
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
}

func TestCheckOBS01DoesNotMergeTerminatedBranchTaintIntoLaterSink(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func terminated(err error, unsafe bool) {
	reason := "ok"
	if unsafe {
		reason = fmt.Sprint(errcode.IsInfraError(err))
		return
	}
	counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	assert.Empty(t, diagnostics)
}

func TestCheckOBS01DetectsSwitchFallthroughTaint(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func fallthroughTaint(err error, code int) {
	reason := "ok"
	switch code {
	case 1:
		reason = fmt.Sprint(errcode.IsInfraError(err))
		fallthrough
	case 2:
		counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
	}
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
}

func TestCheckOBS01DetectsRangeValueTaint(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func rangeValue(err error) {
	for _, reason := range []string{fmt.Sprint(errcode.IsInfraError(err))} {
		counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
	}
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
}

func TestCheckOBS01DetectsFuncLiteralLocalTaint(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func literal(err error) {
	func() {
		reason := fmt.Sprint(errcode.IsInfraError(err))
		counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
	}()
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
}

func TestCheckOBS01DetectsGlobalTransitiveHelperSinkParams(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "shared/obs.go", `package shared

import (
	"context"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func Record(reason string) {
	counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
}

func Forward(reason string) {
	Record(reason)
}
`)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"fmt"

	"example.com/metricsfixture/shared"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func viaShared(err error) {
	shared.Forward(fmt.Sprint(errcode.IsInfraError(err)))
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable", "./shared")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
}

func TestCheckOBS01ProductionScopeCoversPkgHelpers(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "pkg/obsreason/reason.go", `package obsreason

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
)

func Reason(err error) string {
	return fmt.Sprint(errcode.IsInfraError(err))
}
`)
	writeFile(t, root, "cmd/app/obs.go", `package main

import (
	"context"

	"example.com/metricsfixture/pkg/obsreason"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
)

var obsProvider = metrics.NopProvider{}
var obsCounter, _ = obsProvider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func viaPkgHelper(err error) {
	obsCounter.With(metrics.Labels{"reason": obsreason.Reason(err)}).Inc(context.Background())
}
`)

	diagnostics, err := CheckOBS01(t.Context(), root)
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "fixture_obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
}

func TestOBS01ProductionPatternsCoverProjectPackages(t *testing.T) {
	root := repoRoot(t)
	covered := prodscan.PatternTopLevels(obs01ProductionPatterns(root))
	var missing []string
	for top := range productionGoTopLevels(t, root) {
		if !covered[top] {
			missing = append(missing, top)
		}
	}
	slices.Sort(missing)
	assert.Empty(t, missing, "new production Go top-level directories must be added to OBS-01 production scan SoR")
}

func TestCheckOBS01DetectsIIFEParamTaint(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func iife(err error) {
	func(reason string) {
		counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
	}(fmt.Sprint(errcode.IsInfraError(err)))
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
}

func TestCheckOBS01DoesNotScanUncalledFuncLiteral(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func uncalled(err error) {
	reason := fmt.Sprint(errcode.IsInfraError(err))
	_ = func() {
		counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
	}
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	assert.Empty(t, diagnostics)
}

func TestCheckOBS01DetectsCalledFuncLiteralVariable(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func called(err error) {
	reason := fmt.Sprint(errcode.IsInfraError(err))
	f := func() {
		counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
	}
	f()
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
}

func TestCheckOBS01DetectsCompoundAssignmentPreservesTaint(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func compound(err error) {
	reason := fmt.Sprint(errcode.IsInfraError(err))
	reason += ""
	counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
}

func TestCheckOBS01DoesNotTaintRangeIndexFromSliceValue(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func rangeIndex(err error) {
	for i := range []string{fmt.Sprint(errcode.IsInfraError(err))} {
		counter.With(metrics.Labels{"reason": fmt.Sprint(i)}).Inc(context.Background())
	}
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	assert.Empty(t, diagnostics)
}

func TestCheckOBS01DetectsMapLabelMutation(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func mapMutation(err error) {
	labels := metrics.Labels{}
	labels["reason"] = fmt.Sprint(errcode.IsInfraError(err))
	counter.With(labels).Inc(context.Background())
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "<labels>", diagnostics[0].Label)
}

func TestCheckOBS01DetectsVariadicWithLabelValues(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
	prom "github.com/prometheus/client_golang/prometheus"
)

var counter = prom.NewCounterVec(prom.CounterOpts{
	Name: "obs_total",
}, []string{"status", "reason"})

func direct(err error) {
	counter.WithLabelValues([]string{"ok", fmt.Sprint(errcode.IsInfraError(err))}...).Inc()
}

func record(vals ...string) {
	counter.WithLabelValues(vals...).Inc()
}

func viaWrapper(err error) {
	record("ok", fmt.Sprint(errcode.IsInfraError(err)))
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 2)
	for _, diag := range diagnostics {
		assert.Equal(t, "obs_total", diag.Metric)
		assert.Equal(t, "reason", diag.Label)
	}
}

func TestCheckOBS01KeyedSpreadSliceUsesElementIndex(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
	prom "github.com/prometheus/client_golang/prometheus"
)

var counter = prom.NewCounterVec(prom.CounterOpts{
	Name: "obs_total",
}, []string{"status", "reason"})

func direct(err error) {
	counter.WithLabelValues([]string{1: fmt.Sprint(errcode.IsInfraError(err))}...).Inc()
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
}

func TestCheckOBS01DetectsBreakTaintAfterSwitch(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func breakTaint(err error, code int) {
	reason := "ok"
	switch code {
	case 1:
		reason = fmt.Sprint(errcode.IsInfraError(err))
		break
	}
	counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
}

func TestCheckOBS01DoesNotCollectSinkParamsFromUncalledLiteral(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func wrapper(reason string) {
	_ = func() {
		counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
	}
}

func direct(err error) {
	wrapper(fmt.Sprint(errcode.IsInfraError(err)))
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	assert.Empty(t, diagnostics)
}

func TestCheckOBS01DetectsIIFEReturnTaint(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func helper(err error) (reason string) {
	func() {
		reason = fmt.Sprint(errcode.IsInfraError(err))
	}()
	return
}

func direct(err error) {
	counter.With(metrics.Labels{"reason": helper(err)}).Inc(context.Background())
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
}

func TestCheckOBS01SpreadSliceVariableUsesGenericLabel(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
	prom "github.com/prometheus/client_golang/prometheus"
)

var counter = prom.NewCounterVec(prom.CounterOpts{
	Name: "obs_total",
}, []string{"status", "reason"})

func direct(err error) {
	vals := []string{"ok", fmt.Sprint(errcode.IsInfraError(err))}
	counter.WithLabelValues(vals...).Inc()
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "<labelValues>", diagnostics[0].Label)
}

func TestCheckOBS01DetectsAssignedMapKeyRangeTaint(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func rangeMapKey(err error) {
	values := map[string]string{fmt.Sprint(errcode.IsInfraError(err)): "x"}
	for reason := range values {
		counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
	}
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
}

func TestCheckOBS01BranchClosureBindingsMerge(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func branchClosure(err error, safe bool) {
	reason := fmt.Sprint(errcode.IsInfraError(err))
	f := func() {}
	if safe {
		f = func() {
			counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
		}
	} else {
		f = func() {}
	}
	f()
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
}

func TestCheckOBS01ClosureCallCanClearCapturedTaint(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func clearInClosure(err error) {
	reason := fmt.Sprint(errcode.IsInfraError(err))
	f := func() {
		reason = "ok"
	}
	f()
	counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	assert.Empty(t, diagnostics)
}

func TestCheckOBS01DetectsVariadicIIFEArgs(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
	prom "github.com/prometheus/client_golang/prometheus"
)

var counter = prom.NewCounterVec(prom.CounterOpts{
	Name: "obs_total",
}, []string{"status", "reason"})

func iife(err error) {
	func(vals ...string) {
		counter.WithLabelValues(vals...).Inc()
	}("ok", fmt.Sprint(errcode.IsInfraError(err)))
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
}

func TestCheckOBS01SpreadWrapperUsesGenericLabelOnce(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
	prom "github.com/prometheus/client_golang/prometheus"
)

var counter = prom.NewCounterVec(prom.CounterOpts{
	Name: "obs_total",
}, []string{"status", "reason"})

func record(vals ...string) {
	counter.WithLabelValues(vals...).Inc()
}

func viaSpread(err error) {
	vals := []string{"ok", fmt.Sprint(errcode.IsInfraError(err))}
	record(vals...)
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "obs_total", diagnostics[0].Metric)
	assert.Equal(t, "<labelValues>", diagnostics[0].Label)
}

func TestCheckOBS01DoesNotTaintAssignedMapKeyFromValue(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func rangeMapValue(err error) {
	values := map[string]string{"safe": fmt.Sprint(errcode.IsInfraError(err))}
	for reason := range values {
		counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
	}
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	assert.Empty(t, diagnostics)
}

func TestCheckOBS01DoesNotScanFunctionLiteralExpressionBody(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func literalValue(err error) {
	f := func() string {
		return fmt.Sprint(errcode.IsInfraError(err))
	}
	counter.With(metrics.Labels{"reason": fmt.Sprint(f)}).Inc(context.Background())
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	assert.Empty(t, diagnostics)
}

func TestCheckOBS01DetectsControlFlowAndMutationEdges(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	prom "github.com/prometheus/client_golang/prometheus"
)

var provider = metrics.NopProvider{}
var loopCounter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "loop_obs_total",
	LabelNames: []string{"reason"},
})
var closureCounter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "closure_obs_total",
	LabelNames: []string{"reason"},
})
var switchCounter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "switch_obs_total",
	LabelNames: []string{"reason"},
})
var continueCounter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "continue_obs_total",
	LabelNames: []string{"reason"},
})
var mapCounter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "map_key_obs_total",
	LabelNames: []string{"reason"},
})
var spreadCounter = prom.NewCounterVec(prom.CounterOpts{
	Name: "spread_obs_total",
}, []string{"status", "reason"})

func record(vals ...string) {
	spreadCounter.WithLabelValues(vals...).Inc()
}

func loopCarried(err error) {
	reason := "ok"
	for i := 0; i < 2; i++ {
		loopCounter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
		reason = fmt.Sprint(errcode.IsInfraError(err))
	}
}

func closureAlternatives(err error, clear bool) {
	reason := fmt.Sprint(errcode.IsInfraError(err))
	f := func() {}
	if clear {
		f = func() {
			reason = "ok"
		}
	} else {
		f = func() {}
	}
	f()
	closureCounter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
}

func switchClosure(err error, mode int) {
	reason := fmt.Sprint(errcode.IsInfraError(err))
	f := func() {
		switchCounter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
	}
	switch mode {
	case 1:
		f = func() {}
	}
	f()
}

func continuePost(err error) {
	reason := "ok"
	i := 0
	for ; i < 2; reason = fmt.Sprint(errcode.IsInfraError(err)) {
		continueCounter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
		i++
		continue
	}
}

func viaCompositeSpread(err error) {
	record([]string{"ok", fmt.Sprint(errcode.IsInfraError(err))}...)
}

func mapKeyMutation(err error) {
	values := map[string]string{}
	values[fmt.Sprint(errcode.IsInfraError(err))] = "x"
	for reason := range values {
		mapCounter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
	}
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 6)
	got := map[string]int{}
	for _, diagnostic := range diagnostics {
		assert.Equal(t, "reason", diagnostic.Label)
		got[diagnostic.Metric]++
	}
	assert.Equal(t, map[string]int{
		"closure_obs_total":  1,
		"continue_obs_total": 1,
		"loop_obs_total":     1,
		"map_key_obs_total":  1,
		"spread_obs_total":   1,
		"switch_obs_total":   1,
	}, got)
}

func TestCheckOBS01DoesNotReportClearedTaintOrCategoryConstants(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func cleared(err error) {
	reason := fmt.Sprint(errcode.IsInfraError(err))
	reason = "ok"
	counter.With(metrics.Labels{"reason": reason}).Inc(context.Background())
}

func constantCategory() {
	counter.With(metrics.Labels{"reason": fmt.Sprint(errcode.CategoryDomain)}).Inc(context.Background())
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	assert.Empty(t, diagnostics)
}

func TestCheckOBS01ResolvesPrometheusWithLabelValuesLabelNames(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
	prom "github.com/prometheus/client_golang/prometheus"
)

var counter = prom.NewCounterVec(prom.CounterOpts{
	Name: "prom_obs_total",
}, []string{"reason"})

func direct(err error) {
	counter.WithLabelValues(fmt.Sprint(errcode.IsInfraError(err))).Inc()
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	assert.Equal(t, "prom_obs_total", diagnostics[0].Metric)
	assert.Equal(t, "reason", diagnostics[0].Label)
}

func TestCheckOBS01RequiresStrictAckFields(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func direct(v metrics.CounterVec, err error) {
	v.With(metrics.Labels{"reason": fmt.Sprint(errcode.IsInfraError(err))}).Inc(context.Background())
}
`)
	writeFile(t, root, "docs/observability/metrics-migration-acks.yaml", `acknowledgements:
  - rule: OBS-01
    fingerprint: abc123
`)

	_, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing ")
}

func TestCheckOBS01AckSuppressesMatchingFingerprint(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "docs/ops/example-dashboard.md", "# example\n")
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func direct(err error) {
	counter.With(metrics.Labels{"reason": fmt.Sprint(errcode.IsInfraError(err))}).Inc(context.Background())
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	writeFile(t, root, "docs/observability/metrics-migration-acks.yaml", fmt.Sprintf(`acknowledgements:
  - rule: OBS-01
    fingerprint: %q
    metric: %s
    label: %s
    oldSemantics: infra errors grouped as infra
    newSemantics: domain config errors grouped as domain
    dashboardOrAlertRefs:
      - docs/ops/example-dashboard.md
    owner: platform-observability
    reviewedAt: "2026-04-28"
    rationale: reviewed SLO bucket migration with service owner
`, diagnostics[0].Fingerprint, diagnostics[0].Metric, diagnostics[0].Label))

	diagnostics, err = checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	assert.Empty(t, diagnostics)
}

func TestCheckOBS01AckSuppressesMatchingFingerprintWithTrackedDashboard(t *testing.T) {
	root := writeMetricsFixture(t)
	gitRun(t, root, "init")
	gitRun(t, root, "config", "user.email", "test@example.com")
	gitRun(t, root, "config", "user.name", "Test")
	gitRun(t, root, "config", "commit.gpgsign", "false")
	writeFile(t, root, "docs/ops/example-dashboard.md", "# example\n")
	gitRun(t, root, "add", "docs/ops/example-dashboard.md")
	gitRun(t, root, "commit", "-q", "-m", "fixture", "--no-gpg-sign")
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func direct(err error) {
	counter.With(metrics.Labels{"reason": fmt.Sprint(errcode.IsInfraError(err))}).Inc(context.Background())
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	writeFile(t, root, "docs/observability/metrics-migration-acks.yaml", fmt.Sprintf(`acknowledgements:
  - rule: OBS-01
    fingerprint: %q
    metric: %s
    label: %s
    oldSemantics: infra errors grouped as infra
    newSemantics: domain config errors grouped as domain
    dashboardOrAlertRefs:
      - docs/ops/example-dashboard.md
    owner: platform-observability
    reviewedAt: "2026-04-28"
    rationale: reviewed SLO bucket migration with service owner
`, diagnostics[0].Fingerprint, diagnostics[0].Metric, diagnostics[0].Label))

	diagnostics, err = checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	assert.Empty(t, diagnostics)
}

func TestCheckOBS01AckMustMatchMetricAndLabel(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "docs/ops/example-dashboard.md", "# example\n")
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func direct(err error) {
	counter.With(metrics.Labels{"reason": fmt.Sprint(errcode.IsInfraError(err))}).Inc(context.Background())
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)

	writeFile(t, root, "docs/observability/metrics-migration-acks.yaml", fmt.Sprintf(`acknowledgements:
  - rule: OBS-01
    fingerprint: %q
    metric: otherMetric
    label: %s
    oldSemantics: infra errors grouped as infra
    newSemantics: domain config errors grouped as domain
    dashboardOrAlertRefs:
      - docs/ops/example-dashboard.md
    owner: platform-observability
    reviewedAt: "2026-04-28"
    rationale: reviewed SLO bucket migration with service owner
`, diagnostics[0].Fingerprint, diagnostics[0].Label))

	diagnostics, err = checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unused or stale")
	require.Len(t, diagnostics, 1)

	writeFile(t, root, "docs/observability/metrics-migration-acks.yaml", fmt.Sprintf(`acknowledgements:
  - rule: OBS-01
    fingerprint: %q
    metric: %s
    label: otherLabel
    oldSemantics: infra errors grouped as infra
    newSemantics: domain config errors grouped as domain
    dashboardOrAlertRefs:
      - docs/ops/example-dashboard.md
    owner: platform-observability
    reviewedAt: "2026-04-28"
    rationale: reviewed SLO bucket migration with service owner
`, diagnostics[0].Fingerprint, "obs_total"))

	diagnostics, err = checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unused or stale")
	require.Len(t, diagnostics, 1)
}

func TestCheckOBS01AckCannotSuppressDynamicLabelKey(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "docs/ops/example-dashboard.md", "# example\n")
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"context"
	"fmt"
	"os"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var provider = metrics.NopProvider{}
var counter, _ = provider.CounterVec(metrics.CounterOpts{
	Name:       "obs_total",
	LabelNames: []string{"reason"},
})

func direct(err error) {
	label := os.Getenv("LABEL")
	counter.With(metrics.Labels{label: fmt.Sprint(errcode.IsInfraError(err))}).Inc(context.Background())
}
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	writeFile(t, root, "docs/observability/metrics-migration-acks.yaml", fmt.Sprintf(`acknowledgements:
  - rule: OBS-01
    fingerprint: %q
    metric: %s
    label: %s
    oldSemantics: infra errors grouped as infra
    newSemantics: domain config errors grouped as domain
    dashboardOrAlertRefs:
      - docs/ops/example-dashboard.md
    owner: platform-observability
    reviewedAt: "2026-04-28"
    rationale: reviewed SLO bucket migration with service owner
`, diagnostics[0].Fingerprint, diagnostics[0].Metric, diagnostics[0].Label))

	diagnostics, err = checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unused or stale")
	require.Len(t, diagnostics, 1)
	assert.Contains(t, diagnostics[0].Message, "not machine-resolvable")
}

func TestCheckOBS01RejectsDuplicateAckFingerprints(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "docs/ops/example-dashboard.md", "# example\n")
	writeFile(t, root, "docs/observability/metrics-migration-acks.yaml", `acknowledgements:
  - rule: OBS-01
    fingerprint: duplicate
    metric: v
    label: reason
    oldSemantics: infra errors grouped as infra
    newSemantics: domain config errors grouped as domain
    dashboardOrAlertRefs:
      - docs/ops/example-dashboard.md
    owner: platform-observability
    reviewedAt: "2026-04-28"
    rationale: reviewed SLO bucket migration with service owner
  - rule: OBS-01
    fingerprint: duplicate
    metric: v
    label: reason
    oldSemantics: infra errors grouped as infra
    newSemantics: domain config errors grouped as domain
    dashboardOrAlertRefs:
      - docs/ops/example-dashboard.md
    owner: platform-observability
    reviewedAt: "2026-04-28"
    rationale: reviewed SLO bucket migration with service owner
`)

	_, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicates fingerprint")
}

func TestCheckOBS01RejectsURLDashboardRefs(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "docs/observability/metrics-migration-acks.yaml", `acknowledgements:
  - rule: OBS-01
    fingerprint: abc123
    metric: fixture_obs_total
    label: reason
    oldSemantics: infra errors grouped as infra
    newSemantics: domain config errors grouped as domain
    dashboardOrAlertRefs:
      - https://example.com/dashboard
    owner: platform-observability
    reviewedAt: "2026-04-28"
    rationale: reviewed SLO bucket migration with service owner
`)

	_, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "committed in HEAD")
}

func TestCheckOBS01RejectsUnusedAck(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "docs/ops/example-dashboard.md", "# example\n")
	writeFile(t, root, "docs/observability/metrics-migration-acks.yaml", `acknowledgements:
  - rule: OBS-01
    fingerprint: stale-fingerprint
    metric: fixture_obs_total
    label: reason
    oldSemantics: infra errors grouped as infra
    newSemantics: domain config errors grouped as domain
    dashboardOrAlertRefs:
      - docs/ops/example-dashboard.md
    owner: platform-observability
    reviewedAt: "2026-04-28"
    rationale: reviewed SLO bucket migration with service owner
`)

	diagnostics, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unused or stale")
	assert.Empty(t, diagnostics)
}

func TestCheckOBS01RejectsSymlinkDashboardRefs(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "docs/ops/example-dashboard.md", "# example\n")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docs", "ops"), 0o755))
	linkPath := filepath.Join(root, "docs", "ops", "dashboard-link.md")
	if err := os.Symlink("example-dashboard.md", linkPath); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	writeFile(t, root, "docs/observability/metrics-migration-acks.yaml", `acknowledgements:
  - rule: OBS-01
    fingerprint: abc123
    metric: fixture_obs_total
    label: reason
    oldSemantics: infra errors grouped as infra
    newSemantics: domain config errors grouped as domain
    dashboardOrAlertRefs:
      - docs/ops/dashboard-link.md
    owner: platform-observability
    reviewedAt: "2026-04-28"
    rationale: reviewed SLO bucket migration with service owner
`)

	_, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "committed in HEAD")
}

func TestCheckOBS01RejectsUntrackedDashboardRefsWhenGitMetadataPresent(t *testing.T) {
	root := writeMetricsFixture(t)
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))
	writeFile(t, root, "docs/ops/example-dashboard.md", "# example\n")
	writeFile(t, root, "docs/observability/metrics-migration-acks.yaml", `acknowledgements:
  - rule: OBS-01
    fingerprint: abc123
    metric: fixture_obs_total
    label: reason
    oldSemantics: infra errors grouped as infra
    newSemantics: domain config errors grouped as domain
    dashboardOrAlertRefs:
      - docs/ops/example-dashboard.md
    owner: platform-observability
    reviewedAt: "2026-04-28"
    rationale: reviewed SLO bucket migration with service owner
`)

	_, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "committed in HEAD")
}

// TestCheckOBS01RejectsStagedButUncommittedDashboardRef closes the
// PR #332 round-2 gap where OBS-01 ack tracking accepted index-only
// (`git add`-but-not-committed) files. The committed-in-HEAD predicate
// now matches the one used by generatedverify so a single CI step that
// only stages — never commits — cannot satisfy either gate.
func TestCheckOBS01RejectsStagedButUncommittedDashboardRef(t *testing.T) {
	root := writeMetricsFixture(t)
	gitRun(t, root, "init")
	gitRun(t, root, "config", "user.email", "test@example.com")
	gitRun(t, root, "config", "user.name", "Test")
	gitRun(t, root, "config", "commit.gpgsign", "false")
	writeFile(t, root, "docs/ops/example-dashboard.md", "# example\n")
	gitRun(t, root, "add", "docs/ops/example-dashboard.md")
	// No commit — the file is in the index only.

	writeFile(t, root, "docs/observability/metrics-migration-acks.yaml", `acknowledgements:
  - rule: OBS-01
    fingerprint: abc123
    metric: fixture_obs_total
    label: reason
    oldSemantics: infra errors grouped as infra
    newSemantics: domain config errors grouped as domain
    dashboardOrAlertRefs:
      - docs/ops/example-dashboard.md
    owner: platform-observability
    reviewedAt: "2026-04-28"
    rationale: reviewed SLO bucket migration with service owner
`)

	_, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "committed in HEAD")
}

func TestCheckOBS01RejectsPlaceholderDashboardRefs(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "docs/observability/metrics-migration-acks.yaml", `acknowledgements:
  - rule: OBS-01
    fingerprint: abc123
    metric: fixture_obs_total
    label: reason
    oldSemantics: infra errors grouped as infra
    newSemantics: domain config errors grouped as domain
    dashboardOrAlertRefs:
      - TODO
    owner: platform-observability
    reviewedAt: "2026-04-28"
    rationale: reviewed SLO bucket migration with service owner
`)

	_, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dashboardOrAlertRefs")
}

func TestCheckOBS01RejectsDashboardRefPathTraversal(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "docs/observability/metrics-migration-acks.yaml", `acknowledgements:
  - rule: OBS-01
    fingerprint: abc123
    metric: fixture_obs_total
    label: reason
    oldSemantics: infra errors grouped as infra
    newSemantics: domain config errors grouped as domain
    dashboardOrAlertRefs:
      - docs/../../go.mod
    owner: platform-observability
    reviewedAt: "2026-04-28"
    rationale: reviewed SLO bucket migration with service owner
`)

	_, err := checkOBS01WithPatterns(t.Context(), root, "./reachable")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dashboardOrAlertRefs")
}

func requireMetric(t *testing.T, schema *Schema, name string) Entry {
	t.Helper()
	for _, m := range schema.Metrics {
		if m.Name == name {
			return m
		}
	}
	names := make([]string, 0, len(schema.Metrics))
	for _, m := range schema.Metrics {
		names = append(names, m.Name)
	}
	slices.Sort(names)
	t.Fatalf("metric %q not found; got %s", name, strings.Join(names, ", "))
	return Entry{}
}

func metricNames(schema *Schema) []string {
	out := make([]string, 0, len(schema.Metrics))
	for _, m := range schema.Metrics {
		out = append(out, m.Name)
	}
	slices.Sort(out)
	return out
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func fixtureProject() *metadata.ProjectMeta {
	const (
		id         = "fixture"
		entrypoint = "cmd/app/main.go"
	)
	return &metadata.ProjectMeta{
		Assemblies: map[string]*metadata.AssemblyMeta{
			id: {
				ID: id,
				Build: metadata.BuildMeta{
					Entrypoint: entrypoint,
				},
			},
		},
	}
}

func writeMetricsFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeMetricsFixtureFiles(t, root)
	// Overwrite the pre-tidy go.mod/go.sum with the tidied canonical pair
	// (computed once, reused across all fixtures) so the module graph is complete
	// under -mod=readonly without a tidy per test.
	mod, sum := canonicalMetricsFixtureMod(t)
	writeFile(t, root, "go.mod", mod)
	writeFile(t, root, "go.sum", sum)
	return root
}

// writeMetricsFixtureFiles lays down the throwaway metrics-fixture module tree:
// a clone of the slimmed post-#1558 root go.mod plus the prometheus-adapter
// require/replace, a cmd/app entrypoint, and reachable/unreachable packages.
// The go.mod/go.sum it writes are the pre-tidy starting point.
func writeMetricsFixtureFiles(t *testing.T, root string) {
	t.Helper()
	moduleRoot := filepath.ToSlash(repoRoot(t))
	mod, err := os.ReadFile(filepath.Join(repoRoot(t), "go.mod"))
	require.NoError(t, err)
	modText := strings.Replace(string(mod), "module github.com/ghbvf/gocell", "module example.com/metricsfixture", 1)
	modText += fmt.Sprintf("\nrequire github.com/ghbvf/gocell v0.0.0\nreplace github.com/ghbvf/gocell => %s\n", moduleRoot)
	// The fixture entrypoint (cmd/app/main.go) imports the prometheus adapter.
	// Since #1558 split each adapter into its own go.work module, adapters/
	// prometheus left the core module and its client_golang subtree left the
	// slimmed root go.mod. Replace the adapter module locally; tidyMetricsFixture
	// below then completes the require graph + go.sum so the fixture loads under
	// -mod=readonly (the mode packages.Load / Build use).
	modText += "require github.com/ghbvf/gocell/adapters/prometheus v0.0.0\n"
	modText += fmt.Sprintf("replace github.com/ghbvf/gocell/adapters/prometheus => %s/adapters/prometheus\n", moduleRoot)
	writeFile(t, root, "go.mod", modText)
	sum, err := os.ReadFile(filepath.Join(repoRoot(t), "go.sum"))
	require.NoError(t, err)
	writeFile(t, root, "go.sum", string(sum))
	writeFile(t, root, "docs/observability/metrics-migration-acks.yaml", "acknowledgements: []\n")
	writeFile(t, root, "cmd/app/main.go", `package main

import (
	adapterprom "github.com/ghbvf/gocell/adapters/prometheus"
	prom "github.com/prometheus/client_golang/prometheus"

	_ "example.com/metricsfixture/reachable"
)

func main() {
	_, _ = adapterprom.NewMetricProvider(adapterprom.MetricProviderConfig{
		Registry: prom.NewRegistry(),
		Namespace: "fixture",
	})
}
`)
	writeFile(t, root, "reachable/reachable.go", `package reachable

import (
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	prom "github.com/prometheus/client_golang/prometheus"
)

var ProviderMetric = metrics.CounterOpts{
	Name: "provider_total",
	Help: "provider metric",
	LabelNames: []string{"first", "second"},
}

var DirectMetric = prom.NewCounterVec(prom.CounterOpts{
	Namespace: "custom",
	Subsystem: "sub",
	Name: "direct_total",
	Help: "direct metric",
	ConstLabels: prom.Labels{"z_const": "z", "a_const": "a"},
}, []string{"first", "second"})

var HistMetric = prom.NewHistogramVec(prom.HistogramOpts{
	Name: "hist_seconds",
	Help: "hist metric",
	Buckets: []float64{0.1, 1},
}, []string{"route", "status"})
`)
	writeFile(t, root, "unreachable/unreachable.go", `package unreachable

import "github.com/ghbvf/gocell/kernel/observability/metrics"

var UnreachableMetric = metrics.CounterOpts{
	Name: "unreachable_total",
	Help: "must not be scanned",
}
`)
}

// metricsFixtureMod caches the tidied go.mod/go.sum for the metrics fixture so
// the (slow) `go mod tidy` runs once per test binary instead of once per
// fixture. The replace directives use the absolute repo root, identical for
// every fixture, so the cached bytes are reusable verbatim.
var metricsFixtureMod struct {
	once sync.Once
	mod  string
	sum  string
	err  error
}

// canonicalMetricsFixtureMod returns the tidied go.mod/go.sum produced by
// tidying one representative fixture module. See tidyMetricsFixture for why the
// clone of the slimmed post-#1558 root go.mod needs tidying at all.
func canonicalMetricsFixtureMod(t *testing.T) (string, string) {
	t.Helper()
	metricsFixtureMod.once.Do(func() {
		dir, err := os.MkdirTemp("", "metricsfixture-canon-")
		if err != nil {
			metricsFixtureMod.err = err
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()
		writeMetricsFixtureFiles(t, dir)
		tidyMetricsFixture(t, dir)
		mod, err := os.ReadFile(filepath.Join(dir, "go.mod")) //nolint:gosec // tempdir test fixture
		if err != nil {
			metricsFixtureMod.err = err
			return
		}
		sum, err := os.ReadFile(filepath.Join(dir, "go.sum")) //nolint:gosec // tempdir test fixture
		if err != nil {
			metricsFixtureMod.err = err
			return
		}
		metricsFixtureMod.mod, metricsFixtureMod.sum = string(mod), string(sum)
	})
	require.NoError(t, metricsFixtureMod.err, "tidy canonical metrics fixture module")
	return metricsFixtureMod.mod, metricsFixtureMod.sum
}

// tidyMetricsFixture runs `go mod tidy` on the throwaway fixture module so its
// go.mod lists the full prometheus-adapter dependency subtree (client_golang +
// indirects). The fixture clones the slimmed post-#1558 root go.mod, which no
// longer carries those modules, so without tidy the module graph is incomplete
// and packages.Load / Build fail under -mod=readonly ("updates to go.mod
// needed"). GOFLAGS=-mod=mod lets tidy write; GOWORK=off keeps it a single
// standalone module regardless of the ambient workspace mode.
func tidyMetricsFixture(t *testing.T, dir string) {
	t.Helper()
	goPath, lookErr := exec.LookPath("go")
	require.NoError(t, lookErr, "go not found in PATH")
	// Strip ambient GOWORK/GOFLAGS/GOTOOLCHAIN before appending ours, so our
	// values win unambiguously (append-only relies on last-wins, which is
	// fragile) and GOTOOLCHAIN=local stops tidy from triggering a network
	// toolchain download in a sandboxed CI runner.
	env := slices.DeleteFunc(os.Environ(), func(e string) bool {
		return strings.HasPrefix(e, "GOWORK=") ||
			strings.HasPrefix(e, "GOFLAGS=") ||
			strings.HasPrefix(e, "GOTOOLCHAIN=")
	})
	env = append(env, "GOWORK=off", "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local")
	cmd := &exec.Cmd{
		Path: goPath,
		Args: []string{"go", "mod", "tidy"},
		Dir:  dir,
		Env:  env,
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "go mod tidy metrics fixture: %s", out)
}

func productionGoTopLevels(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			top := strings.Split(rel, "/")[0]
			// A top-level dir that is its own go.work satellite module root (carries
			// go.mod, e.g. cellmodules/ #1559) is unaddressable by the ModeModule
			// (GOWORK=off) production scan that prodscan.Patterns drives, so it must
			// not be required by this coverage guard either — symmetric with
			// prodscan.Patterns skipping it (shared prodscan.IsModuleRoot is the
			// single source). Full coverage of satellites is the ModeWorkspace
			// migration tracked by gh #1590.
			if rel == top && prodscan.IsModuleRoot(path) {
				return filepath.SkipDir
			}
			if strings.HasPrefix(d.Name(), ".") || obs01CoverageExcludedTop(top) || d.Name() == "testdata" {
				if rel == top || d.Name() == "testdata" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		if strings.HasPrefix(rel, "testdata/") || strings.Contains(rel, "/testdata/") {
			return nil
		}
		top := "."
		if before, _, ok := strings.Cut(rel, "/"); ok {
			top = before
		}
		if !obs01CoverageExcludedTop(top) {
			out[top] = true
		}
		return nil
	})
	require.NoError(t, err)
	return out
}

func obs01CoverageExcludedTop(top string) bool {
	switch top {
	case "tools", "tests", "vendor", "generated":
		return true
	default:
		return false
	}
}

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func gitRun(t *testing.T, root string, args ...string) {
	t.Helper()
	gitPath, lookErr := exec.LookPath("git")
	require.NoError(t, lookErr, "git not found in PATH")
	cmdArgs := append([]string{"git"}, args...)
	cmd := &exec.Cmd{Path: gitPath, Args: cmdArgs, Dir: root}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s failed:\n%s", strings.Join(args, " "), string(out))
}
