package governance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureRoot writes one or more .go files under <root>/<rel>/ and returns
// the temp root. The inner file paths are relative to root.
func writeFixtures(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, src := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(src), 0o644))
	}
}

func runOBS01(t *testing.T, files map[string]string) []ValidationResult {
	t.Helper()
	root := t.TempDir()
	writeFixtures(t, root, files)
	v := &Validator{root: root}
	return v.validateOBS01()
}

func TestOBS01_LiteralLabels_NoViolation(t *testing.T) {
	src := `package metrics
import "github.com/ghbvf/gocell/kernel/observability/metrics"
import "github.com/ghbvf/gocell/pkg/errcode"
var _ = metrics.CounterOpts{LabelNames: []string{"cell", "topic"}}
var _ = errcode.Category(nil)
`
	results := runOBS01(t, map[string]string{
		"runtime/observability/metrics/m.go": src,
	})
	assert.Empty(t, results)
}

func TestOBS01_CategoryInLabels_Warning(t *testing.T) {
	src := `package metrics
import "github.com/ghbvf/gocell/kernel/observability/metrics"
import "github.com/ghbvf/gocell/pkg/errcode"
func register() {
    var err error
    _ = metrics.CounterOpts{LabelNames: []string{"cell", errcode.Category(err).String()}}
}
`
	results := runOBS01(t, map[string]string{
		"runtime/observability/metrics/m.go": src,
	})
	require.Len(t, results, 1)
	assert.Equal(t, ruleOBS01, results[0].Code)
	assert.Equal(t, SeverityWarning, results[0].Severity)
	assert.Contains(t, results[0].Message, "Category")
}

func TestOBS01_IsInfraErrorInLabels_Warning(t *testing.T) {
	src := `package metrics
import "github.com/ghbvf/gocell/kernel/observability/metrics"
import "github.com/ghbvf/gocell/pkg/errcode"
import "fmt"
func register() {
    var err error
    _ = metrics.HistogramOpts{LabelNames: []string{"cell", fmt.Sprint(errcode.IsInfraError(err))}}
}
`
	results := runOBS01(t, map[string]string{
		"runtime/observability/metrics/m.go": src,
	})
	require.Len(t, results, 1)
	assert.Contains(t, results[0].Message, "IsInfraError")
}

func TestOBS01_NonMetricsCompositeLit_Ignore(t *testing.T) {
	src := `package metrics
import "github.com/ghbvf/gocell/kernel/observability/metrics"
import "github.com/ghbvf/gocell/pkg/errcode"
type otherOpts struct{ LabelNames []string }
func register() {
    var err error
    _ = otherOpts{LabelNames: []string{errcode.Category(err).String()}}
    _ = metrics.CounterOpts{LabelNames: []string{"cell"}} // unrelated, no violation
}
`
	results := runOBS01(t, map[string]string{
		"runtime/observability/metrics/m.go": src,
	})
	assert.Empty(t, results)
}

func TestOBS01_AliasedImport_Detect(t *testing.T) {
	src := `package metrics
import m "github.com/ghbvf/gocell/kernel/observability/metrics"
import ec "github.com/ghbvf/gocell/pkg/errcode"
func register() {
    var err error
    _ = m.GaugeOpts{LabelNames: []string{ec.Category(err).String()}}
}
`
	results := runOBS01(t, map[string]string{
		"runtime/observability/metrics/m.go": src,
	})
	require.Len(t, results, 1)
	assert.Contains(t, results[0].Message, "Category")
}

func TestOBS01_NoLabelNamesKey_Ignore(t *testing.T) {
	src := `package metrics
import "github.com/ghbvf/gocell/kernel/observability/metrics"
import "github.com/ghbvf/gocell/pkg/errcode"
func register() {
    var err error
    _ = errcode.Category(err)
    _ = metrics.CounterOpts{Name: "x", Help: "y"}
}
`
	results := runOBS01(t, map[string]string{
		"runtime/observability/metrics/m.go": src,
	})
	assert.Empty(t, results)
}

func TestOBS01_OutsideScope_Ignore(t *testing.T) {
	src := `package cellpkg
import "github.com/ghbvf/gocell/kernel/observability/metrics"
import "github.com/ghbvf/gocell/pkg/errcode"
func register() {
    var err error
    _ = metrics.CounterOpts{LabelNames: []string{errcode.Category(err).String()}}
}
`
	results := runOBS01(t, map[string]string{
		"cells/foo/handler.go": src,
	})
	assert.Empty(t, results, "OBS-01 should only scan runtime/observability/metrics + adapters/")
}

func TestOBS01_AdaptersScope_Detect(t *testing.T) {
	src := `package adapter
import "github.com/ghbvf/gocell/kernel/observability/metrics"
import "github.com/ghbvf/gocell/pkg/errcode"
func register() {
    var err error
    _ = metrics.CounterOpts{LabelNames: []string{errcode.Category(err).String()}}
}
`
	results := runOBS01(t, map[string]string{
		"adapters/postgres/metrics.go": src,
	})
	require.Len(t, results, 1)
}
