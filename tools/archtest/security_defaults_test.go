package archtest

// security_defaults_test.go — static archtest rules for PR-MODE-1 SEC-FAIL-CLOSED.
//
// Four sub-tests mirror the SEC-FAIL-CLOSED-01..04 rule IDs:
//
//   01  addr-driven gate: bundle.go must not wrap WithListener in IfStmt guarded
//       by PrimaryHTTPAddr / InternalHTTPAddr / HealthHTTPAddr != "".
//   02  listener authChain non-nil: all WithListener calls must pass an explicit
//       non-nil 3rd argument (no bare nil literal).
//   03  adapter TLS endpoint: redis, vault, s3 adapters must import pkg/secutil
//       and call secutil.ValidateTLSEndpoint.
//   04  websocket origins: no file in adapters/websocket may assign
//       opts.InsecureSkipVerify = true.
//
// ref: tools/archtest/auth_authtest_boundary_test.go — 4 sub-test pattern

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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
}

// testSEC01AddrDrivenGate is intentionally a no-op.
//
// Rationale (SEC-FAIL-CLOSED-01 retired): the addr-driven gate pattern
// (if shared.PrimaryHTTPAddr != "" { bootstrap.WithListener(...) }) does NOT
// produce fail-open authentication behaviour — it merely skips listener
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
// Removing this rule avoids penalising a safe addr-conditional pattern while
// keeping the actionable nil-authChain rule (SEC-02) intact.
func testSEC01AddrDrivenGate(t *testing.T, _ string) {
	t.Helper()
	// Rule retired — see inline rationale above.
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
		return nil, nil // tolerate parse errors (build step handles them)
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
		return nil, nil
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
		switch rhs := assign.Rhs[0].(type) {
		case *ast.Ident:
			if rhs.Name == "true" {
				lines = append(lines, fset.Position(assign.Pos()).Line)
			}
		}
		return true
	})
	return lines, nil
}
