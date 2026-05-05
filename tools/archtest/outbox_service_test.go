package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
)

const (
	outboxServiceRuleTxRunnerNil     = "OUTBOX-SERVICE-01"
	outboxServiceRuleDirectPublish   = "OUTBOX-SERVICE-02"
	outboxServiceRuleRuntimeOutbox   = "OUTBOX-SERVICE-03"
	outboxServiceRulePublisherMode   = "OUTBOX-SERVICE-04"
	outboxServiceRuleWriterAdapter   = "OUTBOX-SERVICE-05_no_writer_adapter_option"
	outboxRuntimeImportRelPath       = "runtime/outbox"
	outboxServiceGlobReadablePattern = "cells/**/slices/**/service.go"
)

type outboxServiceViolation struct {
	Rule    string
	File    string
	Line    int
	Message string
}

func (v outboxServiceViolation) String() string {
	return fmt.Sprintf("%s: %s:%d: %s", v.Rule, v.File, v.Line, v.Message)
}

func TestSliceServicesDoNotBypassTransactionalOutbox(t *testing.T) {
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)

	violations := checkSliceServiceOutboxRules(t, root, modPath)
	byRule := groupOutboxServiceViolations(violations)

	if len(violations) > 0 {
		t.Logf("Found %d outbox service architecture violation(s):", len(violations))
		for _, v := range violations {
			t.Logf("  %s", v)
		}
	}

	t.Run("OUTBOX-SERVICE-01_no_txrunner_nil_mode", func(t *testing.T) {
		assert.Empty(t, byRule[outboxServiceRuleTxRunnerNil],
			"%s must not branch on txRunner == nil or txRunner != nil", outboxServiceGlobReadablePattern)
	})
	t.Run("OUTBOX-SERVICE-02_no_direct_publisher_publish", func(t *testing.T) {
		assert.Empty(t, byRule[outboxServiceRuleDirectPublish],
			"%s must not call Publisher.Publish directly from the service layer", outboxServiceGlobReadablePattern)
	})
	t.Run("OUTBOX-SERVICE-03_no_runtime_outbox_import", func(t *testing.T) {
		assert.Empty(t, byRule[outboxServiceRuleRuntimeOutbox],
			"%s must not import runtime/outbox", outboxServiceGlobReadablePattern)
	})
	t.Run("OUTBOX-SERVICE-04_no_publisher_mode_parsing", func(t *testing.T) {
		assert.Empty(t, byRule[outboxServiceRulePublisherMode],
			"%s must not depend on outbox.Publisher or construct DirectEmitter; Cell boundary owns mode parsing", outboxServiceGlobReadablePattern)
	})
	t.Run("OUTBOX-SERVICE-05_no_writer_adapter_option", func(t *testing.T) {
		assert.Empty(t, byRule[outboxServiceRuleWriterAdapter],
			"%s must not define WithOutboxWriter; service layer owns WithEmitter / WithTxManager only", outboxServiceGlobReadablePattern)
	})
}

func checkSliceServiceOutboxRules(t *testing.T, root, modPath string) []outboxServiceViolation {
	t.Helper()

	files, err := findSliceServiceFiles(root)
	require.NoError(t, err)
	require.NotEmpty(t, files, "no %s files found", outboxServiceGlobReadablePattern)

	var violations []outboxServiceViolation
	for _, file := range files {
		fileViolations, err := checkSliceServiceOutboxFile(root, modPath, file)
		require.NoError(t, err)
		violations = append(violations, fileViolations...)
	}
	return violations
}

func findSliceServiceFiles(root string) ([]string, error) {
	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		return nil, err
	}

	var files []string
	for _, s := range project.Slices {
		sliceDir := filepath.Join(root, filepath.Dir(s.File))
		svc := filepath.Join(sliceDir, "service.go")
		if _, statErr := os.Stat(svc); statErr == nil {
			if isSliceServiceFile(root, svc) {
				files = append(files, svc)
			}
		}
	}
	sort.Strings(files)
	return files, nil
}

func isSliceServiceFile(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	return strings.HasPrefix(rel, "cells/") &&
		strings.Contains(rel, "/slices/") &&
		strings.HasSuffix(rel, "/service.go")
}

