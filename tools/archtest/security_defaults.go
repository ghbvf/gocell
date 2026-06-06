package archtest

// security_defaults.go — importable SEC-FAIL-CLOSED-02..10 rule logic
// (#1640 M3 PR-9).
//
// Non-test home for the CheckSecurityDefaults scanner and all its sub-check
// helpers, so external Cell repositories can compile and run the rule without
// needing GoCell's _test.go files. GoCell's own TestSecurityDefaults in
// security_defaults_test.go dogfoods CheckSecurityDefaults — single source,
// no parallel rule body.
//
// Rules implemented here (SEC-FAIL-CLOSED-02..10):
//
//	02  listener authChain non-nil: all WithListener calls must pass an explicit
//	    non-nil 3rd argument (no bare nil literal).
//	03  adapter TLS endpoint: redis, vault, s3 adapters must import pkg/secutil
//	    and call secutil.ValidateTLSEndpoint.
//	04  websocket origins: no file in adapters/websocket may assign
//	    opts.InsecureSkipVerify = true.
//	05  example docker compose credentials must come from environment
//	    interpolation, not committed literal values.
//	06  internal listener guard: production WithListener calls must not wire
//	    cell.InternalListener with a literal AuthNone chain.
//	07  websocket UpgradeConfig literals must include Authenticator field.
//	08  no production code may call runtime/websocket.Hub.Broadcast (deleted API).
//	09  hub.go conns and subjectIdx delete points must stay in sync.
//	10  health listener required: a package main that references
//	    cell.PrimaryListener must also reference cell.HealthListener.
//
// SEC-FAIL-CLOSED-01 is retired (see testSEC01AddrDrivenGate godoc in
// security_defaults_test.go).
//
// Dogfood test: TestSecurityDefaults (security_defaults_test.go).
//
// # Register status
//
// Not registered in StandardCellRules: scan-scope is gocell-hardcoded
// (adapters/redis|vault|s3, adapters/websocket, runtime/websocket, kernel/cell
// path, composition-root allowlists) with no ConfigForExternalCell
// consumer-extension → vacuous/false-red externally; kept importable +
// module-path-agnostic + fork-safe.

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// ---------------------------------------------------------------------------
// Package-path constants (module-path-agnostic)
// ---------------------------------------------------------------------------

// kernelCellPkgPath is the canonical import path of the kernel/cell package,
// home of the ListenerRef consts (PrimaryListener / InternalListener /
// HealthListener). SEC-FAIL-CLOSED-10 resolves listener references to this path
// via ResolvePackageRef (type-aware), so import aliases do not defeat it.
//
// Named without a "sec" prefix so that code in this file (listenerRefsInPass,
// dotImportKernelCellHits) and the standalone tests in security_defaults_test.go
// can reference it with a single shared name.
const kernelCellPkgPath = PlatformModulePath + "/kernel/cell"

// secSecutilImportLiteral is the Go quoted import literal for pkg/secutil, used
// in text-based source scan (SEC-03). Derived from PlatformModulePath to
// remain module-path-agnostic.
const secSecutilImportLiteral = `"` + PlatformModulePath + `/pkg/secutil"`

// secWebsocketImportLiteral is the Go quoted import literal for
// runtime/websocket, used in text-based source scan (SEC-08). Derived from
// PlatformModulePath to remain module-path-agnostic.
const secWebsocketImportLiteral = `"` + PlatformModulePath + `/runtime/websocket"`

// ---------------------------------------------------------------------------
// SEC rule ID string constants
// ---------------------------------------------------------------------------

const (
	// secFailClosed01 is retained as an inert marker; the rule body is a
	// one-line t.Skip() — see testSEC01AddrDrivenGate in security_defaults_test.go.
	secFailClosed01 = "SEC-FAIL-CLOSED-01"
	secFailClosed02 = "SEC-FAIL-CLOSED-02"
	secFailClosed03 = "SEC-FAIL-CLOSED-03"
	secFailClosed04 = "SEC-FAIL-CLOSED-04"
	secFailClosed05 = "SEC-FAIL-CLOSED-05"
	secFailClosed06 = "SEC-FAIL-CLOSED-06"
	secFailClosed07 = "SEC-FAIL-CLOSED-07"
	secFailClosed08 = "SEC-FAIL-CLOSED-08"
	secFailClosed09 = "SEC-FAIL-CLOSED-09"
	secFailClosed10 = "SEC-FAIL-CLOSED-10"
)

