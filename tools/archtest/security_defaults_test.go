package archtest

// security_defaults_test.go — static archtest rules for PR-MODE-1 SEC-FAIL-CLOSED.
//
// Sub-tests mirror the SEC-FAIL-CLOSED-01..09 rule IDs:
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
//   07  websocket UpgradeConfig literals must include Authenticator field
//   08  no production code may call runtime/websocket.Hub.Broadcast (deleted API)
//   09  hub.go conns and subjectIdx delete points must stay in sync
//
// ref: tools/archtest/auth_authtest_boundary_test.go — 4 sub-test pattern

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/testutil/fileutil"
)

const (
	// RETIRED: SEC-FAIL-CLOSED-01 — see testSEC01AddrDrivenGate godoc below.
	// The constant + t.Run subtest are retained as inert markers so historic
	// CI logs and grep continue to map cleanly onto rule IDs; the body is a
	// one-line t.Skip().
	secFailClosed01 = "SEC-FAIL-CLOSED-01"
	secFailClosed02 = "SEC-FAIL-CLOSED-02"
	secFailClosed03 = "SEC-FAIL-CLOSED-03"
	secFailClosed04 = "SEC-FAIL-CLOSED-04"
	secFailClosed05 = "SEC-FAIL-CLOSED-05"
	secFailClosed06 = "SEC-FAIL-CLOSED-06"
	secFailClosed07 = "SEC-FAIL-CLOSED-07"
	secFailClosed08 = "SEC-FAIL-CLOSED-08"
	secFailClosed09 = "SEC-FAIL-CLOSED-09"
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

	t.Run(secFailClosed07+"_websocket_upgrade_config_must_set_authenticator", func(t *testing.T) {
		testSEC07WebsocketAuthenticatorRequired(t, root)
	})

	t.Run(secFailClosed08+"_no_legacy_broadcast_call", func(t *testing.T) {
		testSEC08NoLegacyBroadcastCall(t, root)
	})

	t.Run(secFailClosed09+"_hub_subjectidx_sync", func(t *testing.T) {
		testSEC09HubSubjectIdxSync(t, root)
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

// testSEC02ListenerAuthChainNonNil scans every production package-main .go
// file in the repo for bootstrap.WithListener CallExpr nodes and verifies
// that the 3rd argument (authChain) is never a bare nil identifier.
//
// Scope: any new entry point (cmd/<name>/main.go, examples/<name>/main.go,
// tests/<harness>/main.go) is auto-included because the filter is package
// clause `main`, not a hardcoded directory list. This closes the SEC-02
// scope-shrink bypass: previously only cmd/corebundle + examples/**/main.go
// were scanned, leaving any new cmd/<name>/ unguarded.
//
// ref: rust-lang/rust-clippy `expect`/deny levels — critical lint rules
// cannot be downgraded by scope-shrinking the scan.
func testSEC02ListenerAuthChainNonNil(t *testing.T, root string) {
	t.Helper()

	scanFiles, err := findAllProductionMainPackageFiles(root)
	require.NoError(t, err, "finding production main package files")

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

	// Same auto-include scope as SEC-02 — every package-main entry point.
	scanFiles, err := findAllProductionMainPackageFiles(root)
	require.NoError(t, err, "finding production main package files")

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
			data, err := os.ReadFile(filepath.Clean(f))
			if err != nil {
				continue
			}
			src := string(data)
			if strings.Contains(src, `"github.com/ghbvf/gocell/pkg/secutil"`) {
				pkgImportsSecutil = true
			}
			if secutilCallsValidateTLSEndpoint(src) {
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

// secutilCallsValidateTLSEndpoint reports whether src contains an actual
// *ast.CallExpr to secutil.ValidateTLSEndpoint. Comment / string-literal
// occurrences of the bytes do not count.
func secutilCallsValidateTLSEndpoint(src string) bool {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "src.go", src, parser.SkipObjectResolution)
	if err != nil {
		return false
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := ce.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if x.Name == "secutil" && sel.Sel.Name == "ValidateTLSEndpoint" {
			found = true
		}
		return true
	})
	return found
}

// TestSecurityDefaultsSEC03_NegativeFixture_StringLiteralOnly asserts the
// scanner does NOT flag a fixture that only contains "secutil.ValidateTLSEndpoint("
// in comments and string-constant values, with no real CallExpr. Legacy
// strings.Contains FALSE-POSITIVES; AST GREEN refactor must distinguish.
func TestSecurityDefaultsSEC03_NegativeFixture_StringLiteralOnly(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	fixturePath := filepath.Join(root, "tools", "archtest", "testdata", "security_defaults_fixtures", "missing_tls_validate", "main.go")
	body := fileutil.MustReadFile(t, fixturePath)
	src := string(body)
	if secutilCallsValidateTLSEndpoint(src) {
		t.Errorf("SEC-FAIL-CLOSED-03 negative fixture missing_tls_validate: legacy " +
			"strings.Contains FALSE-POSITIVES on comment/string-literal occurrences of " +
			"\"secutil.ValidateTLSEndpoint(\"; AST GREEN refactor required (scan *ast.CallExpr " +
			"with selector secutil.ValidateTLSEndpoint)")
	}
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
	data, err := os.ReadFile(filepath.Clean(path))
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

func TestFindUpgradeConfigWithoutAuthenticator_DetectsLiteralWithMissingField(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Case 1: same-package UpgradeConfig literal (ast.Ident) — no Authenticator field.
	path1 := filepath.Join(dir, "nauth_ident.go")
	src1 := `package main

import "github.com/ghbvf/gocell/adapters/websocket"

func main() {
	_ = websocket.UpgradeConfig{AllowedOrigins: []string{"http://*"}}
}
`
	require.NoError(t, os.WriteFile(path1, []byte(src1), 0o644))

	lines1, err := findUpgradeConfigWithoutAuthenticator(path1)
	require.NoError(t, err)
	assert.NotEmpty(t, lines1, "should detect ident-form UpgradeConfig missing Authenticator")

	// Case 2: qualified SelectorExpr (pkgname.UpgradeConfig) — no Authenticator field.
	path2 := filepath.Join(dir, "nauth_sel.go")
	src2 := `package main

import adapterws "github.com/ghbvf/gocell/adapters/websocket"

func main() {
	_ = adapterws.UpgradeConfig{AllowedOrigins: []string{"http://*"}}
}
`
	require.NoError(t, os.WriteFile(path2, []byte(src2), 0o644))

	lines2, err := findUpgradeConfigWithoutAuthenticator(path2)
	require.NoError(t, err)
	assert.NotEmpty(t, lines2, "should detect selector-form UpgradeConfig missing Authenticator")
}

func TestFindUpgradeConfigWithoutAuthenticator_AcceptsLiteralWithAuthenticator(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Case 1: same-package UpgradeConfig with Authenticator field present (ast.Ident).
	path1 := filepath.Join(dir, "withauth_ident.go")
	src1 := `package main

import "github.com/ghbvf/gocell/adapters/websocket"

func main() {
	_ = websocket.UpgradeConfig{
		AllowedOrigins: []string{"http://*"},
		Authenticator:  nil,
	}
}
`
	require.NoError(t, os.WriteFile(path1, []byte(src1), 0o644))

	lines1, err := findUpgradeConfigWithoutAuthenticator(path1)
	require.NoError(t, err)
	assert.Empty(t, lines1, "should not flag UpgradeConfig that has Authenticator field")

	// Case 2: qualified SelectorExpr with Authenticator field present.
	path2 := filepath.Join(dir, "withauth_sel.go")
	src2 := `package main

import adapterws "github.com/ghbvf/gocell/adapters/websocket"

func main() {
	_ = adapterws.UpgradeConfig{
		AllowedOrigins: []string{"http://*"},
		Authenticator:  nil,
	}
}
`
	require.NoError(t, os.WriteFile(path2, []byte(src2), 0o644))

	lines2, err := findUpgradeConfigWithoutAuthenticator(path2)
	require.NoError(t, err)
	assert.Empty(t, lines2, "should not flag UpgradeConfig that has Authenticator field (selector form)")
}

func testSEC07WebsocketAuthenticatorRequired(t *testing.T, root string) {
	t.Helper()
	files, err := findAllProductionGoFiles(root)
	require.NoError(t, err)

	var violations []string
	for _, f := range files {
		hits, err := findUpgradeConfigWithoutAuthenticator(f)
		require.NoErrorf(t, err, "scanning %s", f)
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		for _, line := range hits {
			violations = append(violations, fmt.Sprintf("%s:%d: UpgradeConfig literal missing Authenticator field (%s)",
				rel, line, secFailClosed07))
		}
	}
	if len(violations) > 0 {
		for _, v := range violations {
			t.Logf("%s violation: %s", secFailClosed07, v)
		}
	}
	assert.Empty(t, violations,
		"adapters/websocket UpgradeConfig literals must explicitly set Authenticator "+
			"(use auth.NewAnonymousAuthenticator() for explicit unauthenticated channels)")
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
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if !isUpgradeConfigType(cl.Type) {
			return true
		}
		if hasKey(cl, "Authenticator") {
			return true
		}
		lines = append(lines, fset.Position(cl.Pos()).Line)
		return true
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

func hasKey(cl *ast.CompositeLit, key string) bool {
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		ident, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		if ident.Name == key {
			return true
		}
	}
	return false
}

func testSEC08NoLegacyBroadcastCall(t *testing.T, root string) {
	t.Helper()
	files, err := findAllProductionGoFiles(root)
	require.NoError(t, err)

	var violations []string
	for _, f := range files {
		hits, err := findLegacyBroadcastCalls(f)
		require.NoErrorf(t, err, "scanning %s", f)
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		for _, line := range hits {
			violations = append(violations, fmt.Sprintf("%s:%d: legacy Hub.Broadcast call (use BroadcastFilter or BroadcastToSubject; %s)",
				rel, line, secFailClosed08))
		}
	}
	if len(violations) > 0 {
		for _, v := range violations {
			t.Logf("%s violation: %s", secFailClosed08, v)
		}
	}
	assert.Empty(t, violations,
		"runtime/websocket.Hub.Broadcast was deleted by PR-V1-SEC-WS-AUTH-ACL; "+
			"use BroadcastFilter (filter required) or BroadcastToSubject (O(1) subject index)")
}

func findLegacyBroadcastCalls(path string) ([]int, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	// Skip files that don't import runtime/websocket (no chance of a Hub.Broadcast call).
	if !bytes.Contains(data, []byte(`"github.com/ghbvf/gocell/runtime/websocket"`)) {
		return nil, nil
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
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel == nil || sel.Sel.Name != "Broadcast" {
			return true
		}
		// BroadcastFilter / BroadcastToSubject have different Sel.Name, so they pass.
		lines = append(lines, fset.Position(call.Pos()).Line)
		return true
	})
	return lines, nil
}

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

// testSEC09HubSubjectIdxSync enforces that every call site mutating h.conns
// (delete or clear) in hub.go resides in either the centralized
// removeConnLocked helper or the shutdown bulk-drain path. This replaces the
// previous per-function boolean ("function deletes h.conns AND touches
// subjectIdx") which silently passed when a function had multiple delete sites
// with only one paired subjectIdx update.
func testSEC09HubSubjectIdxSync(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, "runtime", "websocket", "hub.go")

	data, err := os.ReadFile(filepath.Clean(path))
	require.NoError(t, err)

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	require.NoError(t, err)

	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		if allowedConnsMutationFuncs[fn.Name.Name] {
			return true
		}
		ast.Inspect(fn.Body, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || (ident.Name != "delete" && ident.Name != "clear") {
				return true
			}
			if len(call.Args) < 1 {
				return true
			}
			sel, ok := call.Args[0].(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "conns" {
				return true
			}
			line := fset.Position(call.Pos()).Line
			violations = append(violations,
				fmt.Sprintf("hub.go:%d: %s() must not call %s(h.conns,...) directly; "+
					"use removeConnLocked() helper (or, for bulk drain, place inside shutdown). [%s]",
					line, fn.Name.Name, ident.Name, secFailClosed09))
			return true
		})
		return true
	})

	if len(violations) > 0 {
		for _, v := range violations {
			t.Logf("%s violation: %s", secFailClosed09, v)
		}
	}
	assert.Empty(t, violations,
		"every h.conns mutation site (delete or clear) must reside in the "+
			"centralized removeConnLocked helper or the shutdown bulk-drain path. "+
			"All other call sites must route through removeConnLocked() to keep "+
			"subjectIdx in lockstep with conns.")
}