func checkSliceServiceOutboxFile(root, modPath, path string) ([]outboxServiceViolation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}

	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	rel = filepath.ToSlash(rel)

	var violations []outboxServiceViolation
	for _, imp := range file.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		if importPath == modPath+"/"+outboxRuntimeImportRelPath {
			violations = append(violations, outboxServiceViolation{
				Rule:    outboxServiceRuleRuntimeOutbox,
				File:    rel,
				Line:    fset.Position(imp.Pos()).Line,
				Message: "service layer must not import runtime/outbox",
			})
		}
	}

	// Track the enclosing FuncDecl while walking so OUTBOX-SERVICE-01 can
	// allow constructor-level fail-fast validation (NewService) while still
	// rejecting runtime-method silent fallback. After 029 #03 ADR Decision 2
	// removed persistence.RunnerOrNoop, constructors fail-fast on nil
	// TxRunner (returning *Service, error) is the explicit replacement for
	// the deleted helper; method-internal fallback remains forbidden.
	var enclosing *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		switch expr := n.(type) {
		case *ast.FuncDecl:
			enclosing = expr
			if isWithOutboxWriterFunc(expr) {
				violations = append(violations, outboxServiceViolation{
					Rule:    outboxServiceRuleWriterAdapter,
					File:    rel,
					Line:    fset.Position(expr.Pos()).Line,
					Message: "service layer must not define WithOutboxWriter adapter option",
				})
			}
			if isPublisherModeParsingFunc(expr) {
				violations = append(violations, outboxServiceViolation{
					Rule:    outboxServiceRulePublisherMode,
					File:    rel,
					Line:    fset.Position(expr.Pos()).Line,
					Message: "service layer must not define direct-publisher mode helpers/options",
				})
			}
		case *ast.BinaryExpr:
			if isTxRunnerNilComparison(expr) && !isConstructorFailFast(enclosing) {
				violations = append(violations, outboxServiceViolation{
					Rule: outboxServiceRuleTxRunnerNil,
					File: rel,
					Line: fset.Position(expr.Pos()).Line,
					Message: "service layer must not branch on txRunner nil mode" +
						" (allowed only in NewService constructor as fail-fast validation returning error)." +
						" To opt in, change NewXxx to NewXxx(...) (*T, error) and add a top-level:" +
						" if txRunner == nil { return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, ...) }",
				})
			}
		case *ast.CallExpr:
			if isDirectPublishCall(expr) {
				violations = append(violations, outboxServiceViolation{
					Rule:    outboxServiceRuleDirectPublish,
					File:    rel,
					Line:    fset.Position(expr.Pos()).Line,
					Message: "service layer must not call Publisher.Publish directly",
				})
			}
			if isDirectEmitterConstructor(expr) {
				violations = append(violations, outboxServiceViolation{
					Rule:    outboxServiceRulePublisherMode,
					File:    rel,
					Line:    fset.Position(expr.Pos()).Line,
					Message: "service layer must not construct DirectEmitter",
				})
			}
		case *ast.SelectorExpr:
			if isOutboxPublisherSelector(expr) {
				violations = append(violations, outboxServiceViolation{
					Rule:    outboxServiceRulePublisherMode,
					File:    rel,
					Line:    fset.Position(expr.Pos()).Line,
					Message: "service layer must not expose outbox.Publisher dependencies",
				})
			}
			if isOutboxDirectPublishModeSelector(expr) {
				violations = append(violations, outboxServiceViolation{
					Rule:    outboxServiceRulePublisherMode,
					File:    rel,
					Line:    fset.Position(expr.Pos()).Line,
					Message: "service layer must not reference outbox direct-publish mode types or constants",
				})
			}
		case *ast.Field:
			if hasPublisherModeState(expr.Names) || isPublishFailureModeExpr(expr.Type) {
				violations = append(violations, outboxServiceViolation{
					Rule:    outboxServiceRulePublisherMode,
					File:    rel,
					Line:    fset.Position(expr.Pos()).Line,
					Message: "service layer must not store publisher mode state",
				})
			}
		case *ast.Ident:
			if isPublisherModeIdent(expr) {
				violations = append(violations, outboxServiceViolation{
					Rule:    outboxServiceRulePublisherMode,
					File:    rel,
					Line:    fset.Position(expr.Pos()).Line,
					Message: "service layer must not define or use direct-publisher mode names",
				})
			}
		}
		return true
	})

	return violations, nil
}

func isWithOutboxWriterFunc(fn *ast.FuncDecl) bool {
	return fn.Name.Name == "WithOutboxWriter"
}

func isPublisherModeParsingFunc(fn *ast.FuncDecl) bool {
	return fn.Name.Name == "WithPublishFailureMode" || fn.Name.Name == "directPublishMode"
}

func isTxRunnerNilComparison(expr *ast.BinaryExpr) bool {
	if expr.Op != token.EQL && expr.Op != token.NEQ {
		return false
	}
	return (isTxRunnerExpr(expr.X) && isNilIdent(expr.Y)) ||
		(isNilIdent(expr.X) && isTxRunnerExpr(expr.Y))
}