// ---------------------------------------------------------------------------
// CheckSecurityDefaults — top-level importable entry point
// ---------------------------------------------------------------------------

// CheckSecurityDefaults runs SEC-FAIL-CLOSED-02..10 and returns all violations
// as []Diagnostic. It does not call t.Errorf; callers use Report to surface
// them. t.Fatalf is used only for infrastructure load failures.
//
// SEC-FAIL-CLOSED-01 is skipped (retired; see security_defaults_test.go).
func CheckSecurityDefaults(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)

	var diags []Diagnostic
	diags = append(diags, secCheckSEC02(t, root)...)
	diags = append(diags, secCheckSEC03(t, root)...)
	diags = append(diags, secCheckSEC04(t, root)...)
	diags = append(diags, secCheckSEC05(t, root)...)
	diags = append(diags, secCheckSEC06(t, root)...)
	diags = append(diags, secCheckSEC07(t, root)...)
	diags = append(diags, secCheckSEC08(t, root)...)
	diags = append(diags, secCheckSEC09(t, root)...)
	diags = append(diags, secCheckSEC10(t)...)
	return diags
}

// ---------------------------------------------------------------------------
// SEC-02: listener authChain must not be bare nil
// ---------------------------------------------------------------------------

func secCheckSEC02(t *testing.T, root string) []Diagnostic {
	t.Helper()
	scanFiles, err := findAllProductionMainPackageFiles(root)
	if err != nil {
		t.Fatalf("%s: finding production main package files: %v", secFailClosed02, err)
	}
	var diags []Diagnostic
	for _, f := range scanFiles {
		hits, err := findWithListenerNilAuthChain(f)
		if err != nil {
			t.Fatalf("%s: scanning %s: %v", secFailClosed02, f, err)
		}
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		for _, line := range hits {
			diags = append(diags, Diagnostic{
				Rel:     rel,
				Line:    line,
				Message: "WithListener 3rd arg is bare nil (" + secFailClosed02 + ")",
			})
		}
	}
	return diags
}

// findWithListenerNilAuthChain parses path and returns line numbers of every
// bootstrap.WithListener CallExpr where the 3rd argument is the identifier nil.
func findWithListenerNilAuthChain(path string) ([]int, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var lines []int
	scanner.EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		// Must be a SelectorExpr "bootstrap.WithListener" or plain "WithListener".
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fn.Sel.Name != "WithListener" {
				return
			}
		case *ast.Ident:
			if fn.Name != "WithListener" {
				return
			}
		default:
			return
		}
		// 3rd argument (index 2) must not be nil identifier.
		if len(call.Args) < 3 {
			return
		}
		arg := call.Args[2]
		ident, ok := arg.(*ast.Ident)
		if ok && ident.Name == "nil" {
			lines = append(lines, fset.Position(call.Lparen).Line)
		}
	})
	return lines, nil
}

// ---------------------------------------------------------------------------
// SEC-06: internal listener must not use AuthNone chain
// ---------------------------------------------------------------------------

func secCheckSEC06(t *testing.T, root string) []Diagnostic {
	t.Helper()
	scanFiles, err := findAllProductionMainPackageFiles(root)
	if err != nil {
		t.Fatalf("%s: finding production main package files: %v", secFailClosed06, err)
	}
	var diags []Diagnostic
	for _, f := range scanFiles {
		hits, err := findInternalListenerAuthNoneChain(f)
		if err != nil {
			t.Fatalf("%s: scanning %s: %v", secFailClosed06, f, err)
		}
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		for _, line := range hits {
			diags = append(diags, Diagnostic{
				Rel:     rel,
				Line:    line,
				Message: "InternalListener uses AuthNone literal (" + secFailClosed06 + ")",
			})
		}
	}
	return diags
}

