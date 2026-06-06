package archtest

// invariants:
//   - INVARIANT: SEC-FAIL-CLOSED-01
//   - INVARIANT: SEC-FAIL-CLOSED-02
//   - INVARIANT: SEC-FAIL-CLOSED-03
//   - INVARIANT: SEC-FAIL-CLOSED-04
//   - INVARIANT: SEC-FAIL-CLOSED-05
//   - INVARIANT: SEC-FAIL-CLOSED-06
//   - INVARIANT: SEC-FAIL-CLOSED-07
//   - INVARIANT: SEC-FAIL-CLOSED-08
//   - INVARIANT: SEC-FAIL-CLOSED-09
//   - INVARIANT: SEC-FAIL-CLOSED-10
//
// security_defaults_test.go — dogfood + standalone fixture tests for
// SEC-FAIL-CLOSED-01..10. The importable scanner logic lives in
// security_defaults.go (module-path-agnostic, #1640 M3 PR-9).
//
// Sub-tests mirror the SEC-FAIL-CLOSED-01..10 rule IDs:
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
//   10  health listener required: a package main that references
//       cell.PrimaryListener must also reference cell.HealthListener (#673 —
//       bootstrap phase0 fail-fasts without a HealthListener; no silent remap).
//
// ref: tools/archtest/auth_authtest_boundary_test.go — 4 sub-test pattern

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/testutil/fileutil"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestSecurityDefaults is the dogfood entry point for SEC-FAIL-CLOSED-02..10.
// It delegates to CheckSecurityDefaults (security_defaults.go) which returns
// violations as []Diagnostic; Report surfaces them via t.Errorf.
//
// SEC-FAIL-CLOSED-01 is retired; its t.Skip sub-test is retained as an inert
// marker so historic CI logs and grep map cleanly onto rule IDs.
func TestSecurityDefaults(t *testing.T) {
	t.Run(secFailClosed01+"_addr_driven_listener_gate_banned", func(t *testing.T) {
		testSEC01AddrDrivenGate(t)
	})
	Report(t, "SEC-FAIL-CLOSED-02..10", CheckSecurityDefaults(t, ConfigForExternalCell{}))
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
// #673 note: for the HealthListener specifically, the retirement premise
// ("omitting an addr merely skips registration") is now strengthened — omitting
// the HealthListener no longer silently remaps health routes onto the public
// primary listener; bootstrap phase0 fail-fasts instead. The addr-driven gate
// stays legitimate (corebundle defaults GOCELL_HTTP_HEALTH_ADDR so the listener
// is always declared), but a package main wiring PrimaryListener must also wire
// HealthListener — now enforced statically by SEC-FAIL-CLOSED-10.
//
// SEC-02 covers the actual nil-authChain risk. See git history for context.
func testSEC01AddrDrivenGate(t *testing.T) {
	t.Helper()
	t.Skip("SEC-FAIL-CLOSED-01 retired: addr-driven if-gate is safe; SEC-02 covers actual fail-open. See git history for context.")
}

// ---------------------------------------------------------------------------
// Standalone fixture tests — exercise helpers from security_defaults.go directly.
// ---------------------------------------------------------------------------

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
	chain := []auth.ListenerAuth{auth.AuthNone{}}
	bootstrap.WithListener(cell.InternalListener, ":9090", chain)
	bootstrap.WithListener(cell.InternalListener, ":9091", insecureInternalAuth())
}

func insecureInternalAuth() []auth.ListenerAuth {
	return []auth.ListenerAuth{auth.AuthNone{}}
}
`
	require.NoError(t, os.WriteFile(path, []byte(src), 0o644))

	lines, err := findInternalListenerAuthNoneChain(path)

	require.NoError(t, err)
	assert.Len(t, lines, 2)
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

	violations := findExampleComposeCredentialViolations(t, root)
	require.Len(t, violations, 1)
	assert.Contains(t, violations[0], "examples/futuredevice/docker-compose.yml:5")
	assert.Contains(t, violations[0], "POSTGRES_PASSWORD")
}

// TestSEC05ExampleComposeCredentialsScansNestedDirs locks in the recursive
// scan over examples/. Pre-Path-C the rule was 2-level (examples/<example>/
// docker-compose.yml only), which silently failed open on
// examples/<example>/<sub>/docker-compose.yml — a hardcoded credential in a
// nested compose file leaks credentials just as surely as one at the top.
// This fixture writes one top-level + one nested compose file with the same
// fallback pattern and asserts both are flagged.
func TestSEC05ExampleComposeCredentialsScansNestedDirs(t *testing.T) {
	root := t.TempDir()
	topDir := filepath.Join(root, "examples", "futuredevice")
	nestedDir := filepath.Join(root, "examples", "futuredevice", "deploy")
	require.NoError(t, os.MkdirAll(nestedDir, 0o755))
	body := []byte(`
services:
  postgres:
    environment:
      POSTGRES_PASSWORD: ${FUTURE_POSTGRES_PASSWORD:-gocell}
`)
	require.NoError(t, os.WriteFile(filepath.Join(topDir, "docker-compose.yml"), body, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(nestedDir, "docker-compose.yml"), body, 0o644))

	violations := findExampleComposeCredentialViolations(t, root)
	require.Len(t, violations, 2, "expected both top-level and nested compose violations: %v", violations)

	rels := append([]string(nil), violations...)
	sort.Strings(rels)
	assert.Contains(t, rels[0], "examples/futuredevice/deploy/docker-compose.yml")
	assert.Contains(t, rels[1], "examples/futuredevice/docker-compose.yml")
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
	scanner.EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
		if fn.Body == nil {
			return
		}
		if allowedConnsMutationFuncs[fn.Name.Name] {
			return
		}
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
			found = append(found, fn.Name.Name)
		})
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
	scanner.EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
		if fn.Body == nil {
			return
		}
		if allowedConnsMutationFuncs[fn.Name.Name] {
			return
		}
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
			found = append(found, fn.Name.Name)
		})
	})

	assert.Empty(t, found, "removeConnLocked and shutdown should be allowed and produce no violations")
}

// ---------------------------------------------------------------------------
// SEC-10 standalone fixture test
// ---------------------------------------------------------------------------

// TestSEC10FixtureCatchesPrimaryWithoutHealth is the positive-coverage proof
// that the rule fires end-to-end. The build-tag-gated healthlistenerfixture is a
// `package main` that references cell.PrimaryListener (through a non-default
// alias, proving canonical-path resolution) and never cell.HealthListener — the
// exact shape SEC-FAIL-CLOSED-10 must flag. Driving sec10Violations (not just the
// listenerRefsInPass helper) proves the real rule path — the package-main gate
// plus the primary-without-health detection — fires, so a regression in either
// is caught.
func TestSEC10FixtureCatchesPrimaryWithoutHealth(t *testing.T) {
	var violations []string
	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/healthlistenerfixture"}),
		func(p *Pass) []Diagnostic {
			violations = append(violations, sec10Violations(p)...)
			return nil
		})

	require.Len(t, violations, 1,
		"the primary-without-health fixture must produce exactly one SEC-FAIL-CLOSED-10 violation; "+
			"this exercises the full rule path (package-main gate + primary-without-health detection), "+
			"not just the listenerRefsInPass helper")
	assert.Contains(t, violations[0], "cell.PrimaryListener",
		"the violation message must name the offending listener ref")
}
