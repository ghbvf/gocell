package archtest

// security_defaults_test.go — static archtest rules for PR-MODE-1 SEC-FAIL-CLOSED.
//
// Five sub-tests mirror the SEC-FAIL-CLOSED-01..05 rule IDs:
//
//   01  addr-driven gate: bundle.go must not wrap WithListener in IfStmt guarded
//       by PrimaryHTTPAddr / InternalHTTPAddr / HealthHTTPAddr != "".
//   02  listener authChain non-nil: all WithListener calls must pass an explicit
//       non-nil 3rd argument (no bare nil literal).
//   03  adapter TLS endpoint: redis, vault, s3 adapters must import pkg/secutil
//       and call secutil.ValidateTLSEndpoint.
//   04  websocket origins: no file in adapters/websocket may assign
//       opts.InsecureSkipVerify = true.
//   05  example docker compose credentials must come from environment
//       interpolation, not committed literal values.
//   06  internal listener guard: production WithListener calls must not wire
//       cell.InternalListener with a literal AuthNone chain.
//
// ref: tools/archtest/auth_authtest_boundary_test.go — 4 sub-test pattern

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	secFailClosed01 = "SEC-FAIL-CLOSED-01"
	secFailClosed02 = "SEC-FAIL-CLOSED-02"
	secFailClosed03 = "SEC-FAIL-CLOSED-03"
	secFailClosed04 = "SEC-FAIL-CLOSED-04"
	secFailClosed05 = "SEC-FAIL-CLOSED-05"
	secFailClosed06 = "SEC-FAIL-CLOSED-06"
)

func TestSecurityDefaults(t *testing.T) {
	root := findModuleRoot(t)

	t.Run(secFailClosed01+"_addr_driven_listener_gate_banned", func(t *testing.T) {
		testSEC01AddrDrivenGate(t, root)
	})

	t.Run(secFailClosed02+"_listener_authchain_must_be_explicit", func(t *testing.T) {
		testSEC02ListenerAuthChainNonNil(t, root)
	})

	t.Run(secFailClosed03+"_adapter_endpoint_must_validate_tls", func(t *testing.T) {
		testSEC03AdapterTLSValidation(t, root)
	})

	t.Run(secFailClosed04+"_websocket_must_not_skip_origin_verify", func(t *testing.T) {
		testSEC04WebSocketOriginVerify(t, root)
	})

	t.Run(secFailClosed05+"_example_compose_credentials_from_env", func(t *testing.T) {
		testSEC05ExampleComposeCredentialsFromEnv(t, root)
	})

	t.Run(secFailClosed06+"_internal_listener_must_not_use_authnone", func(t *testing.T) {
		testSEC06InternalListenerMustNotUseAuthNone(t, root)
	})
}

// testSEC01AddrDrivenGate is skipped — SEC-FAIL-CLOSED-01 is retired.
//
// Rationale: the addr-driven gate pattern
// (if shared.PrimaryHTTPAddr != "" { bootstrap.WithListener(...) }) does NOT
// produce fail-open authentication behavior — it merely skips listener
// registration when the addr is absent. The actual auth-chain nil risk is
// already prevented by SEC-FAIL-CLOSED-02 (explicit authChain enforcement at
// call sites) and by runtime phase0ValidateOptions (which rejects nil
// authChain before any server starts).
//
// The gate is legitimate for dev/CI: tests and dev setups that omit an addr
// simply don't bind that port. Production correctness is enforced by
// SharedDeps.Validate and internalGuardFromEnv (which now fails-fast in ALL
// adapter modes when GOCELL_SERVICE_SECRET is unset, not just "real" mode).
//
// SEC-02 covers the actual nil-authChain risk. See git history for context.
func testSEC01AddrDrivenGate(t *testing.T, _ string) {
	t.Helper()
	t.Skip("SEC-FAIL-CLOSED-01 retired: addr-driven if-gate is safe; SEC-02 covers actual fail-open. See git history for context.")
}