func findInternalListenerAuthNoneChain(path string) ([]int, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var lines []int
	facts := collectAuthNoneChainFacts(f)
	scanner.EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		if !isWithListenerCall(call) || len(call.Args) < 3 {
			return
		}
		if !isInternalListenerRef(call.Args[0]) || !chainExprContainsAuthNone(call.Args[2], facts) {
			return
		}
		lines = append(lines, fset.Position(call.Lparen).Line)
	})
	return lines, nil
}

type authNoneChainFacts struct {
	vars  map[string]bool
	funcs map[string]bool
}

func collectAuthNoneChainFacts(f *ast.File) authNoneChainFacts {
	facts := authNoneChainFacts{
		vars:  make(map[string]bool),
		funcs: make(map[string]bool),
	}
	secCollectAuthNoneFuncFacts(f, facts.funcs)
	secCollectAuthNoneVarFacts(f, facts.vars)
	secCollectAuthNoneAssignFacts(f, facts.vars)
	return facts
}

// secCollectAuthNoneFuncFacts populates funcs with names of functions whose
// return statements yield a chain literal containing AuthNone.
func secCollectAuthNoneFuncFacts(f *ast.File, funcs map[string]bool) {
	scanner.EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
		if fn.Body == nil {
			return
		}
		scanner.EachInSubtree[ast.ReturnStmt](fn.Body, func(ret *ast.ReturnStmt) {
			for _, result := range ret.Results {
				if chainLiteralContainsAuthNone(result) {
					funcs[fn.Name.Name] = true
				}
			}
		})
	})
}

// secCollectAuthNoneVarFacts populates vars with names of var/const specs whose
// initialiser is a chain literal containing AuthNone.
func secCollectAuthNoneVarFacts(f *ast.File, vars map[string]bool) {
	scanner.EachInSubtree[ast.ValueSpec](f, func(stmt *ast.ValueSpec) {
		for i, name := range stmt.Names {
			if authNoneRHSAt(stmt.Values, i) {
				vars[name.Name] = true
			}
		}
	})
}

// secCollectAuthNoneAssignFacts populates vars with names of variables that are
// assigned a chain literal containing AuthNone.
func secCollectAuthNoneAssignFacts(f *ast.File, vars map[string]bool) {
	scanner.EachInSubtree[ast.AssignStmt](f, func(stmt *ast.AssignStmt) {
		for i, lhsExpr := range stmt.Lhs {
			id := exprToIdent(lhsExpr)
			if id == nil {
				continue
			}
			if authNoneRHSAt(stmt.Rhs, i) {
				vars[id.Name] = true
			}
		}
	})
}

func authNoneRHSAt(rhs []ast.Expr, idx int) bool {
	if len(rhs) == 0 {
		return false
	}
	if len(rhs) == 1 {
		return chainLiteralContainsAuthNone(rhs[0])
	}
	if idx >= len(rhs) {
		return false
	}
	return chainLiteralContainsAuthNone(rhs[idx])
}

// exprToIdent casts e to *ast.Ident, returning nil if not an identifier.
func exprToIdent(e ast.Expr) *ast.Ident {
	id, _ := e.(*ast.Ident)
	return id
}

func chainExprContainsAuthNone(expr ast.Expr, facts authNoneChainFacts) bool {
	if chainLiteralContainsAuthNone(expr) {
		return true
	}
	switch e := expr.(type) {
	case *ast.Ident:
		return facts.vars[e.Name]
	case *ast.CallExpr:
		id, ok := e.Fun.(*ast.Ident)
		return ok && facts.funcs[id.Name]
	default:
		return false
	}
}

func isWithListenerCall(call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name == "WithListener"
	case *ast.Ident:
		return fn.Name == "WithListener"
	default:
		return false
	}
}

func isInternalListenerRef(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "InternalListener"
}

func chainLiteralContainsAuthNone(expr ast.Expr) bool {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return false
	}
	return slices.ContainsFunc(lit.Elts, isAuthNoneComposite)
}

func isAuthNoneComposite(expr ast.Expr) bool {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return false
	}
	sel, ok := lit.Type.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "AuthNone"
}

// ---------------------------------------------------------------------------
// SEC-03: adapter TLS endpoint validation
// ---------------------------------------------------------------------------

