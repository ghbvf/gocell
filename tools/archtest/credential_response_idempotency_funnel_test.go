// INVARIANT: CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01
//
// Locks the credential-response idempotency-exempt codegen funnel
// (tools/codegen/contractgen/credential_response_idempotency_guard.go). The funnel
// rejects, at generation time, any kind:http contract whose response schema declares
// a pkg/redaction sensitive-key field at any nesting depth AND whose
// endpoints.http.auth.idempotencyExempt is NOT true — so a credential-bearing
// response (e.g. accessToken / refreshToken under "data") can never reach the HTTP
// idempotency store (Redis, 24h TTL) without an explicit opt-out (issue #1469).
//
// Background: the HTTP idempotency store records+replays responses keyed on the
// Idempotency-Key header. For credential-returning routes (login / token-refresh /
// change-password), replay-eligible recording would persist live tokens that must
// never be read back — a P0 credential-leakage vector. The three affected contracts
// (http.auth.login.v1, http.auth.refresh.v1, http.auth.user.change-password.v1) already
// declare idempotencyExempt: true in their contract.yaml; this funnel ensures that any
// future credential-returning route cannot be silently omitted.
//
// Symmetric to AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 (which prevents audit-domain
// wire-out schemas from projecting Principal credentials). Both funnels share the
// walkSchemaSensitiveKeys tree-walker in sensitive_wire_guard.go.
//
// AI-robust grading (per .claude/rules/gocell/ai-robust.md §Hard 范本目录 —
// "codegen funnel + golden"):
//
//   - Upstream Hard: a sensitive response field on a non-exempt route cannot be
//     generated. By schema → contractgen rejects it at generation time (the funnel).
//     By hand-edited handler_gen.go removing IdempotencyExempt: true → `gocell verify
//     generated` byte-stable golden drift fails CI. Both forgery vectors are closed.
//   - Downstream Medium (ceiling, this archtest): the funnel must be CALLED on
//     every HTTP response generation path. That "must-call" cannot be expressed in
//     Go's type system — buildResponseDTOs (called exclusively from buildHTTPDTOs's
//     response branch) is the only production caller today, but Go cannot enforce
//     that all future HTTP codegen paths also invoke it. A1 below is the Go-language
//     ceiling for this shape (call-site allowlist + coverage) — a permanent
//     won't-do ceiling (same shape as the SPAN-SETATTR #851 / HEALTHZ-HOLDER #893
//     / SAGA-JOURNAL #982 / outbox-provenance #1282 Medium ceilings). A
//     schemaToWireOutDTOs wrapper could strengthen archtest-Medium coverage but does
//     not change the tier; it is left as optional defense-in-depth, not separately
//     tracked.
//
// Symbol list (single source — do NOT copy to rule .md files):
//   - Funnel function:         rejectUnexemptCredentialResponse (credential_response_idempotency_guard.go)
//   - Allowed production caller: buildResponseDTOs (builder.go, HTTP Response wire-out helper)
//   - Shared walker:           walkSchemaSensitiveKeys (sensitive_wire_guard.go)
//
// Two sub-checks:
//
//   - A1 (call-site allowlist): in production contractgen files, the only call to
//     rejectUnexemptCredentialResponse is inside buildResponseDTOs (the HTTP
//     Response wire-out helper, extracted from buildHTTPDTOs to keep cognitive
//     complexity bounded). Any call from a different function — especially the
//     Request path or a new codegen function added without awareness of the funnel
//     — fails this check immediately. The guard is an unexported package-internal
//     func, so there is no cross-package alias / dot-import vector.
//   - A2 (scope completeness): every kind:http contract in production whose response
//     schema is known to carry a sensitive key must declare idempotencyExempt: true.
//     This check enumerates the three currently-known such contracts and asserts they
//     carry the flag. It is NOT a full schema-walk of all contracts at archtest time
//     (that would re-implement the generator); instead it asserts the known set is
//     complete, ensuring the guard is non-vacuous and that removing the flag from
//     any of the three flagged contracts is detected immediately.
//
// 盲区自检 (tool-scope blind spots + reverse self-checks):
//
//   - A1 matches the callee by *ast.Ident name only (the function is unexported
//     and package-internal, so there is no cross-package alias / dot-import
//     vector — unlike crypto/hmac.New, no types resolution needed).
//     A method or selector of the same name on another value
//     (x.rejectUnexemptCredentialResponse) would NOT be a bare Ident and is
//     therefore out of scope; TestCredentialIdempotencyFunnel_NoSelectorShadow
//     asserts no such selector exists in production contractgen, closing that
//     blind spot.
//   - A2's idempotencyExempt check reads the contract.yaml field via YAML
//     unmarshal. A contract that carries the sensitive key in a nested $ref'd
//     schema file (not inlined) is out of A2's static scope; however, the funnel
//     itself walks the fully-resolved schema tree at generation time, so the gap
//     is between A2's static assertion and the generator's dynamic walk — not a
//     gap in the actual protection. The three currently-known contracts all inline
//     their response fields (or the prior agent verified their schemas), so the
//     A2 check is non-vacuous today.
//   - A2 scope is the *known* closed set of flagged contracts (not all http
//     contracts). A new credential-returning contract that omits idempotencyExempt
//     would be caught by the funnel at generation time (Upstream Hard), not by A2.
//     A2's purpose is to ensure the three known contracts have NOT had the flag
//     silently removed (regression guard), and to serve as the non-vacuity anchor
//     for the overall invariant.
package archtest

