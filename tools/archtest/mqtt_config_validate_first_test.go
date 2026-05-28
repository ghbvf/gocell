// INVARIANT: MQTT-CONFIG-VALIDATE-FIRST-01
//
// mqtt_config_validate_first_test.go — locks the Open function in
// adapters/mqtt so its body invokes cfg.Validate() before any side-effecting
// call to autopaho.NewConnection. The earlier shape (which allowed Open to
// skip Validate) let callers feed an unvalidated Config — broken ClientID,
// out-of-range KeepAlive, plaintext remote broker, all bypassed.
//
// # AI-robust grading
//
// This is a Medium archtest that closes the C1 F1 carryover gap. The true
// Hard form (sealed Config + NewConfig(... ) (Config, error) sole
// constructor) is tracked by gh issue #1226 — it requires restructuring all
// Config struct-literal callsites and is out of scope for the PR-1 review
// fix batch. The Medium archtest below makes "Open skips Validate" a
// CI-detectable form regression in the meantime; the Hard upgrade can land
// independently.
//
//   - Upstream (caller-side construction of Config): no funnel — callers
//     still construct Config via struct literal. Tracked as Soft → Hard by
//     #1226.
//   - Downstream (Open body): Medium archtest below — the first statement
//     after the MustHaveClock call must be a call to (Config).Validate.
//
// # Form-lock rule
//
//   - Locate the FuncDecl named "Open" inside package adapters/mqtt.
//   - Walk the body's top-level statements in order.
//   - The first statement that calls autopaho.NewConnection is the
//     "side-effect boundary"; before this boundary, an IfStmt or ExprStmt
//     must call (Config).Validate via the cfg receiver.
//   - If no call to autopaho.NewConnection is found, the test is vacuously
//     OK (Open no longer constructs the manager — refactor moved the side
//     effect elsewhere, which a reviewer must inspect).
//
// # Blind spots
//
//  1. A future refactor that moves NewConnection into a helper called by
//     Open: the helper would not be inside Open's FuncDecl, so this scan
//     would not find NewConnection at all and would vacuously pass. The
//     reverse self-check below requires ≥1 cfg.Validate call AND ≥1
//     NewConnection call inside Open to keep the scanner honest. If a
//     future refactor moves NewConnection out, this test will fail with
//     "no autopaho.NewConnection call inside Open" — the maintainer must
//     update the rule to follow the moved side effect.
//  2. The scan reads AST, not types. cfg.Validate() resolution uses
//     ResolveMethodCall to ensure the receiver type is mqtt.Config, not an
//     accidentally similarly-named method on another type.
//
// ref: adapters/mqtt.Open
// ref: gh issue #1226 — Hard form via sealed Config / NewConfig sole constructor
package archtest

import (
	"go/ast"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMQTTConfigValidateFirst01 enforces MQTT-CONFIG-VALIDATE-FIRST-01.
func TestMQTTConfigValidateFirst01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const ruleID = "MQTT-CONFIG-VALIDATE-FIRST-01"

	var openFD *ast.FuncDecl
	var typesInfo *types.Info

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{mqttPkgPath},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != mqttPkgPath {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
					if fd.Name == nil || fd.Name.Name != "Open" || fd.Recv != nil {
						return
					}
					openFD = fd
					typesInfo = p.TypesInfo
				})
			}
			return nil
		})

	require.NotNil(t, openFD,
		"%s: cannot find FuncDecl Open inside adapters/mqtt — rule must be updated", ruleID)
	require.NotNil(t, openFD.Body,
		"%s: Open has no body", ruleID)
	require.NotNil(t, typesInfo,
		"%s: TypesInfo unavailable — go/types loading failed", ruleID)

	// Walk statements; record the position of the first cfg.Validate call and
	// the first autopaho.NewConnection call. The Validate call must come first.
	validatePos, newConnPos := -1, -1
	for idx, stmt := range openFD.Body.List {
		if callsConfigValidate(stmt, typesInfo) && validatePos == -1 {
			validatePos = idx
		}
		if callsAutopahoNewConnection(stmt, typesInfo) && newConnPos == -1 {
			newConnPos = idx
		}
	}

	require.GreaterOrEqual(t, validatePos, 0,
		"%s: Open body must contain a (Config).Validate call", ruleID)
	require.GreaterOrEqual(t, newConnPos, 0,
		"%s: Open body must contain an autopaho.NewConnection call "+
			"(otherwise the side-effect boundary moved and this rule needs updating)", ruleID)
	assert.Less(t, validatePos, newConnPos,
		"%s: (Config).Validate at stmt #%d must precede autopaho.NewConnection at stmt #%d",
		ruleID, validatePos, newConnPos)
}

// callsConfigValidate reports whether stmt contains a call to a method named
// Validate on a value of type mqtt.Config (resolved via types.Info). Both
// IfStmt-init (`if err := cfg.Validate(); ...`) and bare ExprStmt
// (`cfg.Validate()`) are accepted.
func callsConfigValidate(stmt ast.Stmt, info *types.Info) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if found {
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
		if sel.Sel.Name != "Validate" {
			return true
		}
		fn, ok := ResolveMethodCall(info, sel)
		if !ok {
			return true
		}
		recv := fn.Type().(*types.Signature).Recv()
		if recv == nil {
			return true
		}
		recvType := recv.Type()
		// Strip pointer.
		if ptr, isPtr := recvType.(*types.Pointer); isPtr {
			recvType = ptr.Elem()
		}
		named, isNamed := recvType.(*types.Named)
		if !isNamed {
			return true
		}
		if named.Obj().Pkg() != nil &&
			named.Obj().Pkg().Path() == mqttPkgPath &&
			named.Obj().Name() == "Config" {
			found = true
			return false
		}
		return true
	})
	return found
}

// callsAutopahoNewConnection reports whether stmt contains a call to
// autopaho.NewConnection (any package alias). Resolution via ResolvePackageRef.
func callsAutopahoNewConnection(stmt ast.Stmt, info *types.Info) bool {
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if !ok {
			return true
		}
		if name != "NewConnection" {
			return true
		}
		if pkgPath == "github.com/eclipse/paho.golang/autopaho" {
			found = true
			return false
		}
		return true
	})
	return found
}
