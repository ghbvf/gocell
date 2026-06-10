//go:build archtest

package archtest

// invariants:
//   - INVARIANT: CONFIG-NOOP-TRANSFORMER-FUNNEL-01

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ruleConfigNoopTransformerFunnel01 = "CONFIG-NOOP-TRANSFORMER-FUNNEL-01"
	// cryptoPkgPath derived from PlatformModulePath per ARCHTEST-MODULE-PATH-FUNNEL-01.
	cryptoPkgPath           = PlatformModulePath + "/runtime/crypto"
	noopTransformerTypeName = "NoopTransformer"
	noopAllowedFile         = "cellmodules/configcore/module.go"
	noopAllowedFunc         = "resolveValueTransformer"
)

// TestConfigNoopTransformerFunnel01 enforces that crypto.NoopTransformer{} — the
// no-encryption ValueTransformer — is constructed at exactly one production
// site: cellmodules/configcore/module.go::resolveValueTransformer. That site
// gates the dev branch behind an explicit postgres-storage rejection (F3), so
// the only way config values reach disk unencrypted is the deliberate,
// greppable dev/memory path. A NoopTransformer{} constructed anywhere else in
// production would re-open the silent plaintext-persistence hole.
//
// AI-robust grade: Hard downstream (caller-allowlist by (file, enclosing func,
// crypto.NoopTransformer{} composite-literal form) — any other production
// construction site fails the archtest; the qualifier is resolved to
// runtime/crypto via the file's imports, so a decoy `x.NoopTransformer{}` from a
// different package does not match the type but also does not slip past as the
// crypto one) + Medium upstream (Go cannot make "construct this exported
// zero-value struct elsewhere" inexpressible; this archtest is the backstop).
// Same shape as the reconstruction / ctx-write caller-allowlist funnels.
//
// Hard-上游升级路径（接 gocell:"required" tag-funnel，泛化 requireddepsgen）追踪于
// gh #1411；届时本 archtest 可退役。
//
// Blind spots (AST-only Run; documented per ai-robust §载体决策原则):
//   - Aliased construction via a function value (e.g. `f := crypto.NoopTransformer{};`
//     returned indirectly) is still a composite literal and IS caught — the scan
//     is on the literal, not the call.
//   - A new type that embeds/wraps NoopTransformer is out of scope: the funnel
//     guards the canonical no-op transformer, not arbitrary no-op equivalents.
//     Such a type would itself need to construct crypto.NoopTransformer{} (caught)
//     or re-implement ValueTransformer (a separate, visible decision).
//   - Test files (_test.go) are excluded: tests legitimately construct the no-op
//     transformer for unit coverage.
func TestConfigNoopTransformerFunnel01(t *testing.T) {
	root := findModuleRoot(t)

	var violations []string
	Run(t, AST(ModuleScope(root)), func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
				sel, ok := cl.Type.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != noopTransformerTypeName {
					return
				}
				qual, isIdent := sel.X.(*ast.Ident)
				if !isIdent || importPathForQualifier(file, qual.Name) != cryptoPkgPath {
					return
				}
				fnName := enclosingFuncName(file, cl.Pos())
				if rel == noopAllowedFile && fnName == noopAllowedFunc {
					return
				}
				if fnName == "" {
					fnName = "<package-level>"
				}
				violations = append(violations, rel+"::"+fnName)
			})
		}
		return nil
	})

	assert.Emptyf(t, violations,
		"%s: crypto.NoopTransformer{} constructed outside the sanctioned site %s::%s "+
			"(F3 dev-only no-encryption funnel). Offending sites: %v. A new construction "+
			"site re-opens the silent plaintext-persistence hole; route through "+
			"resolveValueTransformer instead.",
		ruleConfigNoopTransformerFunnel01, noopAllowedFile, noopAllowedFunc, violations)
}

// TestConfigNoopTransformerFunnel01_DetectsViolation is the reverse self-check:
// it proves the scan's matching predicate flags a crypto.NoopTransformer{} built
// in a non-allowlisted function, and does NOT mis-flag a same-named selector from
// a different (non-crypto) package. Without this, a regression that silently
// stopped matching would pass the forward test vacuously.
func TestConfigNoopTransformerFunnel01_DetectsViolation(t *testing.T) {
	const src = `package foo

import (
	crypto "github.com/ghbvf/gocell/runtime/crypto"
	other "example.com/other"
)

func bad() any { return crypto.NoopTransformer{} }

func decoy() any { return other.NoopTransformer{} }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "module.go", src, 0)
	require.NoError(t, err)

	var cryptoHits, decoyHits []string
	EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
		sel, ok := cl.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != noopTransformerTypeName {
			return
		}
		qual, isIdent := sel.X.(*ast.Ident)
		if !isIdent {
			return
		}
		fnName := enclosingFuncName(file, cl.Pos())
		if importPathForQualifier(file, qual.Name) == cryptoPkgPath {
			cryptoHits = append(cryptoHits, fnName)
		} else {
			decoyHits = append(decoyHits, fnName)
		}
	})

	assert.Equal(t, []string{"bad"}, cryptoHits,
		"the crypto.NoopTransformer{} in bad() must be detected as a real construction site")
	assert.Equal(t, []string{"decoy"}, decoyHits,
		"a same-named selector from a non-crypto package must NOT resolve to runtime/crypto")
}
