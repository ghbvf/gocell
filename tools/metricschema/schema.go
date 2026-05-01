// Package metricschema builds the generated metrics schema from type-checked
// assembly reachability.
package metricschema

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/format"
	"go/token"
	"go/types"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
	"golang.org/x/tools/go/packages"
	"gopkg.in/yaml.v3"
)

const (
	kernelMetricsPkg  = "github.com/ghbvf/gocell/kernel/observability/metrics"
	kernelOutboxPkg   = "github.com/ghbvf/gocell/kernel/outbox"
	runtimeMetricsPkg = "github.com/ghbvf/gocell/runtime/observability/metrics"
	adapterPromPkg    = "github.com/ghbvf/gocell/adapters/prometheus"
	prometheusPkg     = "github.com/prometheus/client_golang/prometheus"
	errcodePkg        = "github.com/ghbvf/gocell/pkg/errcode"
)

// Schema describes concrete metric registrations reachable from an assembly
// entrypoint.
type Schema struct {
	AssemblyID string  `yaml:"assemblyId"`
	Scope      string  `yaml:"scope"`
	Entrypoint string  `yaml:"entrypoint"`
	Metrics    []Entry `yaml:"metrics"`
}

// Entry describes a single concrete metric registration.
type Entry struct {
	Name         string   `yaml:"name"`
	FQName       string   `yaml:"fqName,omitempty"`
	Namespace    string   `yaml:"namespace,omitempty"`
	Subsystem    string   `yaml:"subsystem,omitempty"`
	Type         string   `yaml:"type"`
	Help         string   `yaml:"help,omitempty"`
	Labels       []string `yaml:"labels"`
	ConstLabels  []string `yaml:"constLabels,omitempty"`
	Buckets      []string `yaml:"buckets,omitempty"`
	BucketSource string   `yaml:"bucketSource,omitempty"`
	File         string   `yaml:"file"`
	Line         int      `yaml:"-"`
}

// Diagnostic describes a typed OBS-01 violation.
type Diagnostic struct {
	Rule        string
	File        string
	Line        int
	Column      int
	Metric      string
	Label       string
	Fingerprint string
	Message     string
}

// ErrUnresolvedMetricSchema is returned when a concrete metric registration
// has an unresolved identity field. Names, labels, namespaces, const-label keys,
// and buckets are part of the generated schema contract, so the generator fails
// instead of emitting an empty or placeholder value.
var ErrUnresolvedMetricSchema = errcode.New(errcode.ErrMetricsSchemaUnresolved, "metrics schema: unresolved metric schema")

// ErrUnresolvedLabel is kept as the historical sentinel for existing callers
// that only checked unresolved labels.
var ErrUnresolvedLabel = ErrUnresolvedMetricSchema

const header = governance.YAMLGeneratedPrefix + "metrics-schema. DO NOT EDIT.\n"

type scanPackage struct {
	pkg                *packages.Package
	inits              map[types.Object]ast.Expr
	fset               *token.FileSet
	root               string
	seenOpts           map[*ast.CompositeLit]bool
	namespace          string
	prometheusOptSinks map[*types.Func][]prometheusOptSink
}

type opts struct {
	name         string
	namespace    string
	subsystem    string
	help         string
	labels       []string
	constLabels  []string
	buckets      []string
	bucketSource string
}

type prometheusOptSink struct {
	ParamIndex      int
	LabelParamIndex int
	MetricType      string
	Vec             bool
	Labels          []string
}

type obs01MetricIdentity struct {
	Metric   string
	Labels   []string
	Resolved bool
}

// Build walks the package graph reachable from assemblyID's build entrypoint
// and returns all concrete metric registrations in project-owned packages.
func Build(projectRoot string, project *metadata.ProjectMeta, assemblyID string) (*Schema, error) {
	asm := project.Assemblies[assemblyID]
	if asm == nil {
		return nil, fmt.Errorf("assembly %q not found", assemblyID)
	}
	entrypoint := asm.Build.Entrypoint
	if entrypoint == "" {
		entrypoint = filepath.Join("cmd", assemblyID, "main.go")
	}
	pattern := "./" + filepath.ToSlash(filepath.Dir(entrypoint))
	pkgs, err := loadReachablePackages(projectRoot, pattern)
	if err != nil {
		return nil, err
	}
	inits := collectInits(pkgs)
	namespace, err := prometheusProviderNamespace(projectRoot, pkgs, inits)
	if err != nil {
		return nil, err
	}
	prometheusOptSinks := collectPrometheusOptSinks(projectRoot, pkgs, inits)

	schema := &Schema{
		AssemblyID: assemblyID,
		Scope:      "assembly-reachable",
		Entrypoint: filepath.ToSlash(entrypoint),
	}
	for _, p := range pkgs {
		sp := newScanPackage(projectRoot, p, namespace, inits, prometheusOptSinks)
		entries, scanErr := sp.scanMetrics()
		if scanErr != nil {
			return nil, scanErr
		}
		schema.Metrics = append(schema.Metrics, entries...)
	}
	sort.Slice(schema.Metrics, func(i, j int) bool {
		if schema.Metrics[i].Name != schema.Metrics[j].Name {
			return schema.Metrics[i].Name < schema.Metrics[j].Name
		}
		if schema.Metrics[i].File != schema.Metrics[j].File {
			return schema.Metrics[i].File < schema.Metrics[j].File
		}
		return schema.Metrics[i].Line < schema.Metrics[j].Line
	})
	return schema, nil
}

// Marshal serializes schema with the generated-file header.
func Marshal(schema *Schema) ([]byte, error) {
	body, err := yaml.Marshal(schema)
	if err != nil {
		return nil, err
	}
	return append([]byte(header), body...), nil
}

func loadPackages(root string, patterns ...string) ([]*packages.Package, error) {
	return loadPackagesWithMode(root, false, patterns...)
}

func loadReachablePackages(root string, patterns ...string) ([]*packages.Package, error) {
	return loadPackagesWithMode(root, true, patterns...)
}

func loadPackagesWithMode(root string, includeDeps bool, patterns ...string) ([]*packages.Package, error) {
	if !includeDeps && len(patterns) > 1 {
		return loadPatternScopedPackages(root, patterns...)
	}
	cfg := &packages.Config{
		Mode: packageLoadMode(includeDeps),
		Dir:  root,
	}
	roots, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("packages.Load: %w", err)
	}
	var out []*packages.Package
	var loadErrs []packages.Error
	packages.Visit(roots, nil, func(p *packages.Package) {
		loadErrs = append(loadErrs, p.Errors...)
		if packageHasProjectFile(root, p) {
			out = append(out, p)
		}
	})
	if len(loadErrs) > 0 {
		return nil, fmt.Errorf("packages.Load: %d error(s): first=%v", len(loadErrs), loadErrs[0])
	}
	return out, nil
}

func loadPatternScopedPackages(root string, patterns ...string) ([]*packages.Package, error) {
	byPath := map[string]*packages.Package{}
	var paths []string
	for _, pattern := range patterns {
		pkgs, err := loadPackagesWithMode(root, false, pattern)
		if err != nil {
			return nil, err
		}
		paths = mergeLoadedPackages(byPath, paths, pkgs)
	}
	sort.Strings(paths)
	out := make([]*packages.Package, 0, len(paths))
	for _, path := range paths {
		out = append(out, byPath[path])
	}
	return out, nil
}

func mergeLoadedPackages(
	byPath map[string]*packages.Package,
	paths []string,
	pkgs []*packages.Package,
) []string {
	for _, p := range pkgs {
		if _, ok := byPath[p.PkgPath]; !ok {
			paths = append(paths, p.PkgPath)
		}
		if existing := byPath[p.PkgPath]; existing == nil || len(existing.Syntax) == 0 {
			byPath[p.PkgPath] = p
		}
	}
	return paths
}

func packageLoadMode(includeDeps bool) packages.LoadMode {
	mode := packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
		packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo |
		packages.NeedImports
	if includeDeps {
		mode |= packages.NeedDeps
	}
	return mode
}

func collectInits(pkgs []*packages.Package) map[types.Object]ast.Expr {
	out := map[types.Object]ast.Expr{}
	for _, p := range pkgs {
		for _, file := range p.Syntax {
			collectInitsFromFile(out, p.TypesInfo, file)
		}
	}
	return out
}

func collectInitsFromFile(out map[types.Object]ast.Expr, info *types.Info, file *ast.File) {
	ast.Inspect(file, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		collectInitsFromValueSpec(out, info, vs)
		return true
	})
}

func collectInitsFromValueSpec(out map[types.Object]ast.Expr, info *types.Info, vs *ast.ValueSpec) {
	for i, name := range vs.Names {
		if i >= len(vs.Values) {
			continue
		}
		if obj := info.Defs[name]; obj != nil {
			out[obj] = vs.Values[i]
		}
	}
}

func packageHasProjectFile(root string, p *packages.Package) bool {
	for _, f := range p.CompiledGoFiles {
		if isProjectGoFile(root, f) {
			return true
		}
	}
	return false
}

func isProjectGoFile(root, file string) bool {
	if strings.HasSuffix(file, "_test.go") {
		return false
	}
	rel, err := filepath.Rel(root, file)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return false
	}
	rel = filepath.ToSlash(rel)
	return !strings.HasPrefix(rel, "tools/") && !strings.Contains(rel, "/testdata/")
}

func newScanPackage(
	root string,
	p *packages.Package,
	namespace string,
	inits map[types.Object]ast.Expr,
	prometheusOptSinks map[*types.Func][]prometheusOptSink,
) *scanPackage {
	if inits == nil {
		inits = collectInits([]*packages.Package{p})
	}
	if prometheusOptSinks == nil {
		prometheusOptSinks = map[*types.Func][]prometheusOptSink{}
	}
	sp := &scanPackage{
		pkg:                p,
		inits:              inits,
		fset:               p.Fset,
		root:               root,
		seenOpts:           map[*ast.CompositeLit]bool{},
		namespace:          namespace,
		prometheusOptSinks: prometheusOptSinks,
	}
	return sp
}

func collectPrometheusOptSinks(
	root string,
	pkgs []*packages.Package,
	inits map[types.Object]ast.Expr,
) map[*types.Func][]prometheusOptSink {
	out := map[*types.Func][]prometheusOptSink{}
	for _, p := range pkgs {
		sp := newScanPackage(root, p, "", inits, nil)
		for fn, sinks := range sp.collectPrometheusOptSinks() {
			out[fn] = append(out[fn], sinks...)
		}
	}
	return out
}

func prometheusProviderNamespace(root string, pkgs []*packages.Package, inits map[types.Object]ast.Expr) (string, error) {
	var namespace string
	for _, p := range pkgs {
		ns, err := prometheusProviderNamespaceForPackage(root, p, inits)
		if err != nil {
			return "", err
		}
		if ns == "" {
			continue
		}
		if namespace != "" && namespace != ns {
			return "", fmt.Errorf("metrics schema: multiple Prometheus provider namespaces found: %q and %q", namespace, ns)
		}
		namespace = ns
	}
	return namespace, nil
}

func prometheusProviderNamespaceForPackage(root string, p *packages.Package, inits map[types.Object]ast.Expr) (string, error) {
	sp := newScanPackage(root, p, "", inits, nil)
	var namespace string
	for _, file := range p.Syntax {
		path := p.Fset.Position(file.Pos()).Filename
		if !isProjectGoFile(root, path) {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return "", err
		}
		ns, err := sp.prometheusProviderNamespaceForFile(file, filepath.ToSlash(rel))
		if err != nil {
			return "", err
		}
		if ns == "" {
			continue
		}
		if namespace != "" && namespace != ns {
			return "", fmt.Errorf("metrics schema: multiple Prometheus provider namespaces found in %s: %q and %q",
				p.PkgPath, namespace, ns)
		}
		namespace = ns
	}
	return namespace, nil
}

func (sp *scanPackage) prometheusProviderNamespaceForFile(file *ast.File, rel string) (string, error) {
	var namespace string
	var scanErr error
	ast.Inspect(file, func(n ast.Node) bool {
		if scanErr != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ns, ok, err := sp.prometheusProviderNamespaceFromCall(call, rel)
		if err != nil {
			scanErr = err
			return false
		}
		if !ok || ns == "" {
			return true
		}
		if namespace != "" && namespace != ns {
			scanErr = fmt.Errorf("metrics schema: multiple Prometheus provider namespaces found in %s: %q and %q",
				rel, namespace, ns)
			return false
		}
		namespace = ns
		return true
	})
	return namespace, scanErr
}

func (sp *scanPackage) prometheusProviderNamespaceFromCall(call *ast.CallExpr, rel string) (string, bool, error) {
	if !isAdapterPrometheusNewMetricProvider(sp.pkg.TypesInfo, call) || len(call.Args) == 0 {
		return "", false, nil
	}
	lit := sp.resolveCompositeLit(call.Args[0])
	if lit == nil {
		return "", false, sp.unresolved(call.Args[0], rel, "prometheus metric provider config must be a resolvable literal")
	}
	return sp.metricProviderConfigNamespace(lit, rel)
}

func isAdapterPrometheusNewMetricProvider(info *types.Info, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == adapterPromPkg && fn.Name() == "NewMetricProvider"
}

func (sp *scanPackage) metricProviderConfigNamespace(lit *ast.CompositeLit, rel string) (string, bool, error) {
	pkgPath, typ, ok := namedType(sp.pkg.TypesInfo, lit.Type)
	if !ok || pkgPath != adapterPromPkg || typ != "MetricProviderConfig" {
		return "", false, nil
	}
	for _, elt := range lit.Elts {
		key, value, ok := keyValueField(elt)
		if !ok || key != "Namespace" {
			continue
		}
		ns, ok := sp.string(value)
		if !ok {
			return "", false, sp.unresolved(value, rel, "prometheus metric provider namespace must be a compile-time string")
		}
		return ns, true, nil
	}
	return "", false, nil
}

func (sp *scanPackage) scanMetrics() ([]Entry, error) {
	var entries []Entry
	for _, file := range sp.pkg.Syntax {
		path := sp.fset.Position(file.Pos()).Filename
		if !isProjectGoFile(sp.root, path) {
			continue
		}
		rel, err := filepath.Rel(sp.root, path)
		if err != nil {
			return nil, err
		}
		rel = filepath.ToSlash(rel)
		if skipMetricImplementationFile(rel) {
			continue
		}
		direct, scanErr := sp.scanDirectPrometheus(file, rel)
		if scanErr != nil {
			return nil, scanErr
		}
		entries = append(entries, direct...)
		generic, scanErr := sp.scanOptsLiterals(file, rel)
		if scanErr != nil {
			return nil, scanErr
		}
		entries = append(entries, generic...)
	}
	return entries, nil
}

func (sp *scanPackage) scanDirectPrometheus(file *ast.File, rel string) ([]Entry, error) {
	var entries []Entry
	for _, decl := range file.Decls {
		var direct []Entry
		var err error
		if fn, ok := decl.(*ast.FuncDecl); ok {
			direct, err = sp.scanDirectPrometheusNode(fn.Body, rel, functionParamSet(sp.pkg.TypesInfo, fn.Type))
		} else {
			direct, err = sp.scanDirectPrometheusNode(decl, rel, nil)
		}
		if err != nil {
			return nil, err
		}
		entries = append(entries, direct...)
	}
	return entries, nil
}

func (sp *scanPackage) scanDirectPrometheusNode(node ast.Node, rel string, params map[types.Object]bool) ([]Entry, error) {
	var entries []Entry
	var scanErr error
	ast.Inspect(node, func(n ast.Node) bool {
		if scanErr != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		callEntries, err := sp.directPrometheusCallEntries(call, rel, params)
		if err != nil {
			scanErr = err
			return false
		}
		entries = append(entries, callEntries...)
		return true
	})
	return entries, scanErr
}

