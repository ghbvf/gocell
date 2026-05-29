// INVARIANT: AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01
//
// Locks the audit-domain wire-out sensitive-field codegen funnel
// (tools/codegen/contractgen/sensitive_wire_guard.go). The funnel rejects, at
// generation time, any audit-domain (http.audit.* / event.audit.*) HTTP
// response or event payload schema that declares a pkg/redaction sensitive-key
// field at any depth — so a third party's sessionId (or any credential-adjacent
// key) can never be projected onto an audit wire surface (issue #1219).
//
// AI-robust 评级 (per .claude/rules/gocell/ai-robust.md §Hard 范本目录 —
// "codegen funnel + golden"):
//
//   - Upstream Hard: a sensitive field cannot reach a generated audit DTO. By
//     schema, contractgen rejects it (the funnel). By hand-edited types_gen.go,
//     `gocell verify generated` byte-stable golden drift fails CI. Both forgery
//     vectors are closed.
//   - Downstream Medium (ceiling, this archtest): the funnel must be CALLED on
//     every audit wire-out generation path. That "must-call" cannot be expressed
//     in Go's type system — schemaToDTOs is shared with the exempt Request path,
//     so the funnel cannot be folded into it unconditionally. A1 below is the
//     Go-language ceiling for this shape (call-site allowlist + coverage). The
//     ceiling — and an optional archtest-Medium strengthening via a
//     schemaToWireOutDTOs wrapper — is tracked in gh #1299 (same won't-do shape
//     as the SPAN-SETATTR #851 / HEALTHZ-HOLDER #893 Medium ceilings).
//
// Two sub-checks:
//
//   - A1 (call-site allowlist + coverage): in production contractgen files,
//     every call to rejectSensitiveAuditWireFields is inside buildHTTPDTOs (the
//     Response path) or buildEventSpec (the Payload path), and BOTH contain one
//     — so the funnel cannot be silently removed from a wire-out path, and is
//     never wired into the exempt Request path.
//   - A2 (scope completeness): every contract owned by auditcore has an id
//     covered by the funnel's prefix gate (http.audit.* / event.audit.*), so a
//     future auditcore-owned contract with a divergent id cannot escape the
//     funnel's scope unnoticed.
//
// 盲区自检 (tool-scope blind spots + reverse self-checks):
//
//   - A1 matches the callee by *ast.Ident name only (rejectSensitiveAuditWireFields
//     is an unexported, package-internal func, so there is no cross-package alias
//     / dot-import vector — unlike crypto/hmac.New, no types resolution needed).
//     A method or selector of the same name on another value (x.rejectSensitiveAuditWireFields)
//     would NOT be a bare Ident and is therefore out of scope; TestAuditWireFunnel_NoSelectorShadow
//     asserts no such selector exists in production contractgen, closing that blind spot.
//   - A1 scope excludes _test.go (DirsScope default) and generated/ — the
//     behavioral test and the fallback unit test call the funnel directly and
//     must not be mistaken for production call sites.
//   - A1 locks where the funnel IS called, not "every wire-out schemaToDTOs is
//     funnel-guarded". Other schemaToDTOs callers (buildSagaSpec's saga output,
//     headers) are NOT funnel-guarded today — but they cannot carry an audit
//     wire surface unnoticed: a kind:saga (or any new-kind) contract owned by
//     auditcore would have an id that misses the http.audit./event.audit. prefix
//     gate, so A2 fails until isAuditWireContract is extended (at which point the
//     funnel must be wired into that path too). The gap is real but A2-gated.
//   - A2 keys on ownerCell == "auditcore"; a Principal-aggregating cell under a
//     different owner name is out of A2's scope. That is acceptable today
//     (auditcore is the sole such cell — 规则不超前于代码现状); the funnel godoc
//     documents the manual extension step. No reverse self-check is added for
//     this case because it is vacuously covered: A2 already enumerates ALL
//     contract.yaml and only acts on ownerCell==auditcore, so a different-owner
//     audit cell is a future-cell concern, not a present escapable gap.
package archtest

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const auditFunnelCallee = "rejectSensitiveAuditWireFields"