import (
	"go/ast"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const credentialIdempotencyFunnelCallee = "rejectUnexemptCredentialResponse"

// credentialIdempotencyFunnelAllowedCallers is the closed set of contractgen
// functions allowed to invoke the funnel. Only buildResponseDTOs (the HTTP
// Response schema wire-out helper, extracted from buildHTTPDTOs to keep
// cognitive complexity bounded) may call it; the Request path must remain exempt
// because inbound credential fields (e.g. password in a login request body)
// are legitimate.
var credentialIdempotencyFunnelAllowedCallers = map[string]bool{
	"buildResponseDTOs": true, // HTTP Response schema wire-out path only
}

// credentialIdempotencyFlaggedContracts is the known closed set of contracts
// whose response schema carries sensitive keys and that therefore MUST declare
// idempotencyExempt: true. A2 asserts that this set has not silently lost the
// flag. This is NOT an assertion that it is the complete set of all such
// contracts (the funnel handles that at generation time).
var credentialIdempotencyFlaggedContracts = []string{
	"http.auth.login.v1",
	"http.auth.refresh.v1",
	"http.auth.user.change-password.v1",
}

// TestCredentialIdempotencyFunnel_CallSiteAllowlist is A1: every call to
// rejectUnexemptCredentialResponse in production contractgen sits inside
// buildResponseDTOs (the HTTP Response wire-out helper, extracted from
// buildHTTPDTOs), never the Request path or any unrelated function.
//
// INVARIANT: CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01 (A1 call-site lock).
func TestCredentialIdempotencyFunnel_CallSiteAllowlist(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"tools/codegen/contractgen"})

	diags := Run(t, AST(scope), func(p *Pass) []Diagnostic {
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
				if fn.Body == nil || fn.Name == nil {
					return
				}
				EachInSubtree[ast.CallExpr](fn, func(c *ast.CallExpr) {
					id, ok := c.Fun.(*ast.Ident)
					if !ok || id.Name != credentialIdempotencyFunnelCallee {
						return
					}
					if !credentialIdempotencyFunnelAllowedCallers[fn.Name.Name] {
						d = append(d, Diagnostic{
							Rel:  rel,
							Line: p.Fset.Position(c.Pos()).Line,
							Message: credentialIdempotencyFunnelCallee + " called inside " + fn.Name.Name +
								" — only buildHTTPDTOs (the HTTP Response wire-out path) may call it; " +
								"the Request path must remain exempt (inbound credentials are legitimate) " +
								"(CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01)",
						})
					}
				})
			})
		}
		return d
	})

	Report(t, "CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01", diags)

	// Verify the funnel is actually wired (at least one call exists in the allowed
	// set) — guards against someone accidentally removing the hook entirely.
	found := false
	_ = Run(t, AST(scope), func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			EachInSubtree[ast.CallExpr](file, func(c *ast.CallExpr) {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == credentialIdempotencyFunnelCallee {
					found = true
				}
			})
		}
		return nil
	})
	if !found {
		t.Errorf("CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01: no call to %s found in production contractgen — "+
			"the credential response guard has been removed from the HTTP generation pipeline",
			credentialIdempotencyFunnelCallee)
	}
}