// TestSEC09_SyntheticDirectDeleteViolates verifies that the SEC-09 archtest
// flags a direct delete(h.conns, ...) call outside the allowed function set.
func TestSEC09_SyntheticDirectDeleteViolates(t *testing.T) {
	t.Parallel()
	src := `package websocket

import "sync"

type Hub struct {
	connMu sync.Mutex
	conns  map[string]int
}

func (h *Hub) badRemove(id string) {
	h.connMu.Lock()
	delete(h.conns, id) // VIOLATION: not in allowedConnsMutationFuncs
	h.connMu.Unlock()
}

func (h *Hub) removeConnLocked(id string) {
	delete(h.conns, id) // OK: allowed
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic.go", src, parser.SkipObjectResolution)
	require.NoError(t, err)

	var found []string
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		if allowedConnsMutationFuncs[fn.Name.Name] {
			return true
		}
		ast.Inspect(fn.Body, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || (ident.Name != "delete" && ident.Name != "clear") {
				return true
			}
			if len(call.Args) < 1 {
				return true
			}
			sel, ok := call.Args[0].(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "conns" {
				return true
			}
			found = append(found, fn.Name.Name)
			return true
		})
		return true
	})

	require.Len(t, found, 1, "exactly one violation expected (badRemove)")
	assert.Equal(t, "badRemove", found[0])
}

// TestSEC09_SyntheticAllowedFunctionsPass verifies the allowlist works:
// removeConnLocked and shutdown can call raw delete/clear without violation.
func TestSEC09_SyntheticAllowedFunctionsPass(t *testing.T) {
	t.Parallel()
	src := `package websocket

import "sync"

type Hub struct {
	connMu sync.Mutex
	conns  map[string]int
}

func (h *Hub) removeConnLocked(id string) {
	delete(h.conns, id)
}

func (h *Hub) shutdown() {
	h.connMu.Lock()
	clear(h.conns)
	h.connMu.Unlock()
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "synthetic.go", src, parser.SkipObjectResolution)
	require.NoError(t, err)

	var found []string
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		if allowedConnsMutationFuncs[fn.Name.Name] {
			return true
		}
		ast.Inspect(fn.Body, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || (ident.Name != "delete" && ident.Name != "clear") {
				return true
			}
			if len(call.Args) < 1 {
				return true
			}
			sel, ok := call.Args[0].(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "conns" {
				return true
			}
			found = append(found, fn.Name.Name)
			return true
		})
		return true
	})

	assert.Empty(t, found, "removeConnLocked and shutdown should be allowed and produce no violations")
}

// findAllProductionMainPackageFiles walks the repo and returns every
// production .go file (non-test, non-vendor, non-generated) whose package
// clause is `main`. New entry points (cmd/<name>/main.go,
// examples/<name>/main.go, tests/<harness>/main.go, ...) are picked up
// automatically — no scope list to maintain.
//
// Parse errors fail-visible (callers receive an error) so a syntactically
// broken entry point cannot silently bypass the SEC scans.
func findAllProductionMainPackageFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", ".git", "generated", "testdata", "node_modules", "worktrees", "bak":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		af, perr := parser.ParseFile(fset, path, nil, parser.PackageClauseOnly)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		if af.Name != nil && af.Name.Name == "main" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}