func secCheckSEC03(t *testing.T, root string) []Diagnostic {
	t.Helper()
	targets := []struct {
		label string
		dir   string
	}{
		{"adapters/redis", filepath.Join(root, "adapters", "redis")},
		{"adapters/vault", filepath.Join(root, "adapters", "vault")},
		{"adapters/s3", filepath.Join(root, "adapters", "s3")},
	}
	var diags []Diagnostic
	for _, tgt := range targets {
		files, err := findProductionGoFilesInDir(tgt.dir)
		if err != nil {
			t.Fatalf("%s: reading %s: %v", secFailClosed03, tgt.label, err)
		}
		diags = append(diags, secCheckSEC03Target(tgt.label, files)...)
	}
	return diags
}

// secCheckSEC03Target checks a single adapter directory for secutil import and
// ValidateTLSEndpoint call, returning any violations as diagnostics.
func secCheckSEC03Target(label string, files []string) []Diagnostic {
	pkgImportsSecutil := false
	pkgCallsValidate := false
	for _, f := range files {
		data, rerr := os.ReadFile(filepath.Clean(f))
		if rerr != nil {
			continue
		}
		src := string(data)
		if strings.Contains(src, secSecutilImportLiteral) {
			pkgImportsSecutil = true
		}
		if secutilCallsValidateTLSEndpoint(src) {
			pkgCallsValidate = true
		}
	}
	var diags []Diagnostic
	if !pkgImportsSecutil {
		diags = append(diags, Diagnostic{
			Rel:     label,
			Line:    1,
			Message: label + ": does not import pkg/secutil (" + secFailClosed03 + ")",
		})
	}
	if !pkgCallsValidate {
		diags = append(diags, Diagnostic{
			Rel:     label,
			Line:    1,
			Message: label + ": no call to secutil.ValidateTLSEndpoint (" + secFailClosed03 + ")",
		})
	}
	return diags
}

// secutilCallsValidateTLSEndpoint reports whether src contains an actual
// *ast.CallExpr to secutil.ValidateTLSEndpoint. Comment / string-literal
// occurrences of the bytes do not count.
func secutilCallsValidateTLSEndpoint(src string) bool {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "src.go", src, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	_, ok := scanner.FindFirstInSubtree[ast.CallExpr](f, func(ce *ast.CallExpr) bool {
		sel, isSel := ce.Fun.(*ast.SelectorExpr)
		if !isSel {
			return false
		}
		x, isIdent := sel.X.(*ast.Ident)
		if !isIdent {
			return false
		}
		return x.Name == "secutil" && sel.Sel.Name == "ValidateTLSEndpoint"
	})
	return ok
}

// ---------------------------------------------------------------------------
// SEC-04: websocket InsecureSkipVerify must not be true
// ---------------------------------------------------------------------------

func secCheckSEC04(t *testing.T, root string) []Diagnostic {
	t.Helper()
	wsDir := filepath.Join(root, "adapters", "websocket")
	files, err := findProductionGoFilesInDir(wsDir)
	if err != nil {
		t.Fatalf("%s: reading adapters/websocket: %v", secFailClosed04, err)
	}
	var diags []Diagnostic
	for _, f := range files {
		hits, err := findInsecureSkipVerifyAssign(f)
		if err != nil {
			t.Fatalf("%s: scanning %s: %v", secFailClosed04, f, err)
		}
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		for _, line := range hits {
			diags = append(diags, Diagnostic{
				Rel:     rel,
				Line:    line,
				Message: "opts.InsecureSkipVerify = true (" + secFailClosed04 + ")",
			})
		}
	}
	return diags
}

// findInsecureSkipVerifyAssign parses path and returns line numbers of every
// AssignStmt of the form `opts.InsecureSkipVerify = true`.
func findInsecureSkipVerifyAssign(path string) ([]int, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var lines []int
	scanner.EachInSubtree[ast.AssignStmt](f, func(assign *ast.AssignStmt) {
		if line, ok := secInsecureSkipVerifyLine(fset, assign); ok {
			lines = append(lines, line)
		}
	})
	return lines, nil
}