func (sp *scanPackage) directPrometheusCallEntries(
	call *ast.CallExpr,
	rel string,
	params map[types.Object]bool,
) ([]Entry, error) {
	if entries, ok, err := sp.knownMetricWrapperEntries(call, rel); ok || err != nil {
		return entries, err
	}
	entry, ok, err := sp.directPrometheusEntry(call, rel, params)
	if err != nil || ok {
		if !ok {
			return nil, err
		}
		return []Entry{entry}, err
	}
	entries, _, err := sp.prometheusWrapperEntries(call, rel)
	return entries, err
}

func skipMetricImplementationFile(rel string) bool {
	switch rel {
	case "adapters/prometheus/metric_provider.go",
		"adapters/prometheus/hook_observer.go",
		"runtime/observability/metrics/provider_collector.go",
		"runtime/observability/metrics/config_event_collector.go",
		"kernel/outbox/relay_collector.go":
		return true
	}
	return false
}

func (sp *scanPackage) knownMetricWrapperEntries(call *ast.CallExpr, rel string) ([]Entry, bool, error) {
	fn := calledFunc(sp.pkg.TypesInfo, call)
	if fn == nil || fn.Pkg() == nil {
		return nil, false, nil
	}
	switch fn.Pkg().Path() {
	case adapterPromPkg:
		if fn.Name() == "NewHookObserver" {
			entries, err := sp.hookObserverEntries(call, rel)
			return entries, true, err
		}
	case runtimeMetricsPkg:
		if fn.Name() == "NewProviderCollector" {
			entries, err := sp.providerCollectorEntries(call, rel)
			return entries, true, err
		}
		if fn.Name() == "NewProviderConfigEventCollector" {
			entries, err := sp.providerConfigEventCollectorEntries(call, rel)
			return entries, true, err
		}
	case kernelOutboxPkg:
		if fn.Name() == "NewProviderRelayCollector" {
			entries, err := sp.providerRelayCollectorEntries(call, rel)
			return entries, true, err
		}
	}
	return nil, false, nil
}

func (sp *scanPackage) hookObserverEntries(call *ast.CallExpr, rel string) ([]Entry, error) {
	if len(call.Args) == 0 {
		return nil, sp.unresolved(call, rel, "prometheus hook observer config argument is missing")
	}
	lit := sp.resolveCompositeLit(call.Args[0])
	if lit == nil {
		return nil, sp.unresolved(call.Args[0], rel, "prometheus hook observer config must be a resolvable literal")
	}
	namespace := "gocell"
	if value, ok := compositeField(lit, "Namespace"); ok {
		ns, ok := sp.string(value)
		if !ok {
			return nil, sp.unresolved(value, rel, "prometheus hook observer namespace must be a compile-time string")
		}
		if ns != "" {
			namespace = ns
		}
	}
	buckets, err := sp.configBuckets(lit, "DurationBuckets", adapterPromPkg, "DefaultHookDurationBuckets", rel)
	if err != nil {
		return nil, err
	}
	return []Entry{
		sp.entryFromOpts("counter", opts{
			name:      "cell_hook_total",
			namespace: namespace,
			help:      "Total number of cell lifecycle hook invocations, partitioned by outcome.",
			labels:    []string{"cell_id", "hook", "outcome"},
		}, rel, call.Pos()),
		sp.entryFromOpts("histogram", opts{
			name:      "cell_hook_duration_seconds",
			namespace: namespace,
			help:      "Duration of cell lifecycle hook invocations in seconds.",
			labels:    []string{"cell_id", "hook"},
			buckets:   buckets,
		}, rel, call.Pos()),
	}, nil
}

func (sp *scanPackage) providerCollectorEntries(call *ast.CallExpr, rel string) ([]Entry, error) {
	if len(call.Args) < 2 {
		return nil, sp.unresolved(call, rel, "provider collector config argument is missing")
	}
	lit := sp.resolveCompositeLit(call.Args[1])
	if lit == nil {
		return nil, sp.unresolved(call.Args[1], rel, "provider collector config must be a resolvable literal")
	}
	buckets, err := sp.configBuckets(lit, "DurationBuckets", runtimeMetricsPkg, "DefaultDurationBuckets", rel)
	if err != nil {
		return nil, err
	}
	labels := []string{"method", "route", "status", "cell"}
	return []Entry{
		sp.entryFromOpts("counter", opts{
			name:      "http_requests_total",
			namespace: sp.namespace,
			help:      "Total number of HTTP requests.",
			labels:    labels,
		}, rel, call.Pos()),
		sp.entryFromOpts("histogram", opts{
			name:      "http_request_duration_seconds",
			namespace: sp.namespace,
			help:      "HTTP request duration in seconds.",
			labels:    labels,
			buckets:   buckets,
		}, rel, call.Pos()),
	}, nil
}

func (sp *scanPackage) providerConfigEventCollectorEntries(call *ast.CallExpr, rel string) ([]Entry, error) {
	if len(call.Args) == 0 {
		return nil, sp.unresolved(call, rel, "provider config event collector Provider argument is missing")
	}
	return []Entry{
		sp.entryFromOpts("counter", opts{
			name:      "config_event_process_total",
			namespace: sp.namespace,
			help:      "Total number of config event handler process results, partitioned by reason.",
			labels:    []string{"cell", "slice", "reason"},
		}, rel, call.Pos()),
		sp.entryFromOpts("counter", opts{
			name:      "config_event_settlement_total",
			namespace: sp.namespace,
			help:      "Total number of config event delivery settlements, partitioned by disposition and result.",
			labels:    []string{"cell", "slice", "disposition", "result"},
		}, rel, call.Pos()),
	}, nil
}

func (sp *scanPackage) providerRelayCollectorEntries(call *ast.CallExpr, rel string) ([]Entry, error) {
	var lit *ast.CompositeLit
	if len(call.Args) > 2 {
		lit = sp.resolveCompositeLit(call.Args[2])
		if lit == nil {
			return nil, sp.unresolved(call.Args[2], rel, "provider relay collector config must be a resolvable literal")
		}
	}
	pollBuckets, err := sp.configBuckets(lit, "PollBuckets", kernelOutboxPkg, "DefaultRelayPollBuckets", rel)
	if err != nil {
		return nil, err
	}
	batchBuckets, err := sp.configBuckets(lit, "BatchBuckets", kernelOutboxPkg, "DefaultRelayBatchBuckets", rel)
	if err != nil {
		return nil, err
	}
	return []Entry{
		sp.entryFromOpts("counter", opts{
			name:      "outbox_relayed_total",
			namespace: sp.namespace,
			help:      "Total number of outbox entries processed by the relay, by outcome.",
			labels:    []string{"cell", "outcome"},
		}, rel, call.Pos()),
		sp.entryFromOpts("histogram", opts{
			name:      "outbox_poll_duration_seconds",
			namespace: sp.namespace,
			help:      "Duration of each relay poll phase in seconds.",
			labels:    []string{"cell", "phase"},
			buckets:   pollBuckets,
		}, rel, call.Pos()),
		sp.entryFromOpts("histogram", opts{
			name:      "outbox_batch_size",
			namespace: sp.namespace,
			help:      "Number of entries claimed per relay poll cycle.",
			labels:    []string{"cell"},
			buckets:   batchBuckets,
		}, rel, call.Pos()),
		sp.entryFromOpts("counter", opts{
			name:      "outbox_reclaimed_total",
			namespace: sp.namespace,
			help:      "Total number of stale entries reclaimed by the relay.",
			labels:    []string{"cell"},
		}, rel, call.Pos()),
		sp.entryFromOpts("counter", opts{
			name:      "outbox_cleaned_total",
			namespace: sp.namespace,
			help:      "Total number of entries cleaned up (deleted) by the relay.",
			labels:    []string{"cell", "status"},
		}, rel, call.Pos()),
	}, nil
}

func (sp *scanPackage) configBuckets(
	lit *ast.CompositeLit,
	fieldName, defaultPkg, defaultName, rel string,
) ([]string, error) {
	if lit != nil {
		if value, ok := compositeField(lit, fieldName); ok {
			if isNilExpr(value) {
				return sp.defaultNumberSlice(defaultPkg, defaultName, rel)
			}
			buckets, err := sp.numberSlice(value, rel)
			if err != nil {
				return nil, err
			}
			if len(buckets) > 0 {
				return buckets, nil
			}
		}
	}
	return sp.defaultNumberSlice(defaultPkg, defaultName, rel)
}

func compositeField(lit *ast.CompositeLit, fieldName string) (ast.Expr, bool) {
	if lit == nil {
		return nil, false
	}
	for _, elt := range lit.Elts {
		key, value, ok := keyValueField(elt)
		if ok && key == fieldName {
			return value, true
		}
	}
	return nil, false
}

func isNilExpr(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "nil"
}

func (sp *scanPackage) collectPrometheusOptSinks() map[*types.Func][]prometheusOptSink {
	out := map[*types.Func][]prometheusOptSink{}
	for _, file := range sp.pkg.Syntax {
		rel := sp.relForNode(file)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			obj, sinks := sp.collectPrometheusOptSinksForFunc(fn, rel)
			if obj == nil || len(sinks) == 0 {
				continue
			}
			out[obj] = append(out[obj], sinks...)
		}
	}
	return out
}

func (sp *scanPackage) collectPrometheusOptSinksForFunc(
	fn *ast.FuncDecl,
	rel string,
) (*types.Func, []prometheusOptSink) {
	if fn.Body == nil {
		return nil, nil
	}
	obj, ok := sp.pkg.TypesInfo.Defs[fn.Name].(*types.Func)
	if !ok {
		return nil, nil
	}
	params := functionParamIndexes(sp.pkg.TypesInfo, fn.Type)
	if len(params) == 0 {
		return obj, nil
	}
	var sinks []prometheusOptSink
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sink, ok := sp.prometheusOptSinkFromCall(call, params, rel)
		if ok {
			sinks = append(sinks, sink)
		}
		return true
	})
	return obj, sinks
}

func (sp *scanPackage) prometheusOptSinkFromCall(
	call *ast.CallExpr,
	params map[types.Object]int,
	rel string,
) (prometheusOptSink, bool) {
	metricType, vec, ok := prometheusConstructor(sp.pkg.TypesInfo, call)
	if !ok || len(call.Args) == 0 {
		return prometheusOptSink{}, false
	}
	idx, ok := params[sp.objectForExpr(call.Args[0])]
	if !ok {
		return prometheusOptSink{}, false
	}
	sink := prometheusOptSink{
		ParamIndex:      idx,
		LabelParamIndex: -1,
		MetricType:      metricType,
		Vec:             vec,
	}
	if vec && len(call.Args) > 1 {
		if labelIdx, ok := params[sp.objectForExpr(call.Args[1])]; ok {
			sink.LabelParamIndex = labelIdx
		} else if labels, err := sp.stringSlice(call.Args[1], rel); err == nil {
			sink.Labels = labels
		}
	}
	return sink, true
}

func (sp *scanPackage) relForNode(node ast.Node) string {
	path := sp.fset.Position(node.Pos()).Filename
	rel, err := filepath.Rel(sp.root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}

func (sp *scanPackage) directPrometheusEntry(call *ast.CallExpr, rel string, params map[types.Object]bool) (Entry, bool, error) {
	if len(call.Args) == 0 {
		return Entry{}, false, nil
	}
	metricType, vec, ok := prometheusConstructor(sp.pkg.TypesInfo, call)
	if !ok {
		return Entry{}, false, nil
	}
	lit := sp.resolveCompositeLit(call.Args[0])
	if lit == nil {
		if params[sp.objectForExpr(call.Args[0])] {
			return Entry{}, false, nil
		}
		return Entry{}, false, sp.unresolved(call.Args[0], rel, "Prometheus metric opts must be a resolvable literal")
	}
	parsed, hasName, err := sp.extractPrometheusOpts(lit, rel)
	if err != nil || !hasName {
		return Entry{}, hasName, err
	}
	sp.seenOpts[lit] = true
	if vec {
		labels, labelErr := sp.prometheusVecLabels(call, rel)
		if labelErr != nil {
			return Entry{}, false, labelErr
		}
		parsed.labels = labels
	}
	return sp.entryFromOpts(metricType, parsed, rel, lit.Pos()), true, nil
}

func (sp *scanPackage) prometheusWrapperEntries(call *ast.CallExpr, rel string) ([]Entry, bool, error) {
	sinks := sp.prometheusOptSinks[calledFunc(sp.pkg.TypesInfo, call)]
	if len(sinks) == 0 {
		return nil, false, nil
	}
	entries := make([]Entry, 0, len(sinks))
	for _, sink := range sinks {
		if sink.ParamIndex >= len(call.Args) {
			return nil, false, sp.unresolved(call, rel, "Prometheus metric helper opts argument is missing")
		}
		lit := sp.resolveCompositeLit(call.Args[sink.ParamIndex])
		if lit == nil {
			return nil, false, sp.unresolved(call.Args[sink.ParamIndex], rel, "Prometheus metric helper opts must be a resolvable literal")
		}
		parsed, hasName, err := sp.extractPrometheusOpts(lit, rel)
		if err != nil {
			return nil, false, err
		}
		if !hasName {
			continue
		}
		sp.seenOpts[lit] = true
		if sink.Vec {
			labels, err := sp.prometheusWrapperLabels(call, sink, rel)
			if err != nil {
				return nil, false, err
			}
			parsed.labels = labels
		}
		entries = append(entries, sp.entryFromOpts(sink.MetricType, parsed, rel, lit.Pos()))
	}
	return entries, true, nil
}

func (sp *scanPackage) prometheusWrapperLabels(call *ast.CallExpr, sink prometheusOptSink, rel string) ([]string, error) {
	if sink.LabelParamIndex >= 0 {
		if sink.LabelParamIndex >= len(call.Args) {
			return nil, sp.unresolved(call, rel, "Prometheus metric helper label argument is missing")
		}
		return sp.stringSlice(call.Args[sink.LabelParamIndex], rel)
	}
	if sink.Labels != nil {
		return append([]string(nil), sink.Labels...), nil
	}
	return nil, sp.unresolved(call, rel, "Prometheus metric helper labels must be resolvable")
}

func (sp *scanPackage) prometheusVecLabels(call *ast.CallExpr, rel string) ([]string, error) {
	if len(call.Args) < 2 {
		return nil, sp.unresolved(call, rel, "missing Prometheus vec label argument")
	}
	return sp.stringSlice(call.Args[1], rel)
}

func (sp *scanPackage) scanOptsLiterals(file *ast.File, rel string) ([]Entry, error) {
	var entries []Entry
	var scanErr error
	ast.Inspect(file, func(n ast.Node) bool {
		if scanErr != nil {
			return false
		}
		lit, ok := n.(*ast.CompositeLit)
		if !ok || sp.seenOpts[lit] {
			return true
		}
		entry, ok, err := sp.optsLiteralEntry(lit, rel)
		if err != nil {
			scanErr = err
			return false
		}
		if ok {
			entries = append(entries, entry)
		}
		return true
	})
	return entries, scanErr
}

func (sp *scanPackage) optsLiteralEntry(lit *ast.CompositeLit, rel string) (Entry, bool, error) {
	pkgPath, typ, ok := namedType(sp.pkg.TypesInfo, lit.Type)
	if !ok {
		return Entry{}, false, nil
	}
	metricType, parsed, hasName, err := sp.extractTypedOpts(pkgPath, typ, lit, rel)
	if err != nil || !hasName {
		return Entry{}, false, err
	}
	return sp.entryFromOpts(metricType, parsed, rel, lit.Pos()), true, nil
}

func (sp *scanPackage) extractTypedOpts(pkgPath, typ string, lit *ast.CompositeLit, rel string) (string, opts, bool, error) {
	switch pkgPath {
	case kernelMetricsPkg:
		metricType, ok := kernelMetricType(typ)
		if !ok {
			return "", opts{}, false, nil
		}
		parsed, hasName, err := sp.extractKernelOpts(lit, rel)
		return metricType, parsed, hasName, err
	case prometheusPkg:
		metricType, ok := prometheusOptsType(typ)
		if !ok {
			return "", opts{}, false, nil
		}
		parsed, hasName, err := sp.extractPrometheusOpts(lit, rel)
		return metricType, parsed, hasName, err
	}
	return "", opts{}, false, nil
}

func (sp *scanPackage) entryFromOpts(metricType string, parsed opts, rel string, pos token.Pos) Entry {
	namespace := parsed.namespace
	if namespace == "" {
		namespace = sp.namespace
	}
	labels := mergeLabels(parsed.labels, parsed.constLabels)
	fqName := promFQName(namespace, parsed.subsystem, parsed.name)
	e := Entry{
		Name:         parsed.name,
		FQName:       fqName,
		Namespace:    namespace,
		Subsystem:    parsed.subsystem,
		Type:         metricType,
		Help:         parsed.help,
		Labels:       labels,
		ConstLabels:  sortedUnique(parsed.constLabels),
		Buckets:      parsed.buckets,
		BucketSource: parsed.bucketSource,
		File:         rel,
		Line:         sp.fset.Position(pos).Line,
	}
	if e.FQName == e.Name {
		e.FQName = ""
	}
	return e
}

func (sp *scanPackage) extractKernelOpts(lit *ast.CompositeLit, rel string) (opts, bool, error) {
	var out opts
	for _, elt := range lit.Elts {
		key, value, ok := keyValueField(elt)
		if !ok {
			continue
		}
		if err := sp.applyKernelOptField(&out, key, value, rel); err != nil {
			return out, false, err
		}
	}
	return out, out.name != "", nil
}

func (sp *scanPackage) extractPrometheusOpts(lit *ast.CompositeLit, rel string) (opts, bool, error) {
	var out opts
	for _, elt := range lit.Elts {
		key, value, ok := keyValueField(elt)
		if !ok {
			continue
		}
		if err := sp.applyPrometheusOptField(&out, key, value, rel); err != nil {
			return out, false, err
		}
	}
	return out, out.name != "", nil
}

func keyValueField(expr ast.Expr) (string, ast.Expr, bool) {
	kv, ok := expr.(*ast.KeyValueExpr)
	if !ok {
		return "", nil, false
	}
	key, ok := kv.Key.(*ast.Ident)
	if !ok {
		return "", nil, false
	}
	return key.Name, kv.Value, true
}

func (sp *scanPackage) applyKernelOptField(out *opts, key string, value ast.Expr, rel string) error {
	switch key {
	case "Name":
		name, ok := sp.string(value)
		if !ok {
			return sp.unresolved(value, rel, "metric name must be a compile-time string")
		}
		out.name = name
	case "Help":
		out.help, _ = sp.string(value)
	case "LabelNames":
		labels, err := sp.stringSlice(value, rel)
		if err != nil {
			return err
		}
		out.labels = labels
	case "Buckets":
		return sp.applyBucketField(out, value, rel)
	}
	return nil
}

func (sp *scanPackage) applyPrometheusOptField(out *opts, key string, value ast.Expr, rel string) error {
	switch key {
	case "Name":
		name, ok := sp.string(value)
		if !ok {
			return sp.unresolved(value, rel, "metric name must be a compile-time string")
		}
		out.name = name
	case "Namespace":
		namespace, ok := sp.string(value)
		if !ok {
			return sp.unresolved(value, rel, "metric namespace must be a compile-time string")
		}
		out.namespace = namespace
	case "Subsystem":
		subsystem, ok := sp.string(value)
		if !ok {
			return sp.unresolved(value, rel, "metric subsystem must be a compile-time string")
		}
		out.subsystem = subsystem
	case "Help":
		out.help, _ = sp.string(value)
	case "ConstLabels":
		labels, err := sp.constLabelNames(value, rel)
		if err != nil {
			return err
		}
		out.constLabels = labels
	case "Buckets":
		return sp.applyBucketField(out, value, rel)
	}
	return nil
}

func (sp *scanPackage) applyBucketField(out *opts, value ast.Expr, rel string) error {
	buckets, err := sp.numberSlice(value, rel)
	if err != nil {
		return err
	}
	out.buckets = buckets
	return nil
}

func (sp *scanPackage) string(expr ast.Expr) (string, bool) {
	if tv, ok := sp.pkg.TypesInfo.Types[expr]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
		return constant.StringVal(tv.Value), true
	}
	obj := sp.objectForExpr(expr)
	if s, ok := stringConstObject(obj); ok {
		return s, true
	}
	if init := sp.inits[obj]; init != nil && init != expr {
		return sp.string(init)
	}
	return "", false
}

