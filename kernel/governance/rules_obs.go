package governance

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// OBS-01: any Prometheus metric registration whose LabelNames slice contains
// a value derived from errcode.Category(...) or errcode.IsInfraError(...) must
// flag an Error so the PR description can attach a bucket-migration table.
//
// Why: Category / IsInfraError partition errors into SLO buckets. A counter
// labelled by either becomes a SLO-shaped bucket; reclassifying an errcode
// silently flips its counter bucket, which drifts dashboards and alert
// thresholds without anyone noticing. Forcing a PR-side discussion on every
// introduction is cheaper than catching the drift at incident time.
//
// Scope: runtime/observability/metrics/, adapters/.
// Severity: Error — blocks merge until a bucket-migration table is present
// in the PR description.
//
// Remediation guidance: when OBS-01 fires, the PR author must:
//  1. Attach a bucket-migration table to the PR description explaining which
//     SLO dashboards / alerts are affected and how they will be migrated.
//  2. Regenerate the metrics-schema baseline:
//     go run ./cmd/gocell generate metrics-schema --id <assemblyID>
//  3. Commit assemblies/<id>/generated/metrics-schema.yaml so the CI
//     verify-and-diff gate (metrics-schema drift check) stays clean.
//
// Note — typed identity upgrade (blocked by kernel dependency policy):
// A stronger implementation would verify that CounterOpts / errcode.Category
// originate from their canonical packages via go/types (typed identity).
// This is blocked because kernel/ must not import golang.org/x/tools/go/packages
// (see CLAUDE.md and the comment in rules_consistency.go). The AST name-matching
// here is consistent with BuildMetricsSchema in metrics_schema.go. Typed identity
// can be added in the tools/ layer or after the kernel dependency policy relaxes.
//
// ref: kubernetes apiserver/pkg/audit Backend.FailurePolicy — same "loud
// rather than silent" treatment of policy-bearing knobs.
const ruleOBS01 = "OBS-01"

const (
	metricsPkgPath = `"github.com/ghbvf/gocell/kernel/observability/metrics"`
	errcodePkgPath = `"github.com/ghbvf/gocell/pkg/errcode"`
)

var obs01OptsTypes = map[string]bool{
	"CounterOpts":   true,
	"HistogramOpts": true,
	"GaugeOpts":     true,
}

var obs01ErrcodeFuncs = map[string]bool{
	"Category":     true,
	"IsInfraError": true,
}

// validateOBS01 walks runtime/observability/metrics/ and adapters/ and reports
// counter/histogram/gauge registrations whose LabelNames slice contains
// errcode-classification calls.
func (v *Validator) validateOBS01() []ValidationResult {
	if v.root == "" {
		return nil
	}
	var results []ValidationResult
	for _, rel := range []string{
		filepath.FromSlash("runtime/observability/metrics"),
		"adapters",
	} {
		root := filepath.Join(v.root, rel)
		results = append(results, v.scanOBS01Tree(root)...)
	}
	return results
}

func (v *Validator) scanOBS01Tree(root string) []ValidationResult {
	if _, statErr := os.Stat(root); errors.Is(statErr, os.ErrNotExist) {
		return nil // directory absent — nothing to scan
	}
	var results []ValidationResult
	fset := token.NewFileSet()
	walkErr := filepath.WalkDir(root, obs01WalkFunc(fset, &results))
	if walkErr != nil {
		results = append(results, ValidationResult{
			Code:     ruleOBS01,
			Severity: SeverityError,
			Message:  "OBS-01 scan incomplete: " + walkErr.Error(),
		})
	}
	return results
}

func obs01WalkFunc(fset *token.FileSet, results *[]ValidationResult) fs.WalkDirFunc {
	return func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "testdata", "generated", "worktrees", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		metricsAlias, hasMetrics := importAlias(file, metricsPkgPath, "metrics")
		errcodeAlias, hasErrcode := importAlias(file, errcodePkgPath, "errcode")
		if !hasMetrics || !hasErrcode {
			return nil
		}
		*results = append(*results, scanOBS01File(fset, file, path, metricsAlias, errcodeAlias)...)
		return nil
	}
}