// secInsecureSkipVerifyLine reports whether assign is `opts.InsecureSkipVerify = true`
// and returns the line number if so.
func secInsecureSkipVerifyLine(fset *token.FileSet, assign *ast.AssignStmt) (int, bool) {
	if len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return 0, false
	}
	// LHS: opts.InsecureSkipVerify — SelectorExpr X=Ident("opts") Sel="InsecureSkipVerify"
	sel, ok := assign.Lhs[0].(*ast.SelectorExpr)
	if !ok {
		return 0, false
	}
	xIdent, ok := sel.X.(*ast.Ident)
	if !ok || xIdent.Name != "opts" {
		return 0, false
	}
	if sel.Sel.Name != "InsecureSkipVerify" {
		return 0, false
	}
	rhs, ok := assign.Rhs[0].(*ast.Ident)
	if !ok || rhs.Name != "true" {
		return 0, false
	}
	return fset.Position(assign.Pos()).Line, true
}

// ---------------------------------------------------------------------------
// SEC-05: example docker compose credentials from env
// ---------------------------------------------------------------------------

func secCheckSEC05(t *testing.T, root string) []Diagnostic {
	t.Helper()
	violations := findExampleComposeCredentialViolations(t, root)
	var diags []Diagnostic
	for _, v := range violations {
		diags = append(diags, Diagnostic{
			Rel:     v,
			Line:    1,
			Message: v + " (" + secFailClosed05 + ")",
		})
	}
	return diags
}

// findExampleComposeCredentialViolations scans every docker-compose.yml under
// examples/ (recursive, any depth) for committed credential literals. Uses
// scanner.DirsScope("examples") with a MatchRels predicate keyed only on
// filename — depth is intentionally NOT constrained: a hardcoded password in
// examples/<example>/deploy/docker-compose.yml leaks credentials just as
// surely as one at the top level. Strict improvement over the pre-Path-C
// two-level os.ReadDir loop, which failed open on nested compose files.
//
// Returns []string so the standalone fixture tests in security_defaults_test.go
// (TestSEC05ExampleComposeCredentials*) can directly assert string content.
// CheckSecurityDefaults converts these to []Diagnostic via secCheckSEC05.
func findExampleComposeCredentialViolations(t *testing.T, root string) []string {
	t.Helper()
	scope := scanner.DirsScope(
		root, []string{"examples"},
		scanner.MatchRels(func(rel string) bool {
			return filepath.Base(rel) == "docker-compose.yml"
		}),
	)
	var violations []string
	scanner.EachContentFile(t, scope, []string{".yml"}, func(_ *testing.T, fc scanner.ContentContext) {
		violations = append(violations, scanComposeCredentialViolations(fc.Rel, fc.Bytes)...)
	})
	return violations
}

// scanComposeCredentialViolations inspects compose YAML bytes for committed
// credential literals (any line whose key matches isComposeCredentialKey
// must use ${VAR:?required} env interpolation). Decoupled from file reading
// so callers funneled through scanner.EachContentFile can pass bytes directly.
func scanComposeCredentialViolations(rel string, data []byte) []string {
	var violations []string
	for i, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok || !isComposeCredentialKey(key) {
			continue
		}
		if !isRequiredComposeEnvInterpolation(value) {
			violations = append(violations,
				fmt.Sprintf("%s:%d: %s must use required environment interpolation ${VAR:?message} (%s)",
					rel, i+1, key, secFailClosed05))
		}
	}
	return violations
}

func isComposeCredentialKey(key string) bool {
	return strings.Contains(key, "PASSWORD") || strings.HasSuffix(key, "_PASS")
}

func isRequiredComposeEnvInterpolation(value string) bool {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	if !strings.HasPrefix(value, "${") || !strings.HasSuffix(value, "}") {
		return false
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(value, "${"), "}")
	name, message, ok := strings.Cut(inner, ":?")
	if !ok || name == "" || message == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// SEC-07: websocket UpgradeConfig must set Authenticator
// ---------------------------------------------------------------------------

func secCheckSEC07(t *testing.T, root string) []Diagnostic {
	t.Helper()
	files, err := findAllProductionGoFiles(root)
	if err != nil {
		t.Fatalf("%s: collecting production files: %v", secFailClosed07, err)
	}
	var diags []Diagnostic
	for _, f := range files {
		hits, err := findUpgradeConfigWithoutAuthenticator(f)
		if err != nil {
			t.Fatalf("%s: scanning %s: %v", secFailClosed07, f, err)
		}
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		for _, line := range hits {
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: "UpgradeConfig literal missing Authenticator field (" +
					secFailClosed07 + ")",
			})
		}
	}
	return diags
}