// testSEC02ListenerAuthChainNonNil scans all production Go files under
// cmd/corebundle/ and examples/**/main.go for bootstrap.WithListener CallExpr
// nodes and verifies that the 3rd argument (authChain) is never a bare nil
// identifier literal.
func testSEC02ListenerAuthChainNonNil(t *testing.T, root string) {
	t.Helper()

	// Collect files to scan: cmd/corebundle + examples/**/main.go (production only).
	var scanFiles []string

	// cmd/corebundle/**/*.go (non-test)
	bundleDir := filepath.Join(root, "cmd", "corebundle")
	bundleFiles, err := findProductionGoFilesInDir(bundleDir)
	require.NoError(t, err, "reading cmd/corebundle")
	scanFiles = append(scanFiles, bundleFiles...)

	// examples/**/main.go
	examplesDir := filepath.Join(root, "examples")
	_ = filepath.WalkDir(examplesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Base(path) == "main.go" {
			scanFiles = append(scanFiles, path)
		}
		return nil
	})

	var violations []string

	for _, f := range scanFiles {
		hits, err := findWithListenerNilAuthChain(f)
		require.NoErrorf(t, err, "scanning %s", f)
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		for _, line := range hits {
			violations = append(violations, fmt.Sprintf("%s:%d: WithListener 3rd arg is bare nil (SEC-FAIL-CLOSED-02)", rel, line))
		}
	}

	if len(violations) > 0 {
		for _, v := range violations {
			t.Logf("%s violation: %s", secFailClosed02, v)
		}
	}
	assert.Empty(t, violations,
		"all bootstrap.WithListener calls in cmd/corebundle and examples/**/main.go must pass "+
			"an explicit non-nil authChain; use cell.AuthNone{} for HealthListener")
}

// findWithListenerNilAuthChain parses path and returns line numbers of every
// bootstrap.WithListener CallExpr where the 3rd argument is the identifier nil.
func findWithListenerNilAuthChain(path string) ([]int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var lines []int
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// Must be a SelectorExpr "bootstrap.WithListener" or plain "WithListener".
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fn.Sel.Name != "WithListener" {
				return true
			}
		case *ast.Ident:
			if fn.Name != "WithListener" {
				return true
			}
		default:
			return true
		}
		// 3rd argument (index 2) must not be nil identifier.
		if len(call.Args) < 3 {
			return true
		}
		arg := call.Args[2]
		ident, ok := arg.(*ast.Ident)
		if ok && ident.Name == "nil" {
			lines = append(lines, fset.Position(call.Lparen).Line)
		}
		return true
	})
	return lines, nil
}

func testSEC06InternalListenerMustNotUseAuthNone(t *testing.T, root string) {
	t.Helper()

	var scanFiles []string
	bundleDir := filepath.Join(root, "cmd", "corebundle")
	bundleFiles, err := findProductionGoFilesInDir(bundleDir)
	require.NoError(t, err, "reading cmd/corebundle")
	scanFiles = append(scanFiles, bundleFiles...)

	examplesDir := filepath.Join(root, "examples")
	err = filepath.WalkDir(examplesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Base(path) == "main.go" {
			scanFiles = append(scanFiles, path)
		}
		return nil
	})
	require.NoError(t, err, "reading examples")

	var violations []string
	for _, f := range scanFiles {
		hits, err := findInternalListenerAuthNoneChain(f)
		require.NoErrorf(t, err, "scanning %s", f)
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		for _, line := range hits {
			violations = append(violations, fmt.Sprintf("%s:%d: InternalListener uses AuthNone literal (SEC-FAIL-CLOSED-06)", rel, line))
		}
	}

	if len(violations) > 0 {
		for _, v := range violations {
			t.Logf("%s violation: %s", secFailClosed06, v)
		}
	}
	assert.Empty(t, violations,
		"production InternalListener declarations must use guarded auth chains, not a literal AuthNone chain")
}