// isConstructorFailFast reports whether fn is a service constructor that
// performs explicit fail-fast validation on a nil TxRunner. After 029 #03 ADR
// Decision 2, constructors are allowed to fail-fast on nil TxRunner because
// that is the explicit, error-surfacing replacement for the deleted
// persistence.RunnerOrNoop helper. Method-level nil fallback (e.g. runInTx
// that skips tx when nil) remains forbidden because it silently degrades to
// non-transactional mode.
//
// A function qualifies iff all of the following hold:
//  1. It is a top-level function (no receiver) whose name starts with "New".
//  2. It returns exactly two results, the last of which is "error".
//  3. Its body's top-level statement list (Body.List, not recursively nested)
//     contains at least one statement matching isFailFastReturn — i.e. an
//     if-statement of the form:
//     if <txRunner-expr> == nil { return nil, <non-nil-expr> }
//
// Condition (3) prevents a NewFoo that internally installs a silent noop
// fallback (if s.txRunner == nil { s.txRunner = noopRunner{} }) from being
// whitelisted by the mere presence of a New* signature returning (*T, error).
func isConstructorFailFast(fn *ast.FuncDecl) bool {
	if fn == nil || fn.Recv != nil { // method (has receiver) — not a constructor
		return false
	}
	if !strings.HasPrefix(fn.Name.Name, "New") {
		return false
	}
	if fn.Type == nil || fn.Type.Results == nil || len(fn.Type.Results.List) != 2 {
		return false
	}
	last := fn.Type.Results.List[len(fn.Type.Results.List)-1]
	id, ok := last.Type.(*ast.Ident)
	if !ok || id.Name != "error" {
		return false
	}
	if fn.Body == nil {
		return false
	}
	for _, stmt := range fn.Body.List {
		ifStmt, ok := stmt.(*ast.IfStmt)
		if ok && isFailFastReturn(ifStmt) {
			return true
		}
	}
	return false
}

// isFailFastReturn reports whether stmt is an if-statement of the form:
//
//	if <txRunner-expr> == nil { return nil, <non-nil-expr> }
//
// The else branch is not examined. Only top-level return statements inside
// stmt.Body are checked; nested blocks are not recursed into.
func isFailFastReturn(stmt *ast.IfStmt) bool {
	// Condition must be a binary == expression with one side being a
	// txRunner expression and the other being nil.
	binExpr, ok := stmt.Cond.(*ast.BinaryExpr)
	if !ok || binExpr.Op != token.EQL {
		return false
	}
	if (!isTxRunnerExpr(binExpr.X) || !isNilIdent(binExpr.Y)) &&
		(!isNilIdent(binExpr.X) || !isTxRunnerExpr(binExpr.Y)) {
		return false
	}
	// The body must contain at least one return statement whose first result
	// is nil and whose second result is any non-nil expression.
	for _, bodyStmt := range stmt.Body.List {
		ret, ok := bodyStmt.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 2 {
			continue
		}
		firstIsNil := isNilIdent(ret.Results[0])
		secondIsNonNil := !isNilIdent(ret.Results[1])
		if firstIsNil && secondIsNonNil {
			return true
		}
	}
	return false
}

func isTxRunnerExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == "txRunner"
	case *ast.SelectorExpr:
		return e.Sel.Name == "txRunner"
	default:
		return false
	}
}

func isNilIdent(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == "nil"
}

func isDirectPublishCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "Publish"
}

func isDirectEmitterConstructor(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "NewDirectEmitter"
}

func isOutboxPublisherSelector(expr *ast.SelectorExpr) bool {
	ident, ok := expr.X.(*ast.Ident)
	return ok && ident.Name == "outbox" && expr.Sel.Name == "Publisher"
}

func isOutboxDirectPublishModeSelector(expr *ast.SelectorExpr) bool {
	ident, ok := expr.X.(*ast.Ident)
	if !ok || ident.Name != "outbox" {
		return false
	}
	switch expr.Sel.Name {
	case "DirectPublishFailureMode", "DirectPublishFailOpen", "DirectPublishFailClosed":
		return true
	default:
		return false
	}
}

func hasPublisherModeState(names []*ast.Ident) bool {
	for _, name := range names {
		if name != nil && name.Name == "publishFailureMode" {
			return true
		}
	}
	return false
}

func isPublishFailureModeExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == "PublishFailureMode"
	case *ast.SelectorExpr:
		return e.Sel.Name == "PublishFailureMode"
	default:
		return false
	}
}

func isPublisherModeIdent(id *ast.Ident) bool {
	switch id.Name {
	case "WithPublishFailureMode", "directPublishMode", "publishFailureMode":
		return true
	default:
		return false
	}
}

func groupOutboxServiceViolations(violations []outboxServiceViolation) map[string][]string {
	byRule := make(map[string][]string)
	for _, v := range violations {
		byRule[v.Rule] = append(byRule[v.Rule], v.String())
	}
	return byRule
}