// scanOBS01File looks for `<metrics-alias>.{CounterOpts,HistogramOpts,GaugeOpts}{...}`
// composite literals whose `LabelNames` slice contains
// `<errcode-alias>.Category(...)` or `<errcode-alias>.IsInfraError(...)`
// expressions.
func scanOBS01File(fset *token.FileSet, file *ast.File, path, metricsAlias, errcodeAlias string) []ValidationResult {
	var results []ValidationResult
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isOBS01OptsLiteral(lit, metricsAlias) {
			return true
		}
		labels, ok := findOBS01LabelNames(lit)
		if !ok {
			return true
		}
		for _, elt := range labels.Elts {
			classifier := findErrcodeClassifierCall(elt, errcodeAlias)
			if classifier == nil {
				continue
			}
			pos := fset.Position(classifier.Pos())
			results = append(results, ValidationResult{
				Code:      ruleOBS01,
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      path,
				Field:     "LabelNames",
				Line:      pos.Line,
				Column:    pos.Column,
				Message: fmt.Sprintf(
					"LabelNames uses errcode.%s — labelling a Prometheus counter by error category creates an SLO bucket; later reclassifying that errcode silently flips bucket counts. Add a bucket-migration table to the PR description and regenerate the metrics-schema baseline: go run ./cmd/gocell generate metrics-schema --id <assemblyID> (see OBS-01 in kernel/governance/rules_obs.go).",
					callName(classifier),
				),
			})
		}
		return true
	})
	return results
}

func isOBS01OptsLiteral(lit *ast.CompositeLit, metricsAlias string) bool {
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != metricsAlias {
		return false
	}
	return obs01OptsTypes[sel.Sel.Name]
}

func findOBS01LabelNames(lit *ast.CompositeLit) (*ast.CompositeLit, bool) {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "LabelNames" {
			continue
		}
		slice, ok := kv.Value.(*ast.CompositeLit)
		if !ok {
			return nil, false
		}
		return slice, true
	}
	return nil, false
}

// findErrcodeClassifierCall returns the first nested errcode.Category(...) or
// errcode.IsInfraError(...) call within expr, or nil. Detects classifier use
// regardless of how it is wrapped (`errcode.Category(err).String()`,
// `fmt.Sprint(errcode.IsInfraError(err))`, etc.).
//
// When errcodeAlias is "." (dot-import), bare Ident calls like Category(err)
// are matched instead of SelectorExpr. This may produce false positives for
// same-named functions from other packages, which is acceptable — dot-import
// of production packages is itself a code smell and should not be exempted.
func findErrcodeClassifierCall(expr ast.Expr, errcodeAlias string) *ast.CallExpr {
	var found *ast.CallExpr
	ast.Inspect(expr, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isErrcodeClassifier(call, errcodeAlias) {
			found = call
			return false
		}
		return true
	})
	return found
}

// isErrcodeClassifier reports whether call invokes an errcode classifier
// function (Category or IsInfraError) qualified by errcodeAlias.
func isErrcodeClassifier(call *ast.CallExpr, errcodeAlias string) bool {
	if errcodeAlias == "." {
		// dot-import: function appears as bare Ident, e.g. Category(err)
		id, ok := call.Fun.(*ast.Ident)
		return ok && obs01ErrcodeFuncs[id.Name]
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == errcodeAlias && obs01ErrcodeFuncs[sel.Sel.Name]
}

func callName(call *ast.CallExpr) string {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	if id, ok := call.Fun.(*ast.Ident); ok {
		return id.Name
	}
	return "?"
}

// importAlias resolves the local alias for an import path. The default short
// name is used when no explicit alias is set; dot-imports return ".".
func importAlias(file *ast.File, fullPathQuoted, defaultName string) (string, bool) {
	for _, imp := range file.Imports {
		if imp.Path == nil || imp.Path.Value != fullPathQuoted {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name, true
		}
		return defaultName, true
	}
	return "", false
}