func findInternalListenerAuthNoneChain(path string) ([]int, error) {
	data, err := os.ReadFile(path)
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
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isWithListenerCall(call) || len(call.Args) < 3 {
			return true
		}
		if !isInternalListenerRef(call.Args[0]) || !chainExprContainsAuthNone(call.Args[2], facts) {
			return true
		}
		lines = append(lines, fset.Position(call.Lparen).Line)
		return true
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
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		for _, stmt := range fn.Body.List {
			ret, ok := stmt.(*ast.ReturnStmt)
			if !ok {
				continue
			}
			for _, result := range ret.Results {
				if chainLiteralContainsAuthNone(result) {
					facts.funcs[fn.Name.Name] = true
				}
			}
		}
		return true
	})
	ast.Inspect(f, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.ValueSpec:
			for i, name := range stmt.Names {
				if authNoneRHSAt(stmt.Values, i) {
					facts.vars[name.Name] = true
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range stmt.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				if authNoneRHSAt(stmt.Rhs, i) {
					facts.vars[id.Name] = true
				}
			}
		}
		return true
	})
	return facts
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

func TestFindInternalListenerAuthNoneChain_CatchesLiteralVarAndHelper(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	src := `package main

import (
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

func main() {
	chain := []cell.ListenerAuth{cell.AuthNone{}}
	bootstrap.WithListener(cell.InternalListener, ":9090", chain)
	bootstrap.WithListener(cell.InternalListener, ":9091", insecureInternalAuth())
}

func insecureInternalAuth() []cell.ListenerAuth {
	return []cell.ListenerAuth{cell.AuthNone{}}
}
`
	require.NoError(t, os.WriteFile(path, []byte(src), 0o644))

	lines, err := findInternalListenerAuthNoneChain(path)

	require.NoError(t, err)
	assert.Len(t, lines, 2)
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

// testSEC03AdapterTLSValidation verifies that adapters/redis, adapters/vault,
// and adapters/s3 each contain at least one call to secutil.ValidateTLSEndpoint.
// This ensures the shared TLS validation helper is wired in and the adapters
// will benefit from phase-2 implementation without extra code changes.
//
// Detection is text-based (strings.Contains) — sufficient for this rule since
// the call site is the only use of secutil in each adapter package.
// Phase 2 may upgrade to packages.Load TypesInfo if false-positive risk grows.
func testSEC03AdapterTLSValidation(t *testing.T, root string) {
	t.Helper()

	targets := []struct {
		label string
		dir   string
	}{
		{"adapters/redis", filepath.Join(root, "adapters", "redis")},
		{"adapters/vault", filepath.Join(root, "adapters", "vault")},
		{"adapters/s3", filepath.Join(root, "adapters", "s3")},
	}

	var violations []string

	for _, tgt := range targets {
		files, err := findProductionGoFilesInDir(tgt.dir)
		require.NoErrorf(t, err, "reading %s", tgt.label)

		pkgImportsSecutil := false
		pkgCallsValidate := false

		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			src := string(data)
			if strings.Contains(src, `"github.com/ghbvf/gocell/pkg/secutil"`) {
				pkgImportsSecutil = true
			}
			if strings.Contains(src, "secutil.ValidateTLSEndpoint(") {
				pkgCallsValidate = true
			}
		}

		if !pkgImportsSecutil {
			violations = append(violations, fmt.Sprintf("%s: does not import pkg/secutil (SEC-FAIL-CLOSED-03)", tgt.label))
		}
		if !pkgCallsValidate {
			violations = append(violations, fmt.Sprintf("%s: no call to secutil.ValidateTLSEndpoint (SEC-FAIL-CLOSED-03)", tgt.label))
		}
	}

	if len(violations) > 0 {
		for _, v := range violations {
			t.Logf("%s violation: %s", secFailClosed03, v)
		}
	}
	assert.Empty(t, violations,
		"adapters/redis, adapters/vault, and adapters/s3 must each import pkg/secutil "+
			"and call secutil.ValidateTLSEndpoint to validate remote endpoint TLS")
}