func findUpgradeConfigWithoutAuthenticator(path string) ([]int, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var lines []int
	scanner.EachInSubtree[ast.CompositeLit](f, func(cl *ast.CompositeLit) {
		if !isUpgradeConfigType(cl.Type) {
			return
		}
		if hasKey(cl, "Authenticator") {
			return
		}
		lines = append(lines, fset.Position(cl.Pos()).Line)
	})
	return lines, nil
}

func isUpgradeConfigType(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "UpgradeConfig"
	case *ast.SelectorExpr:
		return t.Sel != nil && t.Sel.Name == "UpgradeConfig"
	}
	return false
}

// hasKey reports whether cl has a TOP-LEVEL key field equal to key.
// FindFirstChild visits only direct children of cl (depth-1), so nested
// composites (e.g. `Other: Sub{Authenticator: ...}`) are not reached.
func hasKey(cl *ast.CompositeLit, key string) bool {
	_, found := scanner.FindFirstChild[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) bool {
		ident, ok := kv.Key.(*ast.Ident)
		return ok && ident.Name == key
	})
	return found
}

// ---------------------------------------------------------------------------
// SEC-08: no legacy Hub.Broadcast call
// ---------------------------------------------------------------------------

func secCheckSEC08(t *testing.T, root string) []Diagnostic {
	t.Helper()
	files, err := findAllProductionGoFiles(root)
	if err != nil {
		t.Fatalf("%s: collecting production files: %v", secFailClosed08, err)
	}
	var diags []Diagnostic
	for _, f := range files {
		hits, err := findLegacyBroadcastCalls(f)
		if err != nil {
			t.Fatalf("%s: scanning %s: %v", secFailClosed08, f, err)
		}
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		for _, line := range hits {
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: "legacy Hub.Broadcast call (use BroadcastFilter or BroadcastToSubject; " +
					secFailClosed08 + ")",
			})
		}
	}
	return diags
}

func findLegacyBroadcastCalls(path string) ([]int, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	// Skip files that don't import runtime/websocket (no chance of a Hub.Broadcast call).
	if !bytes.Contains(data, []byte(secWebsocketImportLiteral)) {
		return nil, nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var lines []int
	scanner.EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		if sel.Sel == nil || sel.Sel.Name != "Broadcast" {
			return
		}
		// BroadcastFilter / BroadcastToSubject have different Sel.Name, so they pass.
		lines = append(lines, fset.Position(call.Pos()).Line)
	})
	return lines, nil
}

// ---------------------------------------------------------------------------
// SEC-09: hub.go conns and subjectIdx delete points must stay in sync
// ---------------------------------------------------------------------------

// allowedConnsMutationFuncs lists hub.go function names where direct mutation
// of h.conns (delete / clear) is permitted. Every other function must route
// through removeConnLocked. shutdown is allowed because its bulk drain pairs
// clear(h.conns) with clear(h.subjectIdx) in adjacent statements; this
// colocation cannot be enforced via a per-function boolean check, hence the
// function-name allowlist.
var allowedConnsMutationFuncs = map[string]bool{
	"removeConnLocked": true,
	"shutdown":         true,
}

func secCheckSEC09(t *testing.T, root string) []Diagnostic {
	t.Helper()
	path := filepath.Join(root, "runtime", "websocket", "hub.go")
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("%s: reading hub.go: %v", secFailClosed09, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("%s: parsing hub.go: %v", secFailClosed09, err)
	}
	var diags []Diagnostic
	scanner.EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
		if fn.Body == nil || allowedConnsMutationFuncs[fn.Name.Name] {
			return
		}
		diags = append(diags, secCheckSEC09FuncBody(fset, fn)...)
	})
	return diags
}

