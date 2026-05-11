// INVARIANT: INTERNAL-CONTRACT-CLIENTS-REQUIRED-01
//
// # INTERNAL-CONTRACT-CLIENTS-REQUIRED-01
//
// Invariant: every wrapper.ContractSpec{...} composite literal whose Path
// starts with "/internal/v1/" must declare a non-empty Clients field.
// Missing or empty Clients on an internal contract means there is no
// caller-cell allowlist, which defeats the purpose of the 4-part
// service-token caller-identity propagation.
//
// Exemption: specs whose ID appears in awaitingRealCallerAllowlist are in
// transition; they get a grace period until Wave 3 wires the real Clients.
// Once a spec is in the allowlist AND the corresponding RouteGroup has been
// wired (i.e. the spec appears in a reg.RouteGroup call), it is removed from
// the allowlist — the gate itself enforces this anti-forget rule.
//
// Detection: type-aware via go/types — for every *ast.CompositeLit, the
// rule resolves cl.Type via pkg.TypesInfo and matches only when the named
// type's import path equals kernel/wrapper and the type name equals
// ContractSpec. Replaces the prior `hasID && hasPath` heuristic that
// false-positived on any struct sharing those field names (closes
// PR445-FU finding F1).
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

const ruleInternalContractClients01 = "INTERNAL-CONTRACT-CLIENTS-REQUIRED-01"

// wrapperContractSpecImportPath is the canonical import path of the package
// declaring ContractSpec. Derived at package init via reflect.TypeOf on
// wrapper.ContractSpec — the import statement above is the single source
// of truth, so a hardcoded-path typo (which silently fail-opened this rule
// from PR #445 commit 876cca5b until this commit) is no longer expressible.
var wrapperContractSpecImportPath = reflect.TypeOf(wrapper.ContractSpec{}).PkgPath()

// awaitingRealCallerAllowlist holds spec IDs that are in transition:
// the Clients field has not yet been set because Wave 3 has not landed.
// Each entry MUST be removed when the real Clients are wired in.
var awaitingRealCallerAllowlist = map[string]bool{}

// TestINTERNAL_CONTRACT_CLIENTS_REQUIRED_01 enforces that every
// wrapper.ContractSpec composite literal with an /internal/v1/* Path
// declares a non-empty Clients field.
//
// Type-aware via typeseval.SharedResolver: only literals whose static type
// resolves to wrapper.ContractSpec are inspected; structurally similar
// types in unrelated packages are ignored.
func TestINTERNAL_CONTRACT_CLIENTS_REQUIRED_01(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	resolver, err := typeseval.SharedResolver(root, false, nil,
		"./runtime/...", "./cells/...", "./cmd/...", "./kernel/...", "./adapters/...")
	require.NoError(t, err, "typeseval.SharedResolver")

	var violations []string
	for _, pkg := range resolver.Packages() {
		if pkg.TypesInfo == nil || pkg.Fset == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			rel := pkgFileRel(root, pkg, file)
			scanner.EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
				if !isContractSpecLit(cl, pkg.TypesInfo) {
					return
				}
				pathVal := contractSpecStringField(cl, "Path")
				if pathVal == "" || !strings.HasPrefix(pathVal, "/internal/v1/") {
					return
				}
				idVal := contractSpecStringField(cl, "ID")
				if awaitingRealCallerAllowlist[idVal] {
					return
				}
				if !hasNonEmptyClientsField(cl) {
					pos := pkg.Fset.Position(cl.Pos())
					violations = append(violations, fmt.Sprintf(
						"%s:%d: ContractSpec{ID:%q, Path:%q} has no Clients — "+
							"internal contracts must declare caller allowlist",
						rel, pos.Line, idVal, pathVal))
				}
			})
		}
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	if len(violations) > 0 {
		t.Errorf("%s: %d /internal/v1/* ContractSpec literals missing Clients field.\n"+
			"All internal contract specs must declare Clients to enforce caller-cell identity.\n"+
			"Add spec.Clients: []string{\"callerCellID\"} or add to awaitingRealCallerAllowlist.",
			ruleInternalContractClients01, len(violations))
	}
}