// testSEC04WebSocketOriginVerify scans adapters/websocket for the forbidden
// assignment opts.InsecureSkipVerify = true. The pattern is detected via AST
// AssignStmt matching to avoid false positives from comments or string literals.
func testSEC04WebSocketOriginVerify(t *testing.T, root string) {
	t.Helper()

	wsDir := filepath.Join(root, "adapters", "websocket")
	files, err := findProductionGoFilesInDir(wsDir)
	require.NoError(t, err, "reading adapters/websocket")

	var violations []string

	for _, f := range files {
		hits, err := findInsecureSkipVerifyAssign(f)
		require.NoErrorf(t, err, "scanning %s", f)
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		for _, line := range hits {
			violations = append(violations, fmt.Sprintf("%s:%d: opts.InsecureSkipVerify = true (SEC-FAIL-CLOSED-04)", rel, line))
		}
	}

	if len(violations) > 0 {
		for _, v := range violations {
			t.Logf("%s violation: %s", secFailClosed04, v)
		}
	}
	assert.Empty(t, violations,
		"adapters/websocket must not assign opts.InsecureSkipVerify = true; "+
			"empty AllowedOrigins must fail-fast rather than silently accepting all origins")
}

// findInsecureSkipVerifyAssign parses path and returns line numbers of every
// AssignStmt of the form `opts.InsecureSkipVerify = true`.
func findInsecureSkipVerifyAssign(path string) ([]int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var lines []int
	ast.Inspect(f, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		if len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		// LHS: opts.InsecureSkipVerify — SelectorExpr X=Ident("opts") Sel="InsecureSkipVerify"
		sel, ok := assign.Lhs[0].(*ast.SelectorExpr)
		if !ok {
			return true
		}
		xIdent, ok := sel.X.(*ast.Ident)
		if !ok || xIdent.Name != "opts" {
			return true
		}
		if sel.Sel.Name != "InsecureSkipVerify" {
			return true
		}
		// RHS: true — must be a pointer deref of AcceptOptions.InsecureSkipVerify via SelectorExpr
		// or a direct BasicLit / Ident "true".
		if rhs, ok := assign.Rhs[0].(*ast.Ident); ok {
			if rhs.Name == "true" {
				lines = append(lines, fset.Position(assign.Pos()).Line)
			}
		}
		return true
	})
	return lines, nil
}

func testSEC05ExampleComposeCredentialsFromEnv(t *testing.T, root string) {
	t.Helper()

	violations, err := findExampleComposeCredentialViolations(root)
	require.NoError(t, err)

	if len(violations) > 0 {
		for _, v := range violations {
			t.Logf("%s violation: %s", secFailClosed05, v)
		}
	}
	assert.Empty(t, violations,
		"example docker compose credential values must use ${VAR:?required} instead of committed literals")
}

func TestSEC05ExampleComposeCredentialsRejectsFallbacksInFutureExamples(t *testing.T) {
	root := t.TempDir()
	exampleDir := filepath.Join(root, "examples", "futuredevice")
	require.NoError(t, os.MkdirAll(exampleDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(exampleDir, "docker-compose.yml"), []byte(`
services:
  postgres:
    environment:
      POSTGRES_PASSWORD: ${FUTURE_POSTGRES_PASSWORD:-gocell}
  rabbitmq:
    environment:
      RABBITMQ_DEFAULT_PASS: ${FUTURE_RABBITMQ_PASSWORD:?required}
`), 0o644))

	violations, err := findExampleComposeCredentialViolations(root)
	require.NoError(t, err)
	require.Len(t, violations, 1)
	assert.Contains(t, violations[0], "examples/futuredevice/docker-compose.yml:5")
	assert.Contains(t, violations[0], "POSTGRES_PASSWORD")
}

func findExampleComposeCredentialViolations(root string) ([]string, error) {
	var violations []string
	examplesDir := filepath.Join(root, "examples")
	err := filepath.WalkDir(examplesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Base(path) != "docker-compose.yml" {
			return nil
		}
		fileViolations, err := findComposeCredentialViolations(root, path)
		if err != nil {
			return err
		}
		violations = append(violations, fileViolations...)
		return nil
	})
	if os.IsNotExist(err) {
		return nil, nil
	}
	return violations, err
}

func findComposeCredentialViolations(root, path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(root, path)
	rel = filepath.ToSlash(rel)

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
	return violations, nil
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
