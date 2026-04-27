package governance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeMetricsFixtures writes Go source files under root/<rel> for metrics scanner tests.
func writeMetricsFixtures(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, src := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(src), 0o644))
	}
}

func TestBuildMetricsSchema_EmptyDirs(t *testing.T) {
	root := t.TempDir()
	schema, err := BuildMetricsSchema(root, "testasm")
	require.NoError(t, err)
	assert.Equal(t, "testasm", schema.AssemblyID)
	assert.Empty(t, schema.Metrics)
}

func TestBuildMetricsSchema_CounterEntry(t *testing.T) {
	root := t.TempDir()
	writeMetricsFixtures(t, root, map[string]string{
		"runtime/observability/metrics/m.go": `package metrics
import kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
func register(p kernelmetrics.Provider) {
    _, _ = p.CounterVec(kernelmetrics.CounterOpts{
        Name:       "http_requests_total",
        Help:       "Total HTTP requests.",
        LabelNames: []string{"method", "route", "status", "cell"},
    })
}
`,
	})
	schema, err := BuildMetricsSchema(root, "testasm")
	require.NoError(t, err)
	require.Len(t, schema.Metrics, 1)
	e := schema.Metrics[0]
	assert.Equal(t, "http_requests_total", e.Name)
	assert.Equal(t, "counter", e.Type)
	assert.Equal(t, "Total HTTP requests.", e.Help)
	assert.Equal(t, []string{"cell", "method", "route", "status"}, e.Labels) // sorted
	assert.Empty(t, e.Buckets)
	assert.Contains(t, e.File, "runtime/observability/metrics/m.go")
	assert.Greater(t, e.Line, 0)
}

func TestBuildMetricsSchema_HistogramWithLiteralBuckets(t *testing.T) {
	root := t.TempDir()
	writeMetricsFixtures(t, root, map[string]string{
		"runtime/observability/metrics/hist.go": `package metrics
import kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
func register(p kernelmetrics.Provider) {
    _, _ = p.HistogramVec(kernelmetrics.HistogramOpts{
        Name:       "http_request_duration_seconds",
        Help:       "HTTP request duration.",
        LabelNames: []string{"method", "route"},
        Buckets:    []float64{0.005, 0.01, 0.025, 0.05},
    })
}
`,
	})
	schema, err := BuildMetricsSchema(root, "testasm")
	require.NoError(t, err)
	require.Len(t, schema.Metrics, 1)
	e := schema.Metrics[0]
	assert.Equal(t, "histogram", e.Type)
	assert.Equal(t, []string{"method", "route"}, e.Labels)
	assert.Equal(t, []string{"0.005", "0.01", "0.025", "0.05"}, e.Buckets)
}

func TestBuildMetricsSchema_HistogramWithVariableBuckets(t *testing.T) {
	root := t.TempDir()
	writeMetricsFixtures(t, root, map[string]string{
		"runtime/observability/metrics/hist.go": `package metrics
import kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
var DefaultBuckets = []float64{0.1, 0.5, 1.0}
func register(p kernelmetrics.Provider) {
    _, _ = p.HistogramVec(kernelmetrics.HistogramOpts{
        Name:    "some_histogram",
        Buckets: DefaultBuckets,
    })
}
`,
	})
	schema, err := BuildMetricsSchema(root, "testasm")
	require.NoError(t, err)
	require.Len(t, schema.Metrics, 1)
	e := schema.Metrics[0]
	// Variable reference captured as <VarName>
	assert.Equal(t, []string{"<DefaultBuckets>"}, e.Buckets)
}

func TestBuildMetricsSchema_GaugeEntry(t *testing.T) {
	root := t.TempDir()
	writeMetricsFixtures(t, root, map[string]string{
		"adapters/postgres/metrics.go": `package postgres
import m "github.com/ghbvf/gocell/kernel/observability/metrics"
func register(p m.Provider) {
    _, _ = p.HistogramVec(m.HistogramOpts{
        Name:       "pg_query_duration_seconds",
        LabelNames: []string{"op"},
    })
}
`,
	})
	schema, err := BuildMetricsSchema(root, "testasm")
	require.NoError(t, err)
	require.Len(t, schema.Metrics, 1)
	assert.Equal(t, "histogram", schema.Metrics[0].Type)
	assert.Contains(t, schema.Metrics[0].File, "adapters/postgres/metrics.go")
}

func TestBuildMetricsSchema_SkipsTestFiles(t *testing.T) {
	root := t.TempDir()
	writeMetricsFixtures(t, root, map[string]string{
		"runtime/observability/metrics/m_test.go": `package metrics
import kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
func TestRegister() {
    _ = kernelmetrics.CounterOpts{Name: "test_metric", LabelNames: []string{"x"}}
}
`,
	})
	schema, err := BuildMetricsSchema(root, "testasm")
	require.NoError(t, err)
	assert.Empty(t, schema.Metrics, "test files must be skipped")
}

func TestBuildMetricsSchema_AnonymousNameSkipped(t *testing.T) {
	root := t.TempDir()
	writeMetricsFixtures(t, root, map[string]string{
		"runtime/observability/metrics/m.go": `package metrics
import kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
// no Name field — should be skipped
var _ = kernelmetrics.CounterOpts{Help: "no name", LabelNames: []string{"x"}}
`,
	})
	schema, err := BuildMetricsSchema(root, "testasm")
	require.NoError(t, err)
	assert.Empty(t, schema.Metrics, "nameless Opts literals must be skipped")
}