func (sp *scanPackage) objectForExpr(expr ast.Expr) types.Object {
	switch e := expr.(type) {
	case *ast.Ident:
		if obj := sp.pkg.TypesInfo.Uses[e]; obj != nil {
			return obj
		}
		return sp.pkg.TypesInfo.Defs[e]
	case *ast.SelectorExpr:
		return sp.pkg.TypesInfo.Uses[e.Sel]
	}
	return nil
}

func stringConstObject(obj types.Object) (string, bool) {
	c, ok := obj.(*types.Const)
	if !ok || c.Val().Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(c.Val()), true
}

func (sp *scanPackage) stringSlice(expr ast.Expr, rel string) ([]string, error) {
	if lit := sp.resolveCompositeLit(expr); lit != nil {
		out := make([]string, 0, len(lit.Elts))
		for _, elt := range lit.Elts {
			s, ok := sp.string(elt)
			if !ok {
				return nil, sp.unresolved(elt, rel, "label names must be compile-time strings")
			}
			out = append(out, s)
		}
		return uniquePreserveOrder(out), nil
	}
	return nil, sp.unresolved(expr, rel, "label names must be a resolvable string slice")
}

func (sp *scanPackage) numberSlice(expr ast.Expr, rel string) ([]string, error) {
	if lit := sp.resolveCompositeLit(expr); lit != nil {
		out := make([]string, 0, len(lit.Elts))
		for _, elt := range lit.Elts {
			s, ok := sp.number(elt)
			if !ok {
				return nil, sp.unresolved(elt, rel, "bucket values must be numeric constants")
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, sp.unresolved(expr, rel, "bucket values must be a resolvable numeric slice")
}

func (sp *scanPackage) number(expr ast.Expr) (string, bool) {
	if bl, ok := expr.(*ast.BasicLit); ok && (bl.Kind == token.FLOAT || bl.Kind == token.INT) {
		return bl.Value, true
	}
	tv, ok := sp.pkg.TypesInfo.Types[expr]
	if !ok || tv.Value == nil {
		return "", false
	}
	switch tv.Value.Kind() {
	case constant.Int:
		return tv.Value.String(), true
	case constant.Float:
		f, exact := constant.Float64Val(tv.Value)
		if !exact {
			return tv.Value.String(), true
		}
		return strconv.FormatFloat(f, 'g', -1, 64), true
	}
	return "", false
}

func (sp *scanPackage) constLabelNames(expr ast.Expr, rel string) ([]string, error) {
	lit := sp.resolveCompositeLit(expr)
	if lit == nil {
		return nil, sp.unresolved(expr, rel, "const label names must be a resolvable map literal")
	}
	out := make([]string, 0, len(lit.Elts))
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := sp.string(kv.Key)
		if !ok {
			return nil, sp.unresolved(kv.Key, rel, "const label names must be compile-time strings")
		}
		out = append(out, key)
	}
	return sortedUnique(out), nil
}

func (sp *scanPackage) defaultNumberSlice(pkgPath, name, rel string) ([]string, error) {
	for obj, expr := range sp.inits {
		if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != pkgPath || obj.Name() != name {
			continue
		}
		return sp.numberSlice(expr, rel)
	}
	return nil, sp.unresolved(&ast.Ident{Name: name, NamePos: token.NoPos}, rel,
		fmt.Sprintf("default bucket values %s.%s are not resolvable", pkgPath, name))
}

func (sp *scanPackage) resolveCompositeLit(expr ast.Expr) *ast.CompositeLit {
	switch e := expr.(type) {
	case *ast.CompositeLit:
		return e
	case *ast.Ident:
		obj := sp.pkg.TypesInfo.Uses[e]
		if obj == nil {
			obj = sp.pkg.TypesInfo.Defs[e]
		}
		if init := sp.inits[obj]; init != nil && init != expr {
			return sp.resolveCompositeLit(init)
		}
	case *ast.SelectorExpr:
		if obj := sp.pkg.TypesInfo.Uses[e.Sel]; obj != nil {
			if init := sp.inits[obj]; init != nil && init != expr {
				return sp.resolveCompositeLit(init)
			}
		}
	}
	return nil
}

func (sp *scanPackage) unresolved(n ast.Node, rel, msg string) error {
	pos := sp.fset.Position(n.Pos())
	return fmt.Errorf("%w: %s:%d:%d: %s: %s",
		ErrUnresolvedMetricSchema, rel, pos.Line, pos.Column, msg, exprString(sp.fset, n))
}

func namedType(info *types.Info, expr ast.Expr) (string, string, bool) {
	t := info.TypeOf(expr)
	named, ok := t.(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return "", "", false
	}
	return named.Obj().Pkg().Path(), named.Obj().Name(), true
}

func kernelMetricType(typ string) (string, bool) {
	switch typ {
	case "CounterOpts":
		return "counter", true
	case "HistogramOpts":
		return "histogram", true
	}
	return "", false
}

func prometheusOptsType(typ string) (string, bool) {
	switch typ {
	case "CounterOpts":
		return "counter", true
	case "HistogramOpts":
		return "histogram", true
	case "GaugeOpts":
		return "gauge", true
	case "SummaryOpts":
		return "summary", true
	}
	return "", false
}

func prometheusConstructor(info *types.Info, call *ast.CallExpr) (metricType string, vec bool, ok bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false, false
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != prometheusPkg {
		return "", false, false
	}
	switch fn.Name() {
	case "NewCounter", "NewCounterFunc":
		return "counter", false, true
	case "NewCounterVec":
		return "counter", true, true
	case "NewHistogram", "NewHistogramFunc":
		return "histogram", false, true
	case "NewHistogramVec":
		return "histogram", true, true
	case "NewGauge", "NewGaugeFunc":
		return "gauge", false, true
	case "NewGaugeVec":
		return "gauge", true, true
	case "NewSummary":
		return "summary", false, true
	case "NewSummaryVec":
		return "summary", true, true
	}
	return "", false, false
}

func mergeLabels(a, b []string) []string {
	out := append([]string(nil), a...)
	out = append(out, b...)
	return uniquePreserveOrder(out)
}

func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	sort.Strings(in)
	out := in[:0]
	for _, s := range in {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return append([]string(nil), out...)
}

func uniquePreserveOrder(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func promFQName(namespace, subsystem, name string) string {
	parts := make([]string, 0, 3)
	if namespace != "" {
		parts = append(parts, namespace)
	}
	if subsystem != "" {
		parts = append(parts, subsystem)
	}
	parts = append(parts, name)
	return strings.Join(parts, "_")
}

func exprString(fset *token.FileSet, n any) string {
	var b bytes.Buffer
	if err := format.Node(&b, fset, n); err != nil {
		return ""
	}
	return b.String()
}

func collectOBS01MetricIdentities(
	root string,
	pkgs []*packages.Package,
	inits map[types.Object]ast.Expr,
	namespace string,
) map[types.Object]obs01MetricIdentity {
	out := map[types.Object]obs01MetricIdentity{}
	for _, p := range pkgs {
		sp := newScanPackage(root, p, namespace, inits, nil)
		for _, file := range p.Syntax {
			rel := sp.relForNode(file)
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.ValueSpec:
					sp.collectOBS01MetricIdentitiesFromValueSpec(out, node, rel)
				case *ast.AssignStmt:
					sp.collectOBS01MetricIdentitiesFromAssign(out, node, rel)
				}
				return true
			})
		}
	}
	return out
}

type obs01ReturnTaints map[string]map[int]bool

func obs01FuncKey(fn *types.Func) string {
	if fn == nil {
		return ""
	}
	return fn.FullName()
}

func collectOBS01ReturnTaints(pkgs []*packages.Package) obs01ReturnTaints {
	funcs, infos, objects := collectOBS01Funcs(pkgs)
	out := obs01ReturnTaints{}
	for changed := true; changed; {
		changed = updateOBS01ReturnTaints(funcs, infos, objects, out)
	}
	return out
}

func collectOBS01Funcs(pkgs []*packages.Package) (
	[]*ast.FuncDecl,
	map[*ast.FuncDecl]*types.Info,
	map[*ast.FuncDecl]*types.Func,
) {
	funcs := make([]*ast.FuncDecl, 0)
	infos := map[*ast.FuncDecl]*types.Info{}
	objects := map[*ast.FuncDecl]*types.Func{}
	for _, p := range pkgs {
		for _, file := range p.Syntax {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				obj, ok := p.TypesInfo.Defs[fn.Name].(*types.Func)
				if !ok {
					continue
				}
				funcs = append(funcs, fn)
				infos[fn] = p.TypesInfo
				objects[fn] = obj
			}
		}
	}
	return funcs, infos, objects
}

func updateOBS01ReturnTaints(
	funcs []*ast.FuncDecl,
	infos map[*ast.FuncDecl]*types.Info,
	objects map[*ast.FuncDecl]*types.Func,
	out obs01ReturnTaints,
) bool {
	changed := false
	for _, fn := range funcs {
		key := obs01FuncKey(objects[fn])
		if key == "" {
			continue
		}
		for index := range obs01FuncReturnTaints(infos[fn], fn, out) {
			if out[key] == nil {
				out[key] = map[int]bool{}
			}
			if out[key][index] {
				continue
			}
			out[key][index] = true
			changed = true
		}
	}
	return changed
}

func obs01FuncReturnTaints(info *types.Info, fn *ast.FuncDecl, returnTaints obs01ReturnTaints) map[int]bool {
	tainted := map[types.Object]bool{}
	out := map[int]bool{}
	walkOBS01Stmts(info, fn.Body.List, tainted, returnTaints, obs01StmtHandlers{
		onReturn: func(node *ast.ReturnStmt, state map[types.Object]bool) {
			for index := range obs01ReturnStmtTaints(info, fn, node, state, returnTaints) {
				out[index] = true
			}
		},
	})
	return out
}

func obs01ReturnStmtTaints(
	info *types.Info,
	fn *ast.FuncDecl,
	stmt *ast.ReturnStmt,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
) map[int]bool {
	out := map[int]bool{}
	if len(stmt.Results) > 0 {
		resultCount := obs01ResultCount(fn)
		if resultCount == 0 {
			resultCount = len(stmt.Results)
		}
		for i := 0; i < resultCount; i++ {
			if exprDependsOnErrcodeClassifierForPosition(info, stmt.Results, i, tainted, returnTaints) {
				out[i] = true
			}
		}
		return out
	}
	for i, obj := range namedResultObjects(info, fn) {
		if tainted[obj] {
			out[i] = true
		}
	}
	return out
}

func obs01ResultCount(fn *ast.FuncDecl) int {
	if fn.Type.Results == nil {
		return 0
	}
	count := 0
	for _, field := range fn.Type.Results.List {
		if len(field.Names) == 0 {
			count++
			continue
		}
		count += len(field.Names)
	}
	return count
}

func namedResultObjects(info *types.Info, fn *ast.FuncDecl) []types.Object {
	if fn.Type.Results == nil {
		return nil
	}
	var out []types.Object
	for _, field := range fn.Type.Results.List {
		for _, name := range field.Names {
			if obj := info.Defs[name]; obj != nil {
				out = append(out, obj)
			}
		}
	}
	return out
}

func (sp *scanPackage) collectOBS01MetricIdentitiesFromValueSpec(
	out map[types.Object]obs01MetricIdentity,
	spec *ast.ValueSpec,
	rel string,
) {
	for i, name := range spec.Names {
		if i >= len(spec.Values) {
			continue
		}
		identity, ok := sp.metricIdentityFromCall(spec.Values[i], rel)
		if !ok {
			continue
		}
		if obj := sp.pkg.TypesInfo.Defs[name]; obj != nil {
			out[obj] = identity
		}
	}
}