// TestCredentialIdempotencyFunnel_NoSelectorShadow is the A1 blind-spot reverse
// check: no production contractgen code calls a selector named
// rejectUnexemptCredentialResponse (e.g. x.rejectUnexemptCredentialResponse),
// which A1's bare-Ident match would miss.
//
// INVARIANT: CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01 (A1 blind-spot self-check).
func TestCredentialIdempotencyFunnel_NoSelectorShadow(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"tools/codegen/contractgen"})

	diags := Run(t, AST(scope), func(p *Pass) []Diagnostic {
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
				if sel.Sel != nil && sel.Sel.Name == credentialIdempotencyFunnelCallee {
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: p.Fset.Position(sel.Pos()).Line,
						Message: "selector named " + credentialIdempotencyFunnelCallee + " would bypass A1's bare-Ident call-site lock " +
							"(CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01)",
					})
				}
			})
		}
		return d
	})

	Report(t, "CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01", diags)
}

// credentialIdempotencyContractMeta is the minimal contract.yaml view A2 needs.
type credentialIdempotencyContractMeta struct {
	ID        string `yaml:"id"`
	Endpoints struct {
		HTTP *struct {
			Auth struct {
				IdempotencyExempt bool `yaml:"idempotencyExempt"`
			} `yaml:"auth"`
		} `yaml:"http"`
	} `yaml:"endpoints"`
}

// TestCredentialIdempotencyFunnel_FlaggedContractsAreExempt is A2: each of the
// known credential-returning contracts must declare
// endpoints.http.auth.idempotencyExempt: true in its contract.yaml. This is a
// regression guard — if someone removes the flag from a known contract the
// funnel would catch it at generation time, but this archtest makes the failure
// visible earlier and more explicitly.
//
// INVARIANT: CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01 (A2 scope regression guard).
func TestCredentialIdempotencyFunnel_FlaggedContractsAreExempt(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	scope := scanner.DirsScope(
		root, []string{"contracts"},
		scanner.MatchRels(func(rel string) bool {
			return filepath.Base(rel) == "contract.yaml"
		}),
	)

	// Build a map of id → idempotencyExempt from the scanned contract.yaml files.
	found := make(map[string]bool) // contractID → idempotencyExempt value
	var diags []Diagnostic
	scanner.EachContentFile(t, scope, []string{".yaml"}, func(t *testing.T, fc scanner.ContentContext) {
		t.Helper()
		var c credentialIdempotencyContractMeta
		if err := yaml.Unmarshal(fc.Bytes, &c); err != nil {
			diags = append(diags, Diagnostic{Rel: fc.Rel, Message: "parse contract.yaml: " + err.Error()})
			return
		}
		for _, flagged := range credentialIdempotencyFlaggedContracts {
			if c.ID == flagged {
				exempt := c.Endpoints.HTTP != nil && c.Endpoints.HTTP.Auth.IdempotencyExempt
				found[c.ID] = exempt
			}
		}
	})
	Report(t, "CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01", diags)

	for _, contractID := range credentialIdempotencyFlaggedContracts {
		exempt, seen := found[contractID]
		if !seen {
			t.Errorf("CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01: contract %q not found in contracts/ — "+
				"either it was renamed/deleted or credentialIdempotencyFlaggedContracts needs updating",
				contractID)
			continue
		}
		if !exempt {
			t.Errorf("CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01: contract %q response carries credentials "+
				"but endpoints.http.auth.idempotencyExempt is not true — "+
				"this route's responses would be recorded by the idempotency store (Redis 24h TTL), "+
				"persisting live tokens; set idempotencyExempt: true in its contract.yaml",
				contractID)
		}
	}
}