func TestBuildMetricsSchema_SortedByName(t *testing.T) {
	root := t.TempDir()
	writeMetricsFixtures(t, root, map[string]string{
		"runtime/observability/metrics/m.go": `package metrics
import kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
func register(p kernelmetrics.Provider) {
    _, _ = p.CounterVec(kernelmetrics.CounterOpts{Name: "z_metric", LabelNames: []string{}})
    _, _ = p.CounterVec(kernelmetrics.CounterOpts{Name: "a_metric", LabelNames: []string{}})
}
`,
	})
	schema, err := BuildMetricsSchema(root, "testasm")
	require.NoError(t, err)
	require.Len(t, schema.Metrics, 2)
	assert.Equal(t, "a_metric", schema.Metrics[0].Name)
	assert.Equal(t, "z_metric", schema.Metrics[1].Name)
}

func TestBuildMetricsSchema_MultipleFiles(t *testing.T) {
	root := t.TempDir()
	writeMetricsFixtures(t, root, map[string]string{
		"runtime/observability/metrics/a.go": `package metrics
import kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
func registerA(p kernelmetrics.Provider) {
    _, _ = p.CounterVec(kernelmetrics.CounterOpts{Name: "metric_a", LabelNames: []string{"cell"}})
}
`,
		"adapters/pg/metrics.go": `package pg
import kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
func registerB(p kernelmetrics.Provider) {
    _, _ = p.CounterVec(kernelmetrics.CounterOpts{Name: "metric_b", LabelNames: []string{"db"}})
}
`,
	})
	schema, err := BuildMetricsSchema(root, "testasm")
	require.NoError(t, err)
	require.Len(t, schema.Metrics, 2)
	names := []string{schema.Metrics[0].Name, schema.Metrics[1].Name}
	assert.Contains(t, names, "metric_a")
	assert.Contains(t, names, "metric_b")
}

func TestBuildMetricsSchema_UnresolvableLabels(t *testing.T) {
	root := t.TempDir()
	writeMetricsFixtures(t, root, map[string]string{
		"runtime/observability/metrics/m.go": `package metrics
import kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
var sharedLabels = []string{"cell"}
func register(p kernelmetrics.Provider) {
    _, _ = p.CounterVec(kernelmetrics.CounterOpts{
        Name:       "metric_var_labels",
        LabelNames: sharedLabels,
    })
}
`,
	})
	schema, err := BuildMetricsSchema(root, "testasm")
	require.NoError(t, err)
	// Variable-reference LabelNames returns nil (not a composite lit)
	require.Len(t, schema.Metrics, 1)
	// Labels should be nil/empty when the value is not a composite literal
	assert.Nil(t, schema.Metrics[0].Labels)
}

func TestMarshalMetricsSchema_HasHeader(t *testing.T) {
	schema := &MetricsSchema{
		AssemblyID: "corebundle",
		Metrics:    []MetricEntry{{Name: "x", Type: "counter", Labels: []string{"l"}, File: "a.go", Line: 1}},
	}
	out, err := MarshalMetricsSchema(schema)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(out), "# Generated by gocell generate metrics-schema. DO NOT EDIT."),
		"output must start with canonical header")
	assert.Contains(t, string(out), "assemblyId: corebundle")
	assert.Contains(t, string(out), "name: x")
}

func TestMarshalMetricsSchema_Idempotent(t *testing.T) {
	root := t.TempDir()
	writeMetricsFixtures(t, root, map[string]string{
		"runtime/observability/metrics/m.go": `package metrics
import kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
func register(p kernelmetrics.Provider) {
    _, _ = p.CounterVec(kernelmetrics.CounterOpts{
        Name:       "req_total",
        Help:       "Total.",
        LabelNames: []string{"method", "status"},
    })
}
`,
	})
	schema1, err := BuildMetricsSchema(root, "asm")
	require.NoError(t, err)
	out1, err := MarshalMetricsSchema(schema1)
	require.NoError(t, err)

	schema2, err := BuildMetricsSchema(root, "asm")
	require.NoError(t, err)
	out2, err := MarshalMetricsSchema(schema2)
	require.NoError(t, err)

	assert.Equal(t, string(out1), string(out2), "two runs must produce identical output (idempotency)")
}

func TestBuildMetricsSchema_SkipsGeneratedDir(t *testing.T) {
	root := t.TempDir()
	writeMetricsFixtures(t, root, map[string]string{
		"runtime/observability/metrics/generated/m.go": `package metrics
import kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
var _ = kernelmetrics.CounterOpts{Name: "gen_metric", LabelNames: []string{}}
`,
		"runtime/observability/metrics/real.go": `package metrics
import kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
var _ = kernelmetrics.CounterOpts{Name: "real_metric", LabelNames: []string{}}
`,
	})
	schema, err := BuildMetricsSchema(root, "testasm")
	require.NoError(t, err)
	for _, e := range schema.Metrics {
		assert.NotContains(t, e.File, "generated/", "generated/ subdirs must be skipped")
	}
}