func (sp *scanPackage) collectOBS01MetricIdentitiesFromAssign(
	out map[types.Object]obs01MetricIdentity,
	stmt *ast.AssignStmt,
	rel string,
) {
	for i, left := range stmt.Lhs {
		if i >= len(stmt.Rhs) {
			continue
		}
		identity, ok := sp.metricIdentityFromCall(stmt.Rhs[i], rel)
		if !ok {
			continue
		}
		if obj := objectForOBS01Expr(sp.pkg.TypesInfo, left); obj != nil {
			out[obj] = identity
		}
	}
}

func (sp *scanPackage) metricIdentityFromCall(expr ast.Expr, rel string) (obs01MetricIdentity, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return obs01MetricIdentity{}, false
	}
	fn := calledFunc(sp.pkg.TypesInfo, call)
	if fn == nil || fn.Pkg() == nil {
		return obs01MetricIdentity{}, false
	}
	switch {
	case fn.Pkg().Path() == kernelMetricsPkg && (fn.Name() == "CounterVec" || fn.Name() == "HistogramVec"):
		return sp.kernelMetricIdentity(call.Args[0], rel)
	case fn.Pkg().Path() == prometheusPkg:
		_, vec, ok := prometheusConstructor(sp.pkg.TypesInfo, call)
		if !ok || !vec {
			return obs01MetricIdentity{}, false
		}
		return sp.prometheusMetricIdentity(call, rel)
	}
	return obs01MetricIdentity{}, false
}

func (sp *scanPackage) kernelMetricIdentity(expr ast.Expr, rel string) (obs01MetricIdentity, bool) {
	lit := sp.resolveCompositeLit(expr)
	if lit == nil {
		return obs01MetricIdentity{}, false
	}
	parsed, hasName, err := sp.extractKernelOpts(lit, rel)
	if err != nil || !hasName {
		return obs01MetricIdentity{}, false
	}
	name := promFQName(sp.namespace, "", parsed.name)
	return obs01MetricIdentity{Metric: name, Labels: parsed.labels, Resolved: true}, true
}

func (sp *scanPackage) prometheusMetricIdentity(call *ast.CallExpr, rel string) (obs01MetricIdentity, bool) {
	lit := sp.resolveCompositeLit(call.Args[0])
	if lit == nil {
		return obs01MetricIdentity{}, false
	}
	parsed, hasName, err := sp.extractPrometheusOpts(lit, rel)
	if err != nil || !hasName {
		return obs01MetricIdentity{}, false
	}
	labels, err := sp.prometheusVecLabels(call, rel)
	if err != nil {
		return obs01MetricIdentity{}, false
	}
	return obs01MetricIdentity{
		Metric:   promFQName(parsed.namespace, parsed.subsystem, parsed.name),
		Labels:   labels,
		Resolved: true,
	}, true
}

// CheckOBS01 reports production metric label values whose expression depends on
// errcode.Category or errcode.IsInfraError without a checked-in acknowledgement.
func CheckOBS01(projectRoot string) ([]Diagnostic, error) {
	return checkOBS01WithPatterns(projectRoot, obs01ProductionPatterns(projectRoot)...)
}

func checkOBS01WithPatterns(projectRoot string, patterns ...string) ([]Diagnostic, error) {
	if len(patterns) == 0 {
		patterns = obs01ProductionPatterns(projectRoot)
	}
	acks, err := loadOBS01Acks(projectRoot)
	if err != nil {
		return nil, err
	}
	matchedAcks := map[string]bool{}
	pkgs, err := loadPackages(projectRoot, patterns...)
	if err != nil {
		return nil, err
	}
	inits := collectInits(pkgs)
	namespace, err := prometheusProviderNamespace(projectRoot, pkgs, inits)
	if err != nil {
		return nil, err
	}
	metricIdentities := collectOBS01MetricIdentities(projectRoot, pkgs, inits, namespace)
	returnTaints := collectOBS01ReturnTaints(pkgs)
	sinkParams := collectOBS01SinkParams(pkgs, metricIdentities)
	var diagnostics []Diagnostic
	for _, p := range pkgs {
		pkgDiagnostics, scanErr := scanOBS01Package(
			projectRoot,
			p,
			acks,
			matchedAcks,
			sinkParams,
			metricIdentities,
			returnTaints,
		)
		if scanErr != nil {
			return nil, scanErr
		}
		diagnostics = append(diagnostics, pkgDiagnostics...)
	}
	sort.Slice(diagnostics, func(i, j int) bool {
		if diagnostics[i].File != diagnostics[j].File {
			return diagnostics[i].File < diagnostics[j].File
		}
		if diagnostics[i].Line != diagnostics[j].Line {
			return diagnostics[i].Line < diagnostics[j].Line
		}
		return diagnostics[i].Column < diagnostics[j].Column
	})
	diagnostics = dedupeDiagnostics(diagnostics)
	if err := rejectUnusedOBS01Acks(projectRoot, acks, matchedAcks); err != nil {
		return diagnostics, err
	}
	return diagnostics, nil
}

func obs01ProductionPatterns(projectRoot string) []string {
	return prodscan.Patterns(projectRoot)
}

func dedupeDiagnostics(in []Diagnostic) []Diagnostic {
	seen := map[Diagnostic]bool{}
	out := make([]Diagnostic, 0, len(in))
	for _, diagnostic := range in {
		if seen[diagnostic] {
			continue
		}
		seen[diagnostic] = true
		out = append(out, diagnostic)
	}
	return out
}

type obs01SinkArg struct {
	Expr          ast.Expr
	ValueIndex    int
	Variadic      bool
	VariadicIndex int
	Metric        string
	Label         string
	Ackable       bool
}

type obs01SinkBinding struct {
	Variadic      bool
	VariadicIndex int
	Metric        string
	Label         string
	Ackable       bool
}

func scanOBS01Package(
	root string,
	p *packages.Package,
	acks map[string]obsAck,
	matchedAcks map[string]bool,
	sinkParams map[string]map[int][]obs01SinkBinding,
	metricIdentities map[types.Object]obs01MetricIdentity,
	returnTaints obs01ReturnTaints,
) ([]Diagnostic, error) {
	var diagnostics []Diagnostic
	for _, file := range p.Syntax {
		path := p.Fset.Position(file.Pos()).Filename
		if !isProjectGoFile(root, path) {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		fileDiagnostics := scanOBS01File(
			p,
			file,
			filepath.ToSlash(rel),
			acks,
			matchedAcks,
			sinkParams,
			metricIdentities,
			returnTaints,
		)
		diagnostics = append(diagnostics, fileDiagnostics...)
	}
	return diagnostics, nil
}

func scanOBS01File(
	p *packages.Package,
	file *ast.File,
	rel string,
	acks map[string]obsAck,
	matchedAcks map[string]bool,
	sinkParams map[string]map[int][]obs01SinkBinding,
	metricIdentities map[types.Object]obs01MetricIdentity,
	returnTaints obs01ReturnTaints,
) []Diagnostic {
	var diagnostics []Diagnostic
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		diagnostics = append(diagnostics,
			scanOBS01Func(p, fn, rel, acks, matchedAcks, sinkParams, metricIdentities, returnTaints)...)
	}
	return diagnostics
}

func scanOBS01Func(
	p *packages.Package,
	fn *ast.FuncDecl,
	rel string,
	acks map[string]obsAck,
	matchedAcks map[string]bool,
	sinkParams map[string]map[int][]obs01SinkBinding,
	metricIdentities map[types.Object]obs01MetricIdentity,
	returnTaints obs01ReturnTaints,
) []Diagnostic {
	var diagnostics []Diagnostic
	tainted := map[types.Object]bool{}
	walkOBS01Stmts(p.TypesInfo, fn.Body.List, tainted, returnTaints, obs01StmtHandlers{
		onCall: func(node *ast.CallExpr, state map[types.Object]bool) {
			diagnostics = append(diagnostics,
				obs01DiagnosticsForCall(p, node, rel, acks, matchedAcks, state, sinkParams, metricIdentities, returnTaints)...)
		},
	})
	return diagnostics
}

func obs01DiagnosticsForCall(
	p *packages.Package,
	call *ast.CallExpr,
	rel string,
	acks map[string]obsAck,
	matchedAcks map[string]bool,
	tainted map[types.Object]bool,
	sinkParams map[string]map[int][]obs01SinkBinding,
	metricIdentities map[types.Object]obs01MetricIdentity,
	returnTaints obs01ReturnTaints,
) []Diagnostic {
	var diagnostics []Diagnostic
	for _, sink := range obs01FuncLitVariadicSpreadSinkArgs(p.TypesInfo, p.Fset, call, metricIdentities) {
		if exprDependsOnErrcodeClassifierForValue(p.TypesInfo, sink.Expr, sink.ValueIndex, tainted, returnTaints) {
			diagnostics = appendOBS01DiagnosticIfUnacked(diagnostics, p, sink, rel, acks, matchedAcks)
		}
	}
	for _, sink := range metricLabelBindingArgs(p.TypesInfo, p.Fset, call, metricIdentities) {
		if exprDependsOnErrcodeClassifierForValue(p.TypesInfo, sink.Expr, sink.ValueIndex, tainted, returnTaints) {
			diagnostics = appendOBS01DiagnosticIfUnacked(diagnostics, p, sink, rel, acks, matchedAcks)
		}
	}
	for _, sink := range obs01SinkArgs(p.TypesInfo, call, sinkParams) {
		if exprDependsOnErrcodeClassifierForValue(p.TypesInfo, sink.Expr, sink.ValueIndex, tainted, returnTaints) {
			diagnostics = appendOBS01DiagnosticIfUnacked(diagnostics, p, sink, rel, acks, matchedAcks)
		}
	}
	return diagnostics
}

func appendOBS01DiagnosticIfUnacked(
	out []Diagnostic,
	p *packages.Package,
	sink obs01SinkArg,
	rel string,
	acks map[string]obsAck,
	matchedAcks map[string]bool,
) []Diagnostic {
	pos := p.Fset.Position(sink.Expr.Pos())
	fingerprint := obs01Fingerprint(rel, pos.Line, pos.Column, sink.Metric, sink.Label, exprString(p.Fset, sink.Expr))
	if sink.Ackable {
		if ack, ok := acks[fingerprint]; ok && ack.matches(sink) {
			matchedAcks[fingerprint] = true
			return out
		}
	}
	return append(out, obs01Diagnostic(p, sink, rel, fingerprint))
}

func obs01Diagnostic(p *packages.Package, sink obs01SinkArg, rel, fingerprint string) Diagnostic {
	pos := p.Fset.Position(sink.Expr.Pos())
	return Diagnostic{
		Rule:        "OBS-01",
		File:        rel,
		Line:        pos.Line,
		Column:      pos.Column,
		Metric:      sink.Metric,
		Label:       sink.Label,
		Fingerprint: fingerprint,
		Message:     obs01DiagnosticMessage(sink, fingerprint),
	}
}

func obs01DiagnosticMessage(sink obs01SinkArg, fingerprint string) string {
	if !sink.Ackable {
		return fmt.Sprintf("metric label value depends on errcode.Category or errcode.IsInfraError, "+
			"but the metric/label identity is not machine-resolvable (metric=%q label=%q fingerprint=%s); "+
			"use metrics.Labels with a metric vector constructed from literal opts before adding an acknowledgement",
			sink.Metric, sink.Label, fingerprint)
	}
	return fmt.Sprintf("metric label value depends on errcode.Category or errcode.IsInfraError"+
		" (metric=%q label=%q fingerprint=%s);"+
		" add a checked-in docs/observability/metrics-migration-acks.yaml entry"+
		" with this fingerprint after documenting the dashboard/alert migration",
		sink.Metric, sink.Label, fingerprint)
}

func metricLabelBindingArgs(
	info *types.Info,
	fset *token.FileSet,
	call *ast.CallExpr,
	metricIdentities map[types.Object]obs01MetricIdentity,
) []obs01SinkArg {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	metric := obs01MetricSink(info, fset, sel.X, metricIdentities)
	if sel.Sel.Name == "WithLabelValues" {
		return obs01WithLabelValuesArgs(info, metric, call)
	}
	return obs01WithLabelsArgs(info, fset, metric, sel.Sel.Name, call.Args)
}

func obs01FuncLitVariadicSpreadSinkArgs(
	info *types.Info,
	fset *token.FileSet,
	call *ast.CallExpr,
	metricIdentities map[types.Object]obs01MetricIdentity,
) []obs01SinkArg {
	fn, ok := unparenExpr(call.Fun).(*ast.FuncLit)
	if !ok {
		return nil
	}
	variadicObj, paramIndex, ok := obs01FuncLitVariadicParam(info, fn.Type)
	if !ok {
		return nil
	}
	var out []obs01SinkArg
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if nested, ok := n.(*ast.FuncLit); ok && nested != fn {
			return false
		}
		bodyCall, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		metricExpr, ok := obs01VariadicSpreadLabelValueCall(info, bodyCall, variadicObj)
		if !ok {
			return true
		}
		metric := obs01MetricSink(info, fset, metricExpr, metricIdentities)
		for i, label := range metric.Labels {
			out = append(out, obs01SinkArgsForBindingWithVariadic(info, true, call, paramIndex, obs01SinkBinding{
				Variadic:      true,
				VariadicIndex: i,
				Metric:        metric.Metric,
				Label:         label,
				Ackable:       metric.Resolved,
			})...)
		}
		return true
	})
	return out
}

func obs01FuncLitVariadicParam(info *types.Info, typ *ast.FuncType) (types.Object, int, bool) {
	if !obs01FuncTypeVariadic(typ) {
		return nil, 0, false
	}
	params := functionParamObjects(info, typ)
	if len(params) == 0 {
		return nil, 0, false
	}
	obj := params[len(params)-1]
	idx, ok := functionParamIndexes(info, typ)[obj]
	return obj, idx, ok
}

func obs01VariadicSpreadLabelValueCall(
	info *types.Info,
	call *ast.CallExpr,
	variadicObj types.Object,
) (ast.Expr, bool) {
	if !call.Ellipsis.IsValid() || len(call.Args) != 1 {
		return nil, false
	}
	if objectForOBS01Expr(info, call.Args[0]) != variadicObj {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "WithLabelValues" {
		return nil, false
	}
	return sel.X, true
}

func obs01WithLabelValuesArgs(info *types.Info, metric obs01MetricIdentity, call *ast.CallExpr) []obs01SinkArg {
	if call.Ellipsis.IsValid() && len(call.Args) == 1 {
		return obs01SpreadLabelValueArgs(metric, call.Args[0])
	}
	valueCount := obs01ValueExprsCount(info, call.Args)
	out := make([]obs01SinkArg, 0, valueCount)
	for i := range valueCount {
		if value, ok := obs01ValueExprForPosition(info, call.Args, i); ok {
			out = append(out, obs01PositionalLabelArg(metric, value, i))
		}
	}
	return out
}

func obs01PositionalLabelArg(metric obs01MetricIdentity, value obs01ValueExpr, index int) obs01SinkArg {
	label := fmt.Sprintf("arg%d", index+1)
	ackable := false
	if index < len(metric.Labels) {
		label = metric.Labels[index]
		ackable = metric.Resolved
	}
	return obs01SinkArg{
		Expr:          value.Expr,
		ValueIndex:    value.Index,
		VariadicIndex: -1,
		Metric:        metric.Metric,
		Label:         label,
		Ackable:       ackable,
	}
}

func obs01WithLabelsArgs(
	info *types.Info,
	fset *token.FileSet,
	metric obs01MetricIdentity,
	method string,
	args []ast.Expr,
) []obs01SinkArg {
	if method != "With" || len(args) != 1 {
		return nil
	}
	pkgPath, typ, ok := namedType(info, args[0])
	if !ok || typ != "Labels" || (pkgPath != kernelMetricsPkg && pkgPath != prometheusPkg) {
		return nil
	}
	return obs01LabelsArgs(info, fset, metric.Metric, metric.Resolved, args[0])
}

func obs01LabelsArgs(info *types.Info, fset *token.FileSet, metric string, ackableMetric bool, expr ast.Expr) []obs01SinkArg {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return []obs01SinkArg{{
			Expr:          expr,
			ValueIndex:    0,
			VariadicIndex: -1,
			Metric:        metric,
			Label:         "<labels>",
			Ackable:       false,
		}}
	}
	out := make([]obs01SinkArg, 0, len(lit.Elts))
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		out = append(out, obs01SinkArg{
			Expr:          kv.Value,
			ValueIndex:    0,
			VariadicIndex: -1,
			Metric:        metric,
			Label:         obs01LabelKey(info, fset, kv.Key),
			Ackable:       ackableMetric && obs01ConstantStringKey(info, kv.Key),
		})
	}
	if len(out) == 0 {
		return []obs01SinkArg{{
			Expr:          expr,
			ValueIndex:    0,
			VariadicIndex: -1,
			Metric:        metric,
			Label:         "<labels>",
			Ackable:       false,
		}}
	}
	return out
}