// auditFunnelAllowedCallers is the closed set of contractgen functions allowed
// to invoke the funnel — the two wire-out (non-Request) DTO build paths.
var auditFunnelAllowedCallers = map[string]bool{
	"buildHTTPDTOs":  true, // HTTP Response schema path
	"buildEventSpec": true, // event Payload schema path
}

// TestAuditWireFunnel_CallSiteAllowlistAndCoverage is A1: the funnel's call
// sites in production contractgen are exactly {buildHTTPDTOs, buildEventSpec},
// and both are present.
//
// INVARIANT: AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 (A1 call-site lock).
func TestAuditWireFunnel_CallSiteAllowlistAndCoverage(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"tools/codegen/contractgen"})

	covered := map[string]bool{}
	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
				if fn.Body == nil || fn.Name == nil {
					return
				}
				EachInSubtree[ast.CallExpr](fn, func(c *ast.CallExpr) {
					id, ok := c.Fun.(*ast.Ident)
					if !ok || id.Name != auditFunnelCallee {
						return
					}
					if auditFunnelAllowedCallers[fn.Name.Name] {
						covered[fn.Name.Name] = true
						return
					}
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(c.Pos()).Line,
						Message: auditFunnelCallee + " called inside " + fn.Name.Name +
							" — only the wire-out paths {buildHTTPDTOs, buildEventSpec} may call it " +
							"(Request path must stay exempt)",
					})
				})
			})
		}
		return d
	})
	Report(t, "AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01", diags)

	for caller := range auditFunnelAllowedCallers {
		if !covered[caller] {
			t.Errorf("AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01: %s does not call %s — "+
				"a wire-out path is no longer funneled (coverage gap)", caller, auditFunnelCallee)
		}
	}
}

// TestAuditWireFunnel_NoSelectorShadow is the A1 blind-spot reverse check: no
// production contractgen code calls a selector named rejectSensitiveAuditWireFields
// (e.g. x.rejectSensitiveAuditWireFields), which A1's bare-Ident match would miss.
//
// INVARIANT: AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 (A1 blind-spot self-check).
func TestAuditWireFunnel_NoSelectorShadow(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"tools/codegen/contractgen"})

	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
				if sel.Sel != nil && sel.Sel.Name == auditFunnelCallee {
					d = append(d, Diagnostic{
						Rel:     rel,
						Line:    p.Fset.Position(sel.Pos()).Line,
						Message: "selector named " + auditFunnelCallee + " would bypass A1's bare-Ident call-site lock",
					})
				}
			})
		}
		return d
	})
	Report(t, "AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01", diags)
}

// auditContractMeta is the minimal contract.yaml view A2 needs.
type auditContractMeta struct {
	ID        string `yaml:"id"`
	OwnerCell string `yaml:"ownerCell"`
}

// TestAuditWireFunnel_ScopeCompleteness is A2: every auditcore-owned contract
// has an id covered by the funnel's prefix gate, so the gate cannot
// under-match a future audit-domain contract.
//
// INVARIANT: AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 (A2 scope completeness).
func TestAuditWireFunnel_ScopeCompleteness(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := scanner.DirsScope(root, []string{"contracts"},
		scanner.MatchRels(func(rel string) bool {
			return filepath.Base(rel) == "contract.yaml"
		}),
	)

	var diags []Diagnostic
	scanner.EachContentFile(t, scope, []string{".yaml"}, func(t *testing.T, fc scanner.ContentContext) {
		t.Helper()
		var c auditContractMeta
		if err := yaml.Unmarshal(fc.Bytes, &c); err != nil {
			diags = append(diags, Diagnostic{Rel: fc.Rel, Message: "parse contract.yaml: " + err.Error()})
			return
		}
		if c.OwnerCell != "auditcore" {
			return
		}
		if !strings.HasPrefix(c.ID, "http.audit.") && !strings.HasPrefix(c.ID, "event.audit.") {
			diags = append(diags, Diagnostic{
				Rel: fc.Rel,
				Message: "auditcore-owned contract id " + c.ID + " is not covered by the funnel prefix gate " +
					"(http.audit.* / event.audit.*) — extend isAuditWireContract in " +
					"tools/codegen/contractgen/sensitive_wire_guard.go or this audit wire surface is unprotected",
			})
		}
	})
	Report(t, "AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01", diags)
}
