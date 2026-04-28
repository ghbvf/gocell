package metricschema

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuild_CorebundleCapturesReachableTypedMetrics(t *testing.T) {
	root := repoRoot(t)
	project, err := metadata.NewParser(root).Parse()
	require.NoError(t, err)

	schema, err := Build(root, project, "corebundle")
	require.NoError(t, err)

	hookTotal := requireMetric(t, schema, "cell_hook_total")
	assert.Equal(t, []string{"cell_id", "hook", "outcome"}, hookTotal.Labels)
	assert.Equal(t, "adapters/prometheus/hook_observer.go", hookTotal.File)

	authVerify := requireMetric(t, schema, "auth_token_verify_total")
	assert.Equal(t, []string{"result", "reason"}, authVerify.Labels)
	assert.Equal(t, "gocell_auth_token_verify_total", authVerify.FQName)
	assert.Equal(t, "gocell", authVerify.Namespace)
	assert.Equal(t, "runtime/auth/metrics.go", authVerify.File)

	outboxRelayed := requireMetric(t, schema, "outbox_relayed_total")
	assert.Equal(t, []string{"cell", "outcome"}, outboxRelayed.Labels)
	assert.Equal(t, "gocell_outbox_relayed_total", outboxRelayed.FQName)
	assert.Equal(t, "kernel/outbox/relay_collector.go", outboxRelayed.File)

	httpDuration := requireMetric(t, schema, "http_request_duration_seconds")
	assert.Equal(t,
		"github.com/ghbvf/gocell/runtime/observability/metrics.ProviderCollectorConfig.DurationBuckets",
		httpDuration.BucketSource)

	vaultLogin := requireMetric(t, schema, "auth_login_total")
	assert.Equal(t, "gocell_vault_auth_login_total", vaultLogin.FQName)
	assert.Equal(t, []string{"method", "result", "reason"}, vaultLogin.Labels)

	for _, m := range schema.Metrics {
		for _, label := range m.Labels {
			assert.NotContains(t, label, "<", "labels must not contain unresolved placeholders")
		}
		for _, bucket := range m.Buckets {
			assert.NotContains(t, bucket, "<", "buckets must not contain unresolved placeholders")
		}
	}
}

func TestBuild_FixtureLocksReachabilityIdentityAndLabels(t *testing.T) {
	root := writeMetricsFixture(t)
	project := fixtureProject("fixture", "cmd/app/main.go")

	schema, err := Build(root, project, "fixture")
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
	project := fixtureProject("fixture", "cmd/app/main.go")

	_, err := Build(root, project, "fixture")
	require.ErrorIs(t, err, ErrUnresolvedLabel)
	assert.Contains(t, err.Error(), "metric name must be a compile-time string")
}

func TestCheckOBS01DetectsDirectLocalAndHelperParamClassifiers(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"errors"
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	ec "github.com/ghbvf/gocell/pkg/errcode"
)

func direct(v metrics.CounterVec, err error) {
	v.With(metrics.Labels{"reason": fmt.Sprint(ec.IsInfraError(err))}).Inc()
}

func local(v metrics.CounterVec, err error) {
	reason := fmt.Sprint(ec.IsInfraError(err))
	v.With(metrics.Labels{"reason": reason}).Inc()
}

func helper(v metrics.CounterVec, reason string) {
	v.With(metrics.Labels{"reason": reason}).Inc()
}

func viaHelper(v metrics.CounterVec, err error) {
	helper(v, fmt.Sprint(ec.IsInfraError(err)))
}

func negative(v metrics.CounterVec, err error) {
	if errors.Is(err, errors.ErrUnsupported) {
		v.With(metrics.Labels{"reason": "unsupported"}).Inc()
	}
}
`)

	diagnostics, err := CheckOBS01(root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 3)
	assert.Contains(t, diagnostics[0].Message, "metrics-migration-acks.yaml")
}

func TestCheckOBS01RequiresStrictAckFields(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func direct(v metrics.CounterVec, err error) {
	v.With(metrics.Labels{"reason": fmt.Sprint(errcode.IsInfraError(err))}).Inc()
}
`)
	writeFile(t, root, "docs/observability/metrics-migration-acks.yaml", `acknowledgements:
  - rule: OBS-01
    fingerprint: abc123
`)

	_, err := CheckOBS01(root, "./reachable")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing ")
}

func TestCheckOBS01AckSuppressesMatchingFingerprint(t *testing.T) {
	root := writeMetricsFixture(t)
	writeFile(t, root, "reachable/obs.go", `package reachable

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func direct(v metrics.CounterVec, err error) {
	v.With(metrics.Labels{"reason": fmt.Sprint(errcode.IsInfraError(err))}).Inc()
}
`)

	diagnostics, err := CheckOBS01(root, "./reachable")
	require.NoError(t, err)
	require.Len(t, diagnostics, 1)
	writeFile(t, root, "docs/observability/metrics-migration-acks.yaml", fmt.Sprintf(`acknowledgements:
  - rule: OBS-01
    fingerprint: %q
    metric: provider_total
    label: reason
    oldSemantics: infra errors grouped as infra
    newSemantics: domain config errors grouped as domain
    dashboardOrAlertRefs:
      - docs/ops/example-dashboard.md
    owner: platform-observability
    reviewedAt: "2026-04-28"
    rationale: reviewed SLO bucket migration with service owner
`, diagnostics[0].Fingerprint))

	diagnostics, err = CheckOBS01(root, "./reachable")
	require.NoError(t, err)
	assert.Empty(t, diagnostics)
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

func fixtureProject(id, entrypoint string) *metadata.ProjectMeta {
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
	moduleRoot := filepath.ToSlash(repoRoot(t))
	writeFile(t, root, "go.mod", fmt.Sprintf(`module example.com/metricsfixture

go 1.25.0

require (
	github.com/ghbvf/gocell v0.0.0
	github.com/prometheus/client_golang v1.23.2
)

replace github.com/ghbvf/gocell => %s
`, moduleRoot))
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
	cmd := exec.Command("go", "mod", "tidy")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return root
}

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}