func obs01SpreadLabelValueArgs(metric obs01MetricIdentity, expr ast.Expr) []obs01SinkArg {
	if lit, ok := unparenExpr(expr).(*ast.CompositeLit); ok {
		return obs01CompositeSpreadLabelValueArgs(metric, lit)
	}
	return []obs01SinkArg{{
		Expr:          expr,
		ValueIndex:    0,
		VariadicIndex: -1,
		Metric:        metric.Metric,
		Label:         "<labelValues>",
		Ackable:       false,
	}}
}

func obs01CompositeSpreadLabelValueArgs(metric obs01MetricIdentity, lit *ast.CompositeLit) []obs01SinkArg {
	out := make([]obs01SinkArg, 0, len(lit.Elts))
	for i, elt := range lit.Elts {
		value := elt
		labelIndex := i
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			value = kv.Value
			if idx, ok := obs01IntegerIndex(kv.Key); ok {
				labelIndex = idx
			}
		}
		label := fmt.Sprintf("arg%d", labelIndex+1)
		ackable := false
		if labelIndex >= 0 && labelIndex < len(metric.Labels) {
			label = metric.Labels[labelIndex]
			ackable = metric.Resolved
		}
		out = append(out, obs01SinkArg{
			Expr:          value,
			ValueIndex:    0,
			VariadicIndex: -1,
			Metric:        metric.Metric,
			Label:         label,
			Ackable:       ackable,
		})
	}
	return out
}

func obs01IntegerIndex(expr ast.Expr) (int, bool) {
	lit, ok := unparenExpr(expr).(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, false
	}
	idx, err := strconv.Atoi(lit.Value)
	if err != nil || idx < 0 {
		return 0, false
	}
	return idx, true
}

func obs01MetricSink(
	info *types.Info,
	fset *token.FileSet,
	expr ast.Expr,
	metricIdentities map[types.Object]obs01MetricIdentity,
) obs01MetricIdentity {
	if identity, ok := metricIdentities[objectForOBS01Expr(info, expr)]; ok {
		return identity
	}
	return obs01MetricIdentity{Metric: exprString(fset, expr), Resolved: false}
}

func obs01LabelKey(info *types.Info, fset *token.FileSet, expr ast.Expr) string {
	if s, ok := obs01ConstantString(info, expr); ok {
		return s
	}
	if s := exprString(fset, expr); s != "" {
		return s
	}
	return "<label>"
}

func obs01ConstantStringKey(info *types.Info, expr ast.Expr) bool {
	if s, ok := obs01ConstantString(info, expr); ok {
		return s != ""
	}
	return false
}

func obs01ConstantString(info *types.Info, expr ast.Expr) (string, bool) {
	if tv, ok := info.Types[expr]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
		return constant.StringVal(tv.Value), true
	}
	if s, ok := stringConstObject(objectForOBS01Expr(info, expr)); ok {
		return s, true
	}
	return "", false
}

func collectOBS01SinkParams(
	pkgs []*packages.Package,
	metricIdentities map[types.Object]obs01MetricIdentity,
) map[string]map[int][]obs01SinkBinding {
	out := map[string]map[int][]obs01SinkBinding{}
	funcs := collectOBS01SinkParamFuncs(pkgs)
	for changed := true; changed; {
		changed = false
		for _, fn := range funcs {
			if collectOBS01SinkParamsForFunc(fn, out, metricIdentities) {
				changed = true
			}
		}
	}
	return out
}

type obs01SinkParamFunc struct {
	info   *types.Info
	fset   *token.FileSet
	decl   *ast.FuncDecl
	obj    *types.Func
	params map[types.Object]int
}

func collectOBS01SinkParamFuncs(pkgs []*packages.Package) []obs01SinkParamFunc {
	var out []obs01SinkParamFunc
	for _, p := range pkgs {
		for _, file := range p.Syntax {
			for _, decl := range file.Decls {
				if fn, ok := obs01SinkParamFuncForDecl(p, decl); ok {
					out = append(out, fn)
				}
			}
		}
	}
	return out
}

func obs01SinkParamFuncForDecl(
	p *packages.Package,
	decl ast.Decl,
) (obs01SinkParamFunc, bool) {
	fn, ok := decl.(*ast.FuncDecl)
	if !ok || fn.Body == nil {
		return obs01SinkParamFunc{}, false
	}
	obj, ok := p.TypesInfo.Defs[fn.Name].(*types.Func)
	if !ok {
		return obs01SinkParamFunc{}, false
	}
	params := functionParamIndexes(p.TypesInfo, fn.Type)
	if len(params) == 0 {
		return obs01SinkParamFunc{}, false
	}
	return obs01SinkParamFunc{
		info:   p.TypesInfo,
		fset:   p.Fset,
		decl:   fn,
		obj:    obj,
		params: params,
	}, true
}

func collectOBS01SinkParamsForFunc(
	fn obs01SinkParamFunc,
	out map[string]map[int][]obs01SinkBinding,
	metricIdentities map[types.Object]obs01MetricIdentity,
) bool {
	changed := false
	walkOBS01Stmts(fn.info, fn.decl.Body.List, map[types.Object]bool{}, obs01ReturnTaints{}, obs01StmtHandlers{
		onCall: func(call *ast.CallExpr, _ map[types.Object]bool) {
			if recordOBS01SinkParamDeps(fn.info, fn.fset, out, fn.obj, call, fn.params, metricIdentities) {
				changed = true
			}
		},
	})
	return changed
}

func recordOBS01SinkParamDeps(
	info *types.Info,
	fset *token.FileSet,
	out map[string]map[int][]obs01SinkBinding,
	fn *types.Func,
	call *ast.CallExpr,
	params map[types.Object]int,
	metricIdentities map[types.Object]obs01MetricIdentity,
) bool {
	changed := false
	recordedVariadic, variadicChanged := recordOBS01VariadicLabelValueParamDeps(info, fset, out, fn, call, params, metricIdentities)
	changed = recordOBS01SinkParamBindings(
		info,
		out,
		fn,
		params,
		metricLabelBindingArgs(info, fset, call, metricIdentities),
		recordedVariadic,
	) || changed
	if variadicChanged {
		changed = true
	}
	return recordOBS01SinkParamBindings(info, out, fn, params, obs01SinkArgs(info, call, out), false) || changed
}

func recordOBS01SinkParamBindings(
	info *types.Info,
	out map[string]map[int][]obs01SinkBinding,
	fn *types.Func,
	params map[types.Object]int,
	sinks []obs01SinkArg,
	skipGenericLabelValues bool,
) bool {
	changed := false
	for _, sink := range sinks {
		if skipGenericLabelValues && sink.Label == "<labelValues>" {
			continue
		}
		for idx := range obs01ParamDeps(info, sink.Expr, params) {
			if recordOBS01SinkBinding(out, fn, idx, obs01SinkBinding{
				Variadic:      sink.Variadic,
				VariadicIndex: sink.VariadicIndex,
				Metric:        sink.Metric,
				Label:         sink.Label,
				Ackable:       sink.Ackable,
			}) {
				changed = true
			}
		}
	}
	return changed
}

func recordOBS01VariadicLabelValueParamDeps(
	info *types.Info,
	fset *token.FileSet,
	out map[string]map[int][]obs01SinkBinding,
	fn *types.Func,
	call *ast.CallExpr,
	params map[types.Object]int,
	metricIdentities map[types.Object]obs01MetricIdentity,
) (bool, bool) {
	if !call.Ellipsis.IsValid() || len(call.Args) != 1 {
		return false, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "WithLabelValues" {
		return false, false
	}
	paramIndexes := obs01ParamDeps(info, call.Args[0], params)
	if len(paramIndexes) == 0 {
		return false, false
	}
	metric := obs01MetricSink(info, fset, sel.X, metricIdentities)
	if len(metric.Labels) == 0 {
		return false, false
	}
	changed := false
	for idx := range paramIndexes {
		for i, label := range metric.Labels {
			if recordOBS01SinkBinding(out, fn, idx, obs01SinkBinding{
				Variadic:      true,
				VariadicIndex: i,
				Metric:        metric.Metric,
				Label:         label,
				Ackable:       metric.Resolved,
			}) {
				changed = true
			}
		}
	}
	return true, changed
}

func recordOBS01SinkBinding(
	out map[string]map[int][]obs01SinkBinding,
	fn *types.Func,
	idx int,
	binding obs01SinkBinding,
) bool {
	key := obs01FuncKey(fn)
	if key == "" {
		return false
	}
	if out[key] == nil {
		out[key] = map[int][]obs01SinkBinding{}
	}
	if slices.Contains(out[key][idx], binding) {
		return false
	}
	out[key][idx] = append(out[key][idx], binding)
	return true
}

func functionParamIndexes(info *types.Info, typ *ast.FuncType) map[types.Object]int {
	out := map[types.Object]int{}
	if typ.Params == nil {
		return out
	}
	idx := 0
	for _, field := range typ.Params.List {
		for _, name := range field.Names {
			if obj := info.Defs[name]; obj != nil {
				out[obj] = idx
			}
			idx++
		}
		if len(field.Names) == 0 {
			idx++
		}
	}
	return out
}

func functionParamObjects(info *types.Info, typ *ast.FuncType) []types.Object {
	if typ.Params == nil {
		return nil
	}
	var out []types.Object
	for _, field := range typ.Params.List {
		for _, name := range field.Names {
			if obj := info.Defs[name]; obj != nil {
				out = append(out, obj)
			}
		}
	}
	return out
}

func functionParamSet(info *types.Info, typ *ast.FuncType) map[types.Object]bool {
	out := map[types.Object]bool{}
	if typ.Params == nil {
		return out
	}
	for _, field := range typ.Params.List {
		for _, name := range field.Names {
			if obj := info.Defs[name]; obj != nil {
				out[obj] = true
			}
		}
	}
	return out
}

func obs01ParamDeps(info *types.Info, expr ast.Expr, params map[types.Object]int) map[int]bool {
	out := map[int]bool{}
	ast.Inspect(expr, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if idx, ok := params[objectForOBS01Expr(info, id)]; ok {
			out[idx] = true
		}
		return true
	})
	return out
}

func obs01SinkArgs(
	info *types.Info,
	call *ast.CallExpr,
	sinkParams map[string]map[int][]obs01SinkBinding,
) []obs01SinkArg {
	fn := calledFunc(info, call)
	key := obs01FuncKey(fn)
	if key == "" || len(sinkParams[key]) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(sinkParams[key]))
	for idx := range sinkParams[key] {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)
	var out []obs01SinkArg
	for _, idx := range indexes {
		for _, binding := range sinkParams[key][idx] {
			out = append(out, obs01SinkArgsForBinding(info, fn, call, idx, binding)...)
		}
	}
	return out
}

func obs01SinkArgsForBinding(
	info *types.Info,
	fn *types.Func,
	call *ast.CallExpr,
	idx int,
	binding obs01SinkBinding,
) []obs01SinkArg {
	return obs01SinkArgsForBindingWithVariadic(info, obs01FuncVariadic(fn), call, idx, binding)
}

func obs01SinkArgsForBindingWithVariadic(
	info *types.Info,
	variadic bool,
	call *ast.CallExpr,
	idx int,
	binding obs01SinkBinding,
) []obs01SinkArg {
	if call.Ellipsis.IsValid() && binding.Variadic {
		return obs01SpreadSinkArgForBinding(call, idx, binding)
	}
	valuePosition := idx
	if binding.Variadic && variadic && binding.VariadicIndex >= 0 {
		valuePosition = idx + binding.VariadicIndex
	}
	value, ok := obs01ValueExprForPosition(info, call.Args, valuePosition)
	if !ok {
		return nil
	}
	return []obs01SinkArg{{
		Expr:          value.Expr,
		ValueIndex:    value.Index,
		Variadic:      binding.Variadic,
		VariadicIndex: binding.VariadicIndex,
		Metric:        binding.Metric,
		Label:         binding.Label,
		Ackable:       binding.Ackable,
	}}
}

func obs01SpreadSinkArgForBinding(call *ast.CallExpr, idx int, binding obs01SinkBinding) []obs01SinkArg {
	if idx >= len(call.Args) {
		return nil
	}
	if lit, ok := unparenExpr(call.Args[idx]).(*ast.CompositeLit); ok {
		value, ok := obs01CompositeValueAtIndex(lit, binding.VariadicIndex)
		if !ok {
			return nil
		}
		return []obs01SinkArg{{
			Expr:          value,
			ValueIndex:    0,
			Variadic:      true,
			VariadicIndex: binding.VariadicIndex,
			Metric:        binding.Metric,
			Label:         binding.Label,
			Ackable:       binding.Ackable,
		}}
	}
	if binding.VariadicIndex != 0 {
		return nil
	}
	return []obs01SinkArg{{
		Expr:          call.Args[idx],
		ValueIndex:    0,
		VariadicIndex: -1,
		Metric:        binding.Metric,
		Label:         "<labelValues>",
		Ackable:       false,
	}}
}

func obs01CompositeValueAtIndex(lit *ast.CompositeLit, index int) (ast.Expr, bool) {
	if index < 0 {
		return nil, false
	}
	nextIndex := 0
	for _, elt := range lit.Elts {
		value := elt
		valueIndex := nextIndex
		nextIndex++
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			value = kv.Value
			keyIndex, ok := obs01IntegerIndex(kv.Key)
			if !ok {
				continue
			}
			valueIndex = keyIndex
			if keyIndex >= nextIndex {
				nextIndex = keyIndex + 1
			}
		}
		if valueIndex == index {
			return value, true
		}
	}
	return nil, false
}

func obs01FuncVariadic(fn *types.Func) bool {
	if fn == nil {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	return ok && sig.Variadic()
}

func calledFunc(info *types.Info, call *ast.CallExpr) *types.Func {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		fn, _ := info.Uses[fun].(*types.Func)
		return fn
	case *ast.SelectorExpr:
		if sel := info.Selections[fun]; sel != nil {
			fn, _ := sel.Obj().(*types.Func)
			return fn
		}
		fn, _ := info.Uses[fun.Sel].(*types.Func)
		return fn
	}
	return nil
}

type obs01StmtHandlers struct {
	onCall         func(*ast.CallExpr, map[types.Object]bool)
	onReturn       func(*ast.ReturnStmt, map[types.Object]bool)
	closures       obs01Closures
	activeClosures map[*ast.FuncLit]bool
	rangeTaints    map[types.Object]obs01RangeTaint
	suppressCalls  map[*ast.CallExpr]bool
}

type obs01Closures map[types.Object]map[*ast.FuncLit]bool

type obs01RangeTaint struct {
	Key   bool
	Value bool
}

type obs01Flow struct {
	tainted       map[types.Object]bool
	continues     bool
	breaks        []map[types.Object]bool
	continuesLoop []map[types.Object]bool
}

