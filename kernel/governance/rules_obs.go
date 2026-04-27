package governance

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
)

// OBS-01: any Prometheus metric registration whose LabelNames slice contains
// a value derived from errcode.Category(...) or errcode.IsInfraError(...) must
// flag a Warning so the PR description can attach a bucket-migration table.
//
// Why: Category / IsInfraError partition errors into SLO buckets. A counter
// labelled by either becomes a SLO-shaped bucket; reclassifying an errcode
// silently flips its counter bucket, which drifts dashboards and alert
// thresholds without anyone noticing. Forcing a PR-side discussion on every
// introduction is cheaper than catching the drift at incident time.
//
// Scope: runtime/observability/metrics/, adapters/.
// Severity: Warning (introducing a Category-labelled counter is allowed when
// accompanied by a migration plan; the rule blocks silent drift, not the
// pattern itself).
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
	var results []ValidationResult
	fset := token.NewFileSet()
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
		results = append(results, scanOBS01File(fset, file, path, metricsAlias, errcodeAlias)...)
		return nil
	})
	return results
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
				Severity:  SeverityWarning,
				IssueType: IssueInvalid,
				File:      path,
				Field:     "LabelNames",
				Line:      pos.Line,
				Column:    pos.Column,
				Message: fmt.Sprintf(
					"counter LabelNames contains errcode classifier call (%s.%s) — PR description must include bucket-migration table per OBS-01 SLO drift policy",
					errcodeAlias, callName(classifier),
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
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != errcodeAlias {
			return true
		}
		if obs01ErrcodeFuncs[sel.Sel.Name] {
			found = call
			return false
		}
		return true
	})
	return found
}

func callName(call *ast.CallExpr) string {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
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