// isContractSpecLit reports whether cl is a wrapper.ContractSpec composite
// literal. Type-aware via go/types: cl.Type is resolved through pkg.TypesInfo
// and matched against the named ContractSpec type in kernel/wrapper.
//
// Fail-safe: returns false when cl, cl.Type, or info is nil — callers using
// inline parser.ParseFile (no TypesInfo) will see false, which is the
// conservative answer for a rule that inspects production code via
// typeseval-loaded packages.
func isContractSpecLit(cl *ast.CompositeLit, info *types.Info) bool {
	if cl == nil || cl.Type == nil || info == nil {
		return false
	}
	tv, ok := info.Types[cl]
	if !ok || tv.Type == nil {
		return false
	}
	named, ok := tv.Type.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == wrapperContractSpecImportPath && obj.Name() == "ContractSpec"
}

// contractSpecStringField returns the string literal value of the named
// top-level field in cl, or "" if absent or not a string literal. Iterates
// cl's direct children only so a same-named field nested inside a sub-struct
// does not pollute the outer literal's reading.
func contractSpecStringField(cl *ast.CompositeLit, fieldName string) string {
	var result string
	done := false
	scanner.EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
		if done {
			return
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != fieldName {
			return
		}
		lit, ok := kv.Value.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		s := lit.Value
		if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
			result = s[1 : len(s)-1]
		} else {
			result = s
		}
		done = true
	})
	return result
}

// hasNonEmptyClientsField returns true if cl declares a top-level Clients
// field whose value is a non-empty composite literal. EachInChildren visits
// only direct children of cl, so nested composites are not reached.
func hasNonEmptyClientsField(cl *ast.CompositeLit) bool {
	found := false
	scanner.EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
		if found {
			return
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Clients" {
			return
		}
		compLit, ok := kv.Value.(*ast.CompositeLit)
		if ok && len(compLit.Elts) > 0 {
			found = true
		}
	})
	return found
}

// TestINTERNAL_CONTRACT_CLIENTS_REQUIRED_01_NotContractSpecFalsePositive_Wave2_RED
// pins down the false-positives of the prior `hasID && hasPath` heuristic.
//
// After Wave 2 (type-aware via *types.Info): the inline parser.ParseFile
// path has no TypesInfo, so isContractSpecLit(cl, nil) returns false for
// every CompositeLit. The fail-safe nil-info contract guarantees no
// false-positive on inline-parsed sources. SubItem (different type, same
// field names) and Outer (subtree-leak from the prior EachNode walk) both
// stay unmatched.
//
// Wave 1 (heuristic + EachNode subtree): matched [SubItem Outer ContractSpec].
// Wave 2 (type-aware + nil info): matched [].
func TestINTERNAL_CONTRACT_CLIENTS_REQUIRED_01_NotContractSpecFalsePositive_Wave2_RED(t *testing.T) {
	t.Parallel()

	src := `package fake

type SubItem struct {
	ID      string
	Path    string
	Clients []string
}

type ContractSpec struct {
	ID      string
	Path    string
	Clients []string
}

type Outer struct {
	Inner ContractSpec
}

var subItem = SubItem{ID: "a", Path: "/internal/v1/x", Clients: []string{"y"}}

var outer = Outer{
	Inner: ContractSpec{ID: "b", Path: "/internal/v1/y"},
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fake.go", src, parser.SkipObjectResolution)
	require.NoError(t, err, "parse inline fixture")

	var matched []string
	scanner.EachInSubtree[ast.CompositeLit](f, func(cl *ast.CompositeLit) {
		if !isContractSpecLit(cl, nil) {
			return
		}
		switch t := cl.Type.(type) {
		case *ast.Ident:
			matched = append(matched, t.Name)
		default:
			matched = append(matched, fmt.Sprintf("%T", t))
		}
	})

	require.Empty(t, matched,
		"isContractSpecLit must not match non-ContractSpec types or outer wrappers; got %v", matched)
}