func walkOBS01Stmts(
	info *types.Info,
	stmts []ast.Stmt,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) obs01Flow {
	if handlers.closures == nil {
		handlers.closures = obs01Closures{}
	}
	if handlers.activeClosures == nil {
		handlers.activeClosures = map[*ast.FuncLit]bool{}
	}
	if handlers.rangeTaints == nil {
		handlers.rangeTaints = map[types.Object]obs01RangeTaint{}
	}
	if handlers.suppressCalls == nil {
		handlers.suppressCalls = map[*ast.CallExpr]bool{}
	}
	flow := obs01Flow{tainted: tainted, continues: true}
	for _, stmt := range stmts {
		if !flow.continues {
			break
		}
		flow = walkOBS01Stmt(info, stmt, flow.tainted, returnTaints, handlers)
	}
	return flow
}

func walkOBS01Stmt(
	info *types.Info,
	stmt ast.Stmt,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) obs01Flow {
	flow := obs01Flow{tainted: tainted, continues: true}
	switch node := stmt.(type) {
	case *ast.AssignStmt:
		walkOBS01AssignStmt(info, node, tainted, returnTaints, handlers)
	case *ast.DeclStmt:
		walkOBS01Decl(info, node.Decl, tainted, returnTaints, handlers)
	case *ast.ReturnStmt:
		flow = walkOBS01ReturnStmt(info, node, tainted, returnTaints, handlers)
	case *ast.IfStmt:
		flow = walkOBS01IfStmt(info, node, tainted, returnTaints, handlers)
	case *ast.ForStmt:
		flow = walkOBS01ForStmt(info, node, tainted, returnTaints, handlers)
	case *ast.RangeStmt:
		flow = walkOBS01RangeStmt(info, node, tainted, returnTaints, handlers)
	case *ast.SwitchStmt:
		flow = walkOBS01SwitchStmt(info, node, tainted, returnTaints, handlers)
	case *ast.TypeSwitchStmt:
		flow = walkOBS01TypeSwitchStmt(info, node, tainted, returnTaints, handlers)
	case *ast.SelectStmt:
		flow = walkOBS01SelectStmt(info, node, tainted, returnTaints, handlers)
	case *ast.BlockStmt:
		flow = walkOBS01Stmts(info, node.List, tainted, returnTaints, handlers)
	case *ast.LabeledStmt:
		flow = walkOBS01Stmt(info, node.Stmt, tainted, returnTaints, handlers)
	case *ast.BranchStmt:
		flow = walkOBS01BranchStmt(node, tainted)
	default:
		inspectOBS01Calls(info, node, tainted, returnTaints, handlers)
	}
	return flow
}

func walkOBS01AssignStmt(
	info *types.Info,
	node *ast.AssignStmt,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) {
	markOBS01AssignedClosure(info, node.Lhs, node.Rhs, node.Tok, handlers.closures)
	markOBS01AssignedRangeTaint(info, node.Lhs, node.Rhs, node.Tok, tainted, returnTaints, handlers.rangeTaints)
	markOBS01AssignedTaint(info, node.Lhs, node.Rhs, node.Tok, tainted, returnTaints)
	inspectOBS01Calls(info, node, tainted, returnTaints, handlers)
}

func walkOBS01ReturnStmt(
	info *types.Info,
	node *ast.ReturnStmt,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) obs01Flow {
	if handlers.onReturn != nil {
		handlers.onReturn(node, tainted)
	}
	inspectOBS01Calls(info, node, tainted, returnTaints, handlers)
	return obs01Flow{tainted: tainted, continues: false}
}

func walkOBS01BranchStmt(node *ast.BranchStmt, tainted map[types.Object]bool) obs01Flow {
	flow := obs01Flow{tainted: tainted, continues: false}
	switch node.Tok {
	case token.BREAK:
		flow.breaks = []map[types.Object]bool{cloneOBS01Taints(tainted)}
	case token.CONTINUE:
		flow.continuesLoop = []map[types.Object]bool{cloneOBS01Taints(tainted)}
	}
	return flow
}

func walkOBS01Decl(
	info *types.Info,
	decl ast.Decl,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) {
	gen, ok := decl.(*ast.GenDecl)
	if !ok {
		inspectOBS01Calls(info, decl, tainted, returnTaints, handlers)
		return
	}
	for _, spec := range gen.Specs {
		valueSpec, ok := spec.(*ast.ValueSpec)
		if !ok {
			inspectOBS01Calls(info, spec, tainted, returnTaints, handlers)
			continue
		}
		markOBS01ValueSpecClosure(info, valueSpec, handlers.closures)
		markOBS01ValueSpecRangeTaint(info, valueSpec, tainted, returnTaints, handlers.rangeTaints)
		markOBS01ValueSpecTaint(info, valueSpec, tainted, returnTaints)
		inspectOBS01Calls(info, valueSpec, tainted, returnTaints, handlers)
	}
}

func walkOBS01IfStmt(
	info *types.Info,
	stmt *ast.IfStmt,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) obs01Flow {
	if stmt.Init != nil {
		initFlow := walkOBS01Stmt(info, stmt.Init, tainted, returnTaints, handlers)
		if !initFlow.continues {
			return initFlow
		}
		tainted = initFlow.tainted
	}
	inspectOBS01Calls(info, stmt.Cond, tainted, returnTaints, handlers)
	baseClosures := cloneOBS01Closures(handlers.closures)
	baseRanges := cloneOBS01RangeTaints(handlers.rangeTaints)
	parentClosures := handlers.closures
	parentRanges := handlers.rangeTaints

	thenHandlers := handlers
	thenHandlers.closures = cloneOBS01Closures(baseClosures)
	thenHandlers.rangeTaints = cloneOBS01RangeTaints(baseRanges)
	thenState := walkOBS01Stmts(info, stmt.Body.List, cloneOBS01Taints(tainted), returnTaints, thenHandlers)

	elseHandlers := handlers
	elseHandlers.closures = cloneOBS01Closures(baseClosures)
	elseHandlers.rangeTaints = cloneOBS01RangeTaints(baseRanges)
	elseState := obs01Flow{tainted: cloneOBS01Taints(tainted), continues: true}
	if stmt.Else != nil {
		elseState = walkOBS01Stmt(info, stmt.Else, elseState.tainted, returnTaints, elseHandlers)
	}
	replaceOBS01Closures(parentClosures, mergeOBS01BranchClosures(thenState, thenHandlers.closures, elseState, elseHandlers.closures))
	replaceOBS01RangeTaints(parentRanges,
		mergeOBS01BranchRangeTaints(thenState, thenHandlers.rangeTaints, elseState, elseHandlers.rangeTaints))
	return mergeOBS01Flows(thenState, elseState)
}

func walkOBS01ForStmt(
	info *types.Info,
	stmt *ast.ForStmt,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) obs01Flow {
	if stmt.Init != nil {
		initFlow := walkOBS01Stmt(info, stmt.Init, tainted, returnTaints, handlers)
		if !initFlow.continues {
			return initFlow
		}
		tainted = initFlow.tainted
	}
	inspectOBS01Calls(info, stmt.Cond, tainted, returnTaints, handlers)
	bodyState := walkOBS01Stmts(info, stmt.Body.List, cloneOBS01Taints(tainted), returnTaints, handlers)
	bodyState = walkOBS01ForPostFlow(info, stmt.Post, bodyState, returnTaints, handlers)
	secondEntry := mergeOBS01TaintMaps(tainted, bodyState.tainted)
	secondEntry = mergeOBS01TaintMaps(append([]map[types.Object]bool{secondEntry}, bodyState.continuesLoop...)...)
	secondBody := walkOBS01Stmts(info, stmt.Body.List, cloneOBS01Taints(secondEntry), returnTaints, handlers)
	secondBody = walkOBS01ForPostFlow(info, stmt.Post, secondBody, returnTaints, handlers)
	return obs01LoopFlow(obs01Flow{tainted: tainted, continues: true}, bodyState, secondBody)
}

func walkOBS01ForPostFlow(
	info *types.Info,
	post ast.Stmt,
	flow obs01Flow,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) obs01Flow {
	if post == nil {
		return flow
	}
	out := flow
	out.continuesLoop = nil
	if flow.continues {
		postFlow := walkOBS01Stmt(info, post, flow.tainted, returnTaints, handlers)
		out.tainted = postFlow.tainted
		out.continues = postFlow.continues
		out.breaks = append(out.breaks, postFlow.breaks...)
		out.continuesLoop = append(out.continuesLoop, postFlow.continuesLoop...)
	}
	for _, state := range flow.continuesLoop {
		postFlow := walkOBS01Stmt(info, post, cloneOBS01Taints(state), returnTaints, handlers)
		if postFlow.continues {
			out.continuesLoop = append(out.continuesLoop, postFlow.tainted)
		}
		out.breaks = append(out.breaks, postFlow.breaks...)
		out.continuesLoop = append(out.continuesLoop, postFlow.continuesLoop...)
	}
	return out
}

func walkOBS01RangeStmt(
	info *types.Info,
	stmt *ast.RangeStmt,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) obs01Flow {
	inspectOBS01Calls(info, stmt.X, tainted, returnTaints, handlers)
	bodyTaints := cloneOBS01Taints(tainted)
	markOBS01RangeTaint(info, stmt, bodyTaints, returnTaints, handlers.rangeTaints)
	bodyState := walkOBS01Stmts(info, stmt.Body.List, bodyTaints, returnTaints, handlers)
	secondEntry := mergeOBS01TaintMaps(tainted, bodyState.tainted)
	for _, state := range bodyState.continuesLoop {
		secondEntry = mergeOBS01TaintMaps(secondEntry, state)
	}
	secondBodyTaints := cloneOBS01Taints(secondEntry)
	markOBS01RangeTaint(info, stmt, secondBodyTaints, returnTaints, handlers.rangeTaints)
	secondBody := walkOBS01Stmts(info, stmt.Body.List, secondBodyTaints, returnTaints, handlers)
	return obs01LoopFlow(obs01Flow{tainted: tainted, continues: true}, bodyState, secondBody)
}

func markOBS01RangeTaint(
	info *types.Info,
	stmt *ast.RangeStmt,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	rangeTaints map[types.Object]obs01RangeTaint,
) {
	keyTainted, valueTainted := obs01RangeKeyValueTaints(info, stmt.X, tainted, returnTaints, rangeTaints)
	if keyTainted {
		if obj := objectForOBS01Expr(info, stmt.Key); obj != nil {
			tainted[obj] = true
		}
	}
	if valueTainted {
		if obj := objectForOBS01Expr(info, stmt.Value); obj != nil {
			tainted[obj] = true
		}
	}
}

func obs01RangeKeyValueTaints(
	info *types.Info,
	expr ast.Expr,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	rangeTaints map[types.Object]obs01RangeTaint,
) (bool, bool) {
	if lit, ok := unparenExpr(expr).(*ast.CompositeLit); ok {
		return obs01CompositeRangeKeyValueTaints(info, lit, tainted, returnTaints)
	}
	if obj := objectForOBS01Expr(info, expr); obj != nil {
		if rangeTaint, ok := rangeTaints[obj]; ok {
			return rangeTaint.Key, rangeTaint.Value
		}
	}
	if !exprDependsOnErrcodeClassifier(info, expr, tainted, returnTaints) {
		return false, false
	}
	if obs01RangeExprIsMap(info, expr) {
		return true, true
	}
	return false, true
}

func obs01RangeExprIsMap(info *types.Info, expr ast.Expr) bool {
	tv, ok := info.Types[unparenExpr(expr)]
	if !ok || tv.Type == nil {
		return false
	}
	_, ok = tv.Type.Underlying().(*types.Map)
	return ok
}

func obs01CompositeRangeKeyValueTaints(
	info *types.Info,
	lit *ast.CompositeLit,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
) (bool, bool) {
	keyTainted := false
	valueTainted := false
	isMap := obs01CompositeIsMap(info, lit)
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			if exprDependsOnErrcodeClassifier(info, elt, tainted, returnTaints) {
				valueTainted = true
			}
			continue
		}
		if isMap && exprDependsOnErrcodeClassifier(info, kv.Key, tainted, returnTaints) {
			keyTainted = true
		}
		if exprDependsOnErrcodeClassifier(info, kv.Value, tainted, returnTaints) {
			valueTainted = true
		}
	}
	return keyTainted, valueTainted
}

func obs01CompositeIsMap(info *types.Info, lit *ast.CompositeLit) bool {
	tv, ok := info.Types[lit]
	if !ok || tv.Type == nil {
		return false
	}
	_, ok = tv.Type.Underlying().(*types.Map)
	return ok
}

func walkOBS01SwitchStmt(
	info *types.Info,
	stmt *ast.SwitchStmt,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) obs01Flow {
	if stmt.Init != nil {
		initFlow := walkOBS01Stmt(info, stmt.Init, tainted, returnTaints, handlers)
		if !initFlow.continues {
			return initFlow
		}
		tainted = initFlow.tainted
	}
	inspectOBS01Calls(info, stmt.Tag, tainted, returnTaints, handlers)
	return obs01BreakableFlow(mergeOBS01CaseTaints(info, stmt.Body.List, tainted, returnTaints, handlers))
}

func walkOBS01TypeSwitchStmt(
	info *types.Info,
	stmt *ast.TypeSwitchStmt,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) obs01Flow {
	if stmt.Init != nil {
		initFlow := walkOBS01Stmt(info, stmt.Init, tainted, returnTaints, handlers)
		if !initFlow.continues {
			return initFlow
		}
		tainted = initFlow.tainted
	}
	if stmt.Assign != nil {
		assignFlow := walkOBS01Stmt(info, stmt.Assign, tainted, returnTaints, handlers)
		if !assignFlow.continues {
			return assignFlow
		}
		tainted = assignFlow.tainted
	}
	return obs01BreakableFlow(mergeOBS01CaseTaints(info, stmt.Body.List, tainted, returnTaints, handlers))
}

func walkOBS01SelectStmt(
	info *types.Info,
	stmt *ast.SelectStmt,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) obs01Flow {
	baseClosures := cloneOBS01Closures(handlers.closures)
	baseRanges := cloneOBS01RangeTaints(handlers.rangeTaints)
	parentClosures := handlers.closures
	parentRanges := handlers.rangeTaints
	states := []obs01Flow{{tainted: tainted, continues: true}}
	closureStates := []obs01Closures{baseClosures}
	rangeStates := []map[types.Object]obs01RangeTaint{baseRanges}
	for _, item := range stmt.Body.List {
		clause, ok := item.(*ast.CommClause)
		if !ok {
			continue
		}
		caseHandlers := handlers
		caseHandlers.closures = cloneOBS01Closures(baseClosures)
		caseHandlers.rangeTaints = cloneOBS01RangeTaints(baseRanges)
		caseState := cloneOBS01Taints(tainted)
		if clause.Comm != nil {
			commFlow := walkOBS01Stmt(info, clause.Comm, caseState, returnTaints, caseHandlers)
			if !commFlow.continues {
				states = append(states, commFlow)
				if obs01FlowCanReachOuter(commFlow) {
					closureStates = append(closureStates, caseHandlers.closures)
					rangeStates = append(rangeStates, caseHandlers.rangeTaints)
				}
				continue
			}
			caseState = commFlow.tainted
		}
		flow := walkOBS01Stmts(info, clause.Body, caseState, returnTaints, caseHandlers)
		states = append(states, flow)
		if obs01FlowCanReachOuter(flow) {
			closureStates = append(closureStates, caseHandlers.closures)
			rangeStates = append(rangeStates, caseHandlers.rangeTaints)
		}
	}
	replaceOBS01Closures(parentClosures, mergeOBS01Closures(closureStates...))
	replaceOBS01RangeTaints(parentRanges, mergeOBS01RangeTaints(rangeStates...))
	return obs01BreakableFlow(mergeOBS01Flows(states...))
}