// secCheckSEC09FuncBody scans a single function body for direct delete/clear
// calls on h.conns outside of the allowed mutation functions.
func secCheckSEC09FuncBody(fset *token.FileSet, fn *ast.FuncDecl) []Diagnostic {
	var diags []Diagnostic
	scanner.EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
		ident, ok := call.Fun.(*ast.Ident)
		if !ok || (ident.Name != "delete" && ident.Name != "clear") {
			return
		}
		if len(call.Args) < 1 {
			return
		}
		sel, ok := call.Args[0].(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "conns" {
			return
		}
		line := fset.Position(call.Pos()).Line
		diags = append(diags, Diagnostic{
			Rel:  "runtime/websocket/hub.go",
			Line: line,
			Message: fmt.Sprintf(
				"%s() must not call %s(h.conns,...) directly; "+
					"use removeConnLocked() helper (or, for bulk drain, place inside shutdown). [%s]",
				fn.Name.Name, ident.Name, secFailClosed09),
		})
	})
	return diags
}

// ---------------------------------------------------------------------------
// SEC-10: health listener required in package main
// ---------------------------------------------------------------------------

func secCheckSEC10(t *testing.T) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	diags = append(diags, sec10MainScan(t)...)
	diags = append(diags, sec10DotImportScan(t)...)
	return diags
}

// sec10MainScan checks that every package main wiring cell.PrimaryListener also
// wires cell.HealthListener.
func sec10MainScan(t *testing.T) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	_ = Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		for _, v := range sec10Violations(p) {
			diags = append(diags, Diagnostic{
				Rel:     p.Pkg.Path(),
				Line:    1,
				Message: v,
			})
		}
		return nil
	})
	return diags
}

// sec10DotImportScan closes the dot-import blind spot of listenerRefsInPass.
func sec10DotImportScan(t *testing.T) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	_ = Run(t, Production(TypedOpts{Tests: false}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Name() != "main" {
			return nil
		}
		for _, hit := range dotImportKernelCellHits(p) {
			// hit is "rel:line" formatted — parse it to extract rel and line.
			rel, lineStr, hasSep := strings.Cut(hit, ":")
			if !hasSep {
				rel = hit
				lineStr = "1"
			}
			line := 1
			if n := parseInt(lineStr); n > 0 {
				line = n
			}
			diags = append(diags, Diagnostic{
				Rel:     rel,
				Line:    line,
				Message: secFailClosed10 + ": package main must not dot-import kernel/cell",
			})
		}
		return nil
	})
	return diags
}

// parseInt parses a decimal string to int, returning 0 on error.
func parseInt(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// listenerRefsInPass reports whether the package in p references the kernel/cell
// PrimaryListener / HealthListener consts (anywhere — not only as WithListener
// arguments, since composition roots like examples/ssobff route the ref through
// a listenerOption helper). primaryLine is the line of the first PrimaryListener
// reference, for diagnostics.
//
// Resolution is type-aware via ResolvePackageRef → canonical *types.PkgName →
// import path, so an aliased `import kcell ".../kernel/cell"` is handled and a
// bare `.Sel.Name == "HealthListener"` (Soft name-convention) match is avoided.
//
// cell.ListenerRef is `type ListenerRef struct{ name string }` with an
// unexported field. Outside kernel/cell a ListenerRef value can ONLY be obtained
// by referencing one of the exported consts (PrimaryListener / InternalListener /
// HealthListener): `cell.ListenerRef(s)` is not a valid conversion (string→struct)
// and the struct literal is unconstructable (unexported field). Fabricating a
// listener ref from a dynamic string is therefore type-system unexpressable in any
// composition root and needs no archtest self-check. The one shape this
// *ast.SelectorExpr walk would still miss is a dot-import `import . ".../kernel/cell"`
// referencing the consts as bare idents; that is closed by
// testSEC10NoDotImportKernelCellBlindSpotInProduction.
func listenerRefsInPass(p *Pass) (primary, health bool, primaryLine int) {
	for _, file := range p.Files {
		if strings.HasSuffix(p.Rel(file), "_test.go") {
			continue
		}
		EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
			pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, sel)
			if !ok || pkgPath != kernelCellPkgPath {
				return
			}
			switch name {
			case "PrimaryListener":
				primary = true
				if primaryLine == 0 {
					primaryLine = p.Fset.Position(sel.Pos()).Line
				}
			case "HealthListener":
				health = true
			}
		})
	}
	return primary, health, primaryLine
}