func mergeOBS01CaseTaints(
	info *types.Info,
	cases []ast.Stmt,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) obs01Flow {
	states := make([]obs01Flow, 0, len(cases)+1)
	hasDefault := false
	var fallthroughState map[types.Object]bool
	var fallthroughClosures obs01Closures
	var fallthroughRanges map[types.Object]obs01RangeTaint
	baseClosures := cloneOBS01Closures(handlers.closures)
	baseRanges := cloneOBS01RangeTaints(handlers.rangeTaints)
	parentClosures := handlers.closures
	parentRanges := handlers.rangeTaints
	var closureStates []obs01Closures
	var rangeStates []map[types.Object]obs01RangeTaint
	for _, item := range cases {
		clause, ok := item.(*ast.CaseClause)
		if !ok {
			continue
		}
		if len(clause.List) == 0 {
			hasDefault = true
		}
		for _, expr := range clause.List {
			inspectOBS01Calls(info, expr, tainted, returnTaints, handlers)
		}
		caseState, caseHandlers := obs01CaseEntry(
			tainted, baseClosures, baseRanges, fallthroughState, fallthroughClosures, fallthroughRanges, handlers)
		flow, fallsThrough := walkOBS01CaseBody(info, clause, caseState, returnTaints, caseHandlers)
		fallthroughState = nil
		fallthroughClosures = nil
		fallthroughRanges = nil
		if fallsThrough {
			fallthroughState = flow.tainted
			fallthroughClosures = cloneOBS01Closures(caseHandlers.closures)
			fallthroughRanges = cloneOBS01RangeTaints(caseHandlers.rangeTaints)
			continue
		}
		states = append(states, flow)
		closureStates, rangeStates = appendOBS01ReachableHandlers(closureStates, rangeStates, flow, caseHandlers)
	}
	if !hasDefault {
		states = append(states, obs01Flow{tainted: tainted, continues: true})
		closureStates = append(closureStates, baseClosures)
		rangeStates = append(rangeStates, baseRanges)
	}
	replaceOBS01Closures(parentClosures, mergeOBS01Closures(closureStates...))
	replaceOBS01RangeTaints(parentRanges, mergeOBS01RangeTaints(rangeStates...))
	return mergeOBS01Flows(states...)
}

func obs01CaseEntry(
	tainted map[types.Object]bool,
	baseClosures obs01Closures,
	baseRanges map[types.Object]obs01RangeTaint,
	fallthroughState map[types.Object]bool,
	fallthroughClosures obs01Closures,
	fallthroughRanges map[types.Object]obs01RangeTaint,
	handlers obs01StmtHandlers,
) (map[types.Object]bool, obs01StmtHandlers) {
	caseState := cloneOBS01Taints(tainted)
	caseClosures := cloneOBS01Closures(baseClosures)
	caseRanges := cloneOBS01RangeTaints(baseRanges)
	if fallthroughState != nil {
		caseState = mergeOBS01TaintMaps(caseState, fallthroughState)
		caseClosures = mergeOBS01Closures(caseClosures, fallthroughClosures)
		caseRanges = mergeOBS01RangeTaints(caseRanges, fallthroughRanges)
	}
	caseHandlers := handlers
	caseHandlers.closures = caseClosures
	caseHandlers.rangeTaints = caseRanges
	return caseState, caseHandlers
}

func walkOBS01CaseBody(
	info *types.Info,
	clause *ast.CaseClause,
	caseState map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	caseHandlers obs01StmtHandlers,
) (obs01Flow, bool) {
	body := clause.Body
	fallsThrough := obs01FallsThrough(body)
	if fallsThrough {
		body = body[:len(body)-1]
	}
	flow := walkOBS01Stmts(info, body, caseState, returnTaints, caseHandlers)
	return flow, fallsThrough && flow.continues
}

func appendOBS01ReachableHandlers(
	closureStates []obs01Closures,
	rangeStates []map[types.Object]obs01RangeTaint,
	flow obs01Flow,
	handlers obs01StmtHandlers,
) ([]obs01Closures, []map[types.Object]obs01RangeTaint) {
	if !obs01FlowCanReachOuter(flow) {
		return closureStates, rangeStates
	}
	return append(closureStates, handlers.closures), append(rangeStates, handlers.rangeTaints)
}

func obs01FallsThrough(stmts []ast.Stmt) bool {
	if len(stmts) == 0 {
		return false
	}
	branch, ok := stmts[len(stmts)-1].(*ast.BranchStmt)
	return ok && branch.Tok == token.FALLTHROUGH
}

func inspectOBS01Calls(
	info *types.Info,
	node ast.Node,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) {
	if node == nil {
		return
	}
	ast.Inspect(node, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			return inspectOBS01Call(info, call, tainted, returnTaints, handlers)
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		return true
	})
}

func inspectOBS01Call(
	info *types.Info,
	call *ast.CallExpr,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) bool {
	if inspectOBS01FuncLitCall(info, call, tainted, returnTaints, handlers) {
		return false
	}
	if inspectOBS01ClosureCall(info, call, tainted, returnTaints, handlers) {
		return false
	}
	reportOBS01Call(call, tainted, handlers)
	return true
}

func inspectOBS01FuncLitCall(
	info *types.Info,
	call *ast.CallExpr,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) bool {
	fn, ok := unparenExpr(call.Fun).(*ast.FuncLit)
	if !ok {
		return false
	}
	inspectOBS01CallArgs(info, call.Args, tainted, returnTaints, handlers)
	reportOBS01Call(call, tainted, handlers)
	walkOBS01FuncLitCall(info, fn, call.Args, tainted, returnTaints, handlers)
	return true
}

func inspectOBS01ClosureCall(
	info *types.Info,
	call *ast.CallExpr,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) bool {
	fns := obs01CalledClosures(info, call, handlers.closures)
	if len(fns) == 0 {
		return false
	}
	inspectOBS01CallArgs(info, call.Args, tainted, returnTaints, handlers)
	baseTaints := cloneOBS01Taints(tainted)
	baseClosures := cloneOBS01Closures(handlers.closures)
	baseRanges := cloneOBS01RangeTaints(handlers.rangeTaints)
	parentClosures := handlers.closures
	parentRanges := handlers.rangeTaints
	var states []map[types.Object]bool
	var closureStates []obs01Closures
	var rangeStates []map[types.Object]obs01RangeTaint
	for _, fn := range fns {
		altHandlers := handlers
		altHandlers.closures = cloneOBS01Closures(baseClosures)
		altHandlers.rangeTaints = cloneOBS01RangeTaints(baseRanges)
		flow := runOBS01FuncLitCall(info, fn, call.Args, cloneOBS01Taints(baseTaints), returnTaints, altHandlers)
		states = append(states, flow.tainted)
		closureStates = append(closureStates, altHandlers.closures)
		rangeStates = append(rangeStates, altHandlers.rangeTaints)
	}
	replaceOBS01Taints(tainted, mergeOBS01TaintMaps(states...))
	replaceOBS01Closures(parentClosures, mergeOBS01Closures(closureStates...))
	replaceOBS01RangeTaints(parentRanges, mergeOBS01RangeTaints(rangeStates...))
	return true
}

func reportOBS01Call(call *ast.CallExpr, tainted map[types.Object]bool, handlers obs01StmtHandlers) {
	if handlers.onCall != nil && !handlers.suppressCalls[call] {
		handlers.onCall(call, tainted)
	}
}

func inspectOBS01CallArgs(
	info *types.Info,
	args []ast.Expr,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) {
	for _, arg := range args {
		inspectOBS01Calls(info, arg, tainted, returnTaints, handlers)
	}
}

func walkOBS01FuncLitCall(
	info *types.Info,
	fn *ast.FuncLit,
	args []ast.Expr,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) {
	flow := runOBS01FuncLitCall(info, fn, args, tainted, returnTaints, handlers)
	replaceOBS01Taints(tainted, flow.tainted)
}

func runOBS01FuncLitCall(
	info *types.Info,
	fn *ast.FuncLit,
	args []ast.Expr,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	handlers obs01StmtHandlers,
) obs01Flow {
	if handlers.activeClosures[fn] {
		return obs01Flow{tainted: tainted, continues: true}
	}
	handlers.activeClosures[fn] = true
	defer delete(handlers.activeClosures, fn)
	suppressed := obs01FuncLitVariadicSpreadCalls(info, fn)
	for call := range suppressed {
		handlers.suppressCalls[call] = true
	}
	defer func() {
		for call := range suppressed {
			delete(handlers.suppressCalls, call)
		}
	}()

	callTaints := cloneOBS01Taints(tainted)
	markOBS01FuncLitParamTaint(info, fn.Type, args, callTaints, returnTaints)
	return walkOBS01Stmts(info, fn.Body.List, callTaints, returnTaints, handlers)
}

func obs01FuncLitVariadicSpreadCalls(info *types.Info, fn *ast.FuncLit) map[*ast.CallExpr]bool {
	variadicObj, _, ok := obs01FuncLitVariadicParam(info, fn.Type)
	if !ok {
		return nil
	}
	out := map[*ast.CallExpr]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if nested, ok := n.(*ast.FuncLit); ok && nested != fn {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if _, ok := obs01VariadicSpreadLabelValueCall(info, call, variadicObj); ok {
			out[call] = true
		}
		return true
	})
	return out
}

func obs01CalledClosures(
	info *types.Info,
	call *ast.CallExpr,
	closures obs01Closures,
) []*ast.FuncLit {
	if closures == nil {
		return nil
	}
	var out []*ast.FuncLit
	for fn := range closures[objectForOBS01Expr(info, call.Fun)] {
		out = append(out, fn)
	}
	return out
}

func markOBS01FuncLitParamTaint(
	info *types.Info,
	typ *ast.FuncType,
	args []ast.Expr,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
) {
	params := functionParamObjects(info, typ)
	for i, obj := range params {
		if obs01FuncTypeVariadic(typ) && i == len(params)-1 {
			if exprDependsOnAnyErrcodeClassifierPosition(info, args, i, tainted, returnTaints) {
				tainted[obj] = true
			}
			continue
		}
		if exprDependsOnErrcodeClassifierForPosition(info, args, i, tainted, returnTaints) {
			tainted[obj] = true
		}
	}
}

func exprDependsOnAnyErrcodeClassifierPosition(
	info *types.Info,
	values []ast.Expr,
	start int,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
) bool {
	for i := start; i < obs01ValueExprsCount(info, values); i++ {
		if exprDependsOnErrcodeClassifierForPosition(info, values, i, tainted, returnTaints) {
			return true
		}
	}
	return false
}

func obs01FuncTypeVariadic(typ *ast.FuncType) bool {
	if typ.Params == nil || len(typ.Params.List) == 0 {
		return false
	}
	_, ok := typ.Params.List[len(typ.Params.List)-1].Type.(*ast.Ellipsis)
	return ok
}

func cloneOBS01Taints(in map[types.Object]bool) map[types.Object]bool {
	out := map[types.Object]bool{}
	for obj, tainted := range in {
		if tainted {
			out[obj] = true
		}
	}
	return out
}

func replaceOBS01Taints(dst, src map[types.Object]bool) {
	for obj := range dst {
		delete(dst, obj)
	}
	for obj, tainted := range src {
		if tainted {
			dst[obj] = true
		}
	}
}

func mergeOBS01Flows(states ...obs01Flow) obs01Flow {
	var continuing []map[types.Object]bool
	var breaks []map[types.Object]bool
	var continuesLoop []map[types.Object]bool
	for _, state := range states {
		if state.continues {
			continuing = append(continuing, state.tainted)
		}
		breaks = append(breaks, state.breaks...)
		continuesLoop = append(continuesLoop, state.continuesLoop...)
	}
	if len(continuing) == 0 {
		return obs01Flow{
			tainted:       map[types.Object]bool{},
			continues:     false,
			breaks:        breaks,
			continuesLoop: continuesLoop,
		}
	}
	return obs01Flow{
		tainted:       mergeOBS01TaintMaps(continuing...),
		continues:     true,
		breaks:        breaks,
		continuesLoop: continuesLoop,
	}
}

func obs01BreakableFlow(flow obs01Flow) obs01Flow {
	var states []map[types.Object]bool
	if flow.continues {
		states = append(states, flow.tainted)
	}
	states = append(states, flow.breaks...)
	if len(states) == 0 {
		return obs01Flow{
			tainted:       map[types.Object]bool{},
			continues:     false,
			continuesLoop: flow.continuesLoop,
		}
	}
	return obs01Flow{
		tainted:       mergeOBS01TaintMaps(states...),
		continues:     true,
		continuesLoop: flow.continuesLoop,
	}
}

func obs01LoopFlow(entry obs01Flow, bodies ...obs01Flow) obs01Flow {
	states := []map[types.Object]bool{entry.tainted}
	for _, body := range bodies {
		if body.continues {
			states = append(states, body.tainted)
		}
		states = append(states, body.breaks...)
		states = append(states, body.continuesLoop...)
	}
	return obs01Flow{tainted: mergeOBS01TaintMaps(states...), continues: true}
}

func obs01FlowCanReachOuter(flow obs01Flow) bool {
	return flow.continues || len(flow.breaks) > 0 || len(flow.continuesLoop) > 0
}

func cloneOBS01Closures(in obs01Closures) obs01Closures {
	out := obs01Closures{}
	for obj, fns := range in {
		out[obj] = map[*ast.FuncLit]bool{}
		for fn := range fns {
			out[obj][fn] = true
		}
	}
	return out
}

func replaceOBS01Closures(dst, src obs01Closures) {
	for obj := range dst {
		delete(dst, obj)
	}
	mergeOBS01ClosureMapInto(dst, src)
}

func mergeOBS01Closures(states ...obs01Closures) obs01Closures {
	out := obs01Closures{}
	for _, state := range states {
		mergeOBS01ClosureMapInto(out, state)
	}
	return out
}

func mergeOBS01BranchClosures(
	left obs01Flow,
	leftClosures obs01Closures,
	right obs01Flow,
	rightClosures obs01Closures,
) obs01Closures {
	out := obs01Closures{}
	if obs01FlowCanReachOuter(left) {
		mergeOBS01ClosureMapInto(out, leftClosures)
	}
	if obs01FlowCanReachOuter(right) {
		mergeOBS01ClosureMapInto(out, rightClosures)
	}
	return out
}

func mergeOBS01ClosureMapInto(dst, src obs01Closures) {
	for obj, fns := range src {
		if dst[obj] == nil {
			dst[obj] = map[*ast.FuncLit]bool{}
		}
		for fn := range fns {
			dst[obj][fn] = true
		}
	}
}

func cloneOBS01RangeTaints(in map[types.Object]obs01RangeTaint) map[types.Object]obs01RangeTaint {
	out := map[types.Object]obs01RangeTaint{}
	maps.Copy(out, in)
	return out
}

func mergeOBS01BranchRangeTaints(
	left obs01Flow,
	leftRanges map[types.Object]obs01RangeTaint,
	right obs01Flow,
	rightRanges map[types.Object]obs01RangeTaint,
) map[types.Object]obs01RangeTaint {
	out := map[types.Object]obs01RangeTaint{}
	if obs01FlowCanReachOuter(left) {
		mergeOBS01RangeTaintMapInto(out, leftRanges)
	}
	if obs01FlowCanReachOuter(right) {
		mergeOBS01RangeTaintMapInto(out, rightRanges)
	}
	return out
}

func replaceOBS01RangeTaints(dst, src map[types.Object]obs01RangeTaint) {
	for obj := range dst {
		delete(dst, obj)
	}
	mergeOBS01RangeTaintMapInto(dst, src)
}

func mergeOBS01RangeTaints(states ...map[types.Object]obs01RangeTaint) map[types.Object]obs01RangeTaint {
	out := map[types.Object]obs01RangeTaint{}
	for _, state := range states {
		mergeOBS01RangeTaintMapInto(out, state)
	}
	return out
}

func mergeOBS01RangeTaintMapInto(dst, src map[types.Object]obs01RangeTaint) {
	for obj, taint := range src {
		existing := dst[obj]
		dst[obj] = obs01RangeTaint{
			Key:   existing.Key || taint.Key,
			Value: existing.Value || taint.Value,
		}
	}
}

func mergeOBS01TaintMaps(states ...map[types.Object]bool) map[types.Object]bool {
	out := map[types.Object]bool{}
	for _, state := range states {
		for obj, tainted := range state {
			if tainted {
				out[obj] = true
			}
		}
	}
	return out
}