// sec10Violations runs the full SEC-FAIL-CLOSED-10 rule against a single typed
// pass: a `package main` that references cell.PrimaryListener (wires the public
// listener) must also reference cell.HealthListener. It returns one diagnostic
// string per violating package (nil for compliant or non-main packages).
//
// Both the production scan (testSEC10HealthListenerRequiredInMain) and the
// positive-coverage fixture test (testSEC10FixtureCatchesPrimaryWithoutHealth)
// drive this one function, so the fixture exercises the real rule path — the
// package-main gate AND the primary-without-health detection — not merely the
// listenerRefsInPass helper. A regression in the gate or the violation
// construction is caught by the fixture's "exactly one violation" assertion.
//
// Returns []string (not []Diagnostic) so that the standalone fixture test
// testSEC10FixtureCatchesPrimaryWithoutHealth (which stays in _test.go) can
// directly assert on string content using assert.Contains.
func sec10Violations(p *Pass) []string {
	if p.Pkg == nil || p.Pkg.Name() != "main" {
		return nil
	}
	primary, health, line := listenerRefsInPass(p)
	if primary && !health {
		return []string{
			fmt.Sprintf("%s (references cell.PrimaryListener at line %d but never cell.HealthListener)", p.Pkg.Path(), line),
		}
	}
	return nil
}

// dotImportKernelCellHits reports `import . "<kernel/cell>"` dot-imports in p —
// the second blind spot of listenerRefsInPass (a bare PrimaryListener/
// HealthListener ident under a dot-import would not be a *ast.SelectorExpr the
// scan walks). Composition roots import kernel/cell qualified, so this is empty.
//
// Returns []string in "rel:line" format for use by sec10DotImportScan (which
// parses the format) and testSEC10NoDotImportKernelCellBlindSpotInProduction
// (which logs the strings as blind spots).
func dotImportKernelCellHits(p *Pass) []string {
	var hits []string
	for _, file := range p.Files {
		if strings.HasSuffix(p.Rel(file), "_test.go") {
			continue
		}
		for _, imp := range file.Imports {
			if imp.Name == nil || imp.Name.Name != "." {
				continue
			}
			if strings.Trim(imp.Path.Value, `"`) == kernelCellPkgPath {
				hits = append(hits, fmt.Sprintf("%s:%d", p.Rel(file), p.Fset.Position(imp.Pos()).Line))
			}
		}
	}
	return hits
}

// ---------------------------------------------------------------------------
// findAllProductionMainPackageFiles — used by SEC-02 and SEC-06
// ---------------------------------------------------------------------------

// findAllProductionMainPackageFiles walks the repo and returns every
// production .go file (non-test, non-vendor, non-generated) whose package
// clause is `main`. New entry points (cmd/<name>/main.go,
// examples/<name>/main.go, tests/<harness>/main.go, ...) are picked up
// automatically — no scope list to maintain.
//
// Parse errors fail-visible (callers receive an error) so a syntactically
// broken entry point cannot silently bypass the SEC scans.
func findAllProductionMainPackageFiles(root string) ([]string, error) {
	// ModuleScope's default skip set already excludes vendor/testdata/
	// worktrees/generated/.git/node_modules — sufficient for the
	// production-main scan. (ExcludeRels only matches exact file rels, not
	// directories; the previous ExcludeRels("bak") call was a no-op and
	// has been removed. If a future "bak/" directory needs skipping, use
	// MatchRels with a path-segment predicate.)
	scope := scanner.ModuleScope(root)
	candidates, err := scope.Files()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, path := range candidates {
		fset := token.NewFileSet()
		af, perr := parser.ParseFile(fset, path, nil, parser.PackageClauseOnly)
		if perr != nil {
			return nil, fmt.Errorf("parse %s: %w", path, perr)
		}
		if af.Name != nil && af.Name.Name == "main" {
			files = append(files, path)
		}
	}
	return files, nil
}