func markOBS01AssignedTaint(
	info *types.Info,
	lhs, rhs []ast.Expr,
	tok token.Token,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
) {
	replaces := tok == token.ASSIGN || tok == token.DEFINE
	for i, left := range lhs {
		if markOBS01IndexAssignedTaint(info, left, rhs, i, tainted, returnTaints) {
			continue
		}
		obj := objectForOBS01Expr(info, left)
		if obj == nil {
			continue
		}
		if exprDependsOnErrcodeClassifierForPosition(info, rhs, i, tainted, returnTaints) {
			tainted[obj] = true
		} else if replaces {
			delete(tainted, obj)
		}
	}
}

func markOBS01AssignedClosure(
	info *types.Info,
	lhs, rhs []ast.Expr,
	tok token.Token,
	closures obs01Closures,
) {
	if tok != token.ASSIGN && tok != token.DEFINE {
		return
	}
	for i, left := range lhs {
		obj := objectForOBS01Expr(info, left)
		if obj == nil {
			continue
		}
		if value, ok := obs01ValueExprForPosition(info, rhs, i); ok && value.Index == 0 {
			if fn, ok := unparenExpr(value.Expr).(*ast.FuncLit); ok {
				closures[obj] = map[*ast.FuncLit]bool{fn: true}
				continue
			}
		}
		delete(closures, obj)
	}
}

func markOBS01AssignedRangeTaint(
	info *types.Info,
	lhs, rhs []ast.Expr,
	tok token.Token,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	rangeTaints map[types.Object]obs01RangeTaint,
) {
	if tok != token.ASSIGN && tok != token.DEFINE {
		return
	}
	for i, left := range lhs {
		if markOBS01IndexAssignedRangeTaint(info, left, rhs, i, tainted, returnTaints, rangeTaints) {
			continue
		}
		obj := objectForOBS01Expr(info, left)
		if obj == nil {
			continue
		}
		if value, ok := obs01ValueExprForPosition(info, rhs, i); ok && value.Index == 0 {
			if lit, ok := unparenExpr(value.Expr).(*ast.CompositeLit); ok && obs01CompositeIsMap(info, lit) {
				key, value := obs01CompositeRangeKeyValueTaints(info, lit, tainted, returnTaints)
				rangeTaints[obj] = obs01RangeTaint{Key: key, Value: value}
				continue
			}
		}
		delete(rangeTaints, obj)
	}
}

func markOBS01IndexAssignedRangeTaint(
	info *types.Info,
	left ast.Expr,
	rhs []ast.Expr,
	index int,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	rangeTaints map[types.Object]obs01RangeTaint,
) bool {
	indexExpr, ok := left.(*ast.IndexExpr)
	if !ok {
		return false
	}
	obj := objectForOBS01Expr(info, indexExpr.X)
	if obj == nil {
		return true
	}
	existing := rangeTaints[obj]
	if exprDependsOnErrcodeClassifier(info, indexExpr.Index, tainted, returnTaints) {
		existing.Key = true
	}
	if exprDependsOnErrcodeClassifierForPosition(info, rhs, index, tainted, returnTaints) {
		existing.Value = true
	}
	if existing.Key || existing.Value {
		rangeTaints[obj] = existing
	}
	return true
}

func markOBS01IndexAssignedTaint(
	info *types.Info,
	left ast.Expr,
	rhs []ast.Expr,
	index int,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
) bool {
	indexExpr, ok := left.(*ast.IndexExpr)
	if !ok {
		return false
	}
	obj := objectForOBS01Expr(info, indexExpr.X)
	if obj == nil {
		return true
	}
	if exprDependsOnErrcodeClassifierForPosition(info, rhs, index, tainted, returnTaints) {
		tainted[obj] = true
	}
	return true
}

func markOBS01ValueSpecClosure(
	info *types.Info,
	spec *ast.ValueSpec,
	closures obs01Closures,
) {
	for i, name := range spec.Names {
		obj := info.Defs[name]
		if obj == nil {
			continue
		}
		if value, ok := obs01ValueExprForPosition(info, spec.Values, i); ok && value.Index == 0 {
			if fn, ok := unparenExpr(value.Expr).(*ast.FuncLit); ok {
				closures[obj] = map[*ast.FuncLit]bool{fn: true}
				continue
			}
		}
		delete(closures, obj)
	}
}

func markOBS01ValueSpecRangeTaint(
	info *types.Info,
	spec *ast.ValueSpec,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
	rangeTaints map[types.Object]obs01RangeTaint,
) {
	for i, name := range spec.Names {
		obj := info.Defs[name]
		if obj == nil {
			continue
		}
		if value, ok := obs01ValueExprForPosition(info, spec.Values, i); ok && value.Index == 0 {
			if lit, ok := unparenExpr(value.Expr).(*ast.CompositeLit); ok && obs01CompositeIsMap(info, lit) {
				key, value := obs01CompositeRangeKeyValueTaints(info, lit, tainted, returnTaints)
				rangeTaints[obj] = obs01RangeTaint{Key: key, Value: value}
				continue
			}
		}
		delete(rangeTaints, obj)
	}
}

func markOBS01ValueSpecTaint(
	info *types.Info,
	spec *ast.ValueSpec,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
) {
	for i, name := range spec.Names {
		obj := info.Defs[name]
		if obj == nil {
			continue
		}
		if exprDependsOnErrcodeClassifierForPosition(info, spec.Values, i, tainted, returnTaints) {
			tainted[obj] = true
		} else {
			delete(tainted, obj)
		}
	}
}

func exprDependsOnErrcodeClassifier(
	info *types.Info,
	expr ast.Expr,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		if id, ok := n.(*ast.Ident); ok {
			if tainted[objectForOBS01Expr(info, id)] {
				found = true
				return false
			}
			return true
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isErrcodeClassifierCall(info, call) || obs01AnyReturnTainted(returnTaints, calledFunc(info, call)) {
			found = true
			return false
		}
		return true
	})
	return found
}

func exprDependsOnErrcodeClassifierForPosition(
	info *types.Info,
	values []ast.Expr,
	index int,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
) bool {
	value, ok := obs01ValueExprForPosition(info, values, index)
	if !ok {
		return false
	}
	return exprDependsOnErrcodeClassifierForValue(info, value.Expr, value.Index, tainted, returnTaints)
}

func exprDependsOnErrcodeClassifierForValue(
	info *types.Info,
	expr ast.Expr,
	valueIndex int,
	tainted map[types.Object]bool,
	returnTaints obs01ReturnTaints,
) bool {
	if valueIndex == 0 && obs01ExprResultCount(info, expr) <= 1 {
		return exprDependsOnErrcodeClassifier(info, expr, tainted, returnTaints)
	}
	if taintedReturn, ok := obs01CallReturnIndexTainted(info, expr, returnTaints, valueIndex); ok && taintedReturn {
		return true
	}
	if exprDirectlyDependsOnErrcodeClassifier(info, expr, tainted) {
		return true
	}
	if _, ok := obs01CallReturnIndexTainted(info, expr, returnTaints, valueIndex); ok {
		return false
	}
	return exprDependsOnErrcodeClassifier(info, expr, tainted, returnTaints)
}

type obs01ValueExpr struct {
	Expr  ast.Expr
	Index int
}

func obs01ValueExprForPosition(info *types.Info, values []ast.Expr, position int) (obs01ValueExpr, bool) {
	if len(values) == 0 || position < 0 {
		return obs01ValueExpr{}, false
	}
	offset := 0
	for _, expr := range values {
		count := obs01ExprResultCount(info, expr)
		if count == 0 {
			count = 1
		}
		if position < offset+count {
			return obs01ValueExpr{Expr: expr, Index: position - offset}, true
		}
		offset += count
	}
	return obs01ValueExpr{}, false
}

func obs01ValueExprsCount(info *types.Info, values []ast.Expr) int {
	total := 0
	for _, expr := range values {
		count := obs01ExprResultCount(info, expr)
		if count == 0 {
			count = 1
		}
		total += count
	}
	return total
}

func obs01ExprResultCount(info *types.Info, expr ast.Expr) int {
	if expr == nil {
		return 0
	}
	tv, ok := info.Types[unparenExpr(expr)]
	if !ok || tv.Type == nil {
		return 1
	}
	if tuple, ok := tv.Type.(*types.Tuple); ok {
		return tuple.Len()
	}
	return 1
}

func obs01CallReturnIndexTainted(
	info *types.Info,
	expr ast.Expr,
	returnTaints obs01ReturnTaints,
	index int,
) (bool, bool) {
	call, ok := unparenExpr(expr).(*ast.CallExpr)
	if !ok {
		return false, false
	}
	if isErrcodeClassifierCall(info, call) {
		return index == 0, true
	}
	fn := calledFunc(info, call)
	key := obs01FuncKey(fn)
	if key == "" {
		return false, false
	}
	tainted, ok := returnTaints[key]
	if !ok {
		return false, false
	}
	return tainted[index], true
}

func exprDirectlyDependsOnErrcodeClassifier(
	info *types.Info,
	expr ast.Expr,
	tainted map[types.Object]bool,
) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		if id, ok := n.(*ast.Ident); ok {
			if tainted[objectForOBS01Expr(info, id)] {
				found = true
				return false
			}
			return true
		}
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isErrcodeClassifierCall(info, call) {
			found = true
			return false
		}
		return true
	})
	return found
}

func obs01AnyReturnTainted(returnTaints obs01ReturnTaints, fn *types.Func) bool {
	key := obs01FuncKey(fn)
	if key == "" {
		return false
	}
	for _, tainted := range returnTaints[key] {
		if tainted {
			return true
		}
	}
	return false
}

func unparenExpr(expr ast.Expr) ast.Expr {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = paren.X
	}
}

func objectForOBS01Expr(info *types.Info, expr ast.Expr) types.Object {
	switch e := expr.(type) {
	case *ast.Ident:
		if obj := info.Uses[e]; obj != nil {
			return obj
		}
		return info.Defs[e]
	case *ast.SelectorExpr:
		return info.Uses[e.Sel]
	}
	return nil
}

func isErrcodeClassifierCall(info *types.Info, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != errcodePkg {
		return false
	}
	return fn.Name() == "Category" || fn.Name() == "IsInfraError"
}

type obsAckFile struct {
	Acknowledgements []obsAck `yaml:"acknowledgements"`
}

type obsAck struct {
	Rule                 string   `yaml:"rule"`
	Fingerprint          string   `yaml:"fingerprint"`
	Metric               string   `yaml:"metric"`
	Label                string   `yaml:"label"`
	OldSemantics         string   `yaml:"oldSemantics"`
	NewSemantics         string   `yaml:"newSemantics"`
	DashboardOrAlertRefs []string `yaml:"dashboardOrAlertRefs"`
	Owner                string   `yaml:"owner"`
	ReviewedAt           string   `yaml:"reviewedAt"`
	Rationale            string   `yaml:"rationale"`
}

func loadOBS01Acks(root string) (map[string]obsAck, error) {
	path := filepath.Join(root, "docs", "observability", "metrics-migration-acks.yaml")
	out := map[string]obsAck{}
	data, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var file obsAckFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for i, ack := range file.Acknowledgements {
		if ack.Rule != "OBS-01" {
			return nil, fmt.Errorf("%s: acknowledgement %d has unsupported rule %q", path, i+1, ack.Rule)
		}
		if err := ack.validate(root, path, i+1); err != nil {
			return nil, err
		}
		if _, exists := out[ack.Fingerprint]; exists {
			return nil, fmt.Errorf("%s: OBS-01 acknowledgement %d duplicates fingerprint %q", path, i+1, ack.Fingerprint)
		}
		out[ack.Fingerprint] = ack
	}
	return out, nil
}

func (ack obsAck) validate(root, path string, idx int) error {
	required := map[string]string{
		"fingerprint":  ack.Fingerprint,
		"metric":       ack.Metric,
		"label":        ack.Label,
		"oldSemantics": ack.OldSemantics,
		"newSemantics": ack.NewSemantics,
		"owner":        ack.Owner,
		"reviewedAt":   ack.ReviewedAt,
		"rationale":    ack.Rationale,
	}
	for field, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s: OBS-01 acknowledgement %d missing %s", path, idx, field)
		}
	}
	if len(ack.DashboardOrAlertRefs) == 0 {
		return fmt.Errorf("%s: OBS-01 acknowledgement %d missing dashboardOrAlertRefs", path, idx)
	}
	if strings.TrimSpace(ack.OldSemantics) == strings.TrimSpace(ack.NewSemantics) {
		return fmt.Errorf("%s: OBS-01 acknowledgement %d oldSemantics and newSemantics must differ", path, idx)
	}
	if _, err := time.Parse("2006-01-02", ack.ReviewedAt); err != nil {
		return fmt.Errorf("%s: OBS-01 acknowledgement %d reviewedAt must be YYYY-MM-DD: %w", path, idx, err)
	}
	for i, ref := range ack.DashboardOrAlertRefs {
		if strings.TrimSpace(ref) == "" {
			return fmt.Errorf("%s: OBS-01 acknowledgement %d has empty dashboardOrAlertRefs[%d]", path, idx, i)
		}
		ok, gitErr := validOBS01Ref(root, ref)
		if gitErr != nil {
			return fmt.Errorf("%s: OBS-01 acknowledgement %d dashboardOrAlertRefs[%d] git lookup failed: %w", path, idx, i, gitErr)
		}
		if !ok {
			return fmt.Errorf("%s: OBS-01 acknowledgement %d dashboardOrAlertRefs[%d]"+
				" must be an existing repo-relative regular file committed in HEAD", path, idx, i)
		}
	}
	return nil
}

// validOBS01Ref reports whether ref is a valid OBS-01 acknowledgement
// reference: a repo-relative path under root, resolving to an existing
// regular file (not a symlink), and committed in HEAD when root is a git
// work tree. The committed-in-HEAD predicate is shared with generatedverify
// via governance.CommittedInHEAD so the two gates use the same definition
// of "tracked".
//
// Returns (false, nil) for path-shape violations and missing files.
// Returns (false, err) only when a git query itself fails — surfacing the
// failure beats silently fail-closing on a broken environment.
func validOBS01Ref(root, ref string) (bool, error) {
	rel, ok := normalizedOBS01RefRel(root, ref)
	if !ok {
		return false, nil
	}
	if !governance.HasGitMetadata(root) {
		return true, nil
	}
	return governance.CommittedInHEAD(root, rel)
}

// normalizedOBS01RefRel resolves ref to a forward-slash repo-relative path
// when it points to an existing regular file under root. The branches that
// short-circuit on filesystem errors swallow the underlying err on purpose:
// they are predicate failures (the ack ref shape is wrong) rather than
// I/O errors the caller should react to. Splitting this from the git
// check keeps validOBS01Ref's error path narrowly scoped to git failures.
func normalizedOBS01RefRel(root, ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if filepath.IsAbs(ref) || strings.HasPrefix(ref, "..") {
		return "", false
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	candidate, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(ref)))
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(cleanRoot, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	info, err := os.Lstat(candidate)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func (ack obsAck) matches(sink obs01SinkArg) bool {
	return ack.Metric == sink.Metric && ack.Label == sink.Label
}

func rejectUnusedOBS01Acks(root string, acks map[string]obsAck, matched map[string]bool) error {
	fingerprints := make([]string, 0, len(acks))
	for fingerprint := range acks {
		fingerprints = append(fingerprints, fingerprint)
	}
	sort.Strings(fingerprints)
	for _, fingerprint := range fingerprints {
		if matched[fingerprint] {
			continue
		}
		ack := acks[fingerprint]
		path := filepath.Join(root, "docs", "observability", "metrics-migration-acks.yaml")
		return fmt.Errorf("%s: OBS-01 acknowledgement for fingerprint %q is unused or stale (metric=%q label=%q)",
			path, fingerprint, ack.Metric, ack.Label)
	}
	return nil
}

func obs01Fingerprint(file string, line, column int, metric, label, expr string) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%d\x00%s\x00%s\x00%s",
		file, line, column, metric, label, expr))
	return hex.EncodeToString(sum[:])
}
