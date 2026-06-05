// ctxkeys_realip_read_caller_test.go — closes the SOURCE side of the client-IP
// PII funnel (#1488).
//
//   - INVARIANT: CTXKEYS-REALIP-READ-CALLER-01
//
// # What this guards
//
// The sink side of the funnel (CLIENT-IP-HASH-FUNNEL-01) makes it a compile
// error to emit a plaintext IP into the bootstrap-failed event payload: the DTO
// field is the sealed redaction.IPHash, constructible only via redaction.HashIP.
// But that only protects the ONE known payload. A future producer could read the
// raw client IP from the request context (ctxkeys.RealIPFrom) and pipe it into a
// NEW event payload, a log line, or any other replayable sink, re-opening the
// exact PII-spread hole #1488 closed.
//
// This archtest pins every production REFERENCE of ctxkeys.RealIPFrom (the only
// reader of the plaintext client IP) to a bounded allowlist of legitimate
// readers. Any new reader must be added here with a rationale — that review
// checkpoint is what stops plaintext IP from silently flowing into a new sink.
// It is the source-side complement to the sink-side sealed type; together they
// form a two-sided funnel for the client IP.
//
// # Legitimate readers (today)
//
//   - runtime/auth/bootstrap.go — bootstrapClientIP, rate-limit keying (in-memory,
//     not persisted).
//   - runtime/http/middleware/rate_limit.go — rate-limit keying (in-memory).
//   - runtime/http/middleware/access_log.go — the operational access log
//     "real_ip" field. Operational access logs legitimately carry the client IP
//     (standard web-server practice, ops/security), are server-side, and are NOT
//     a replayable event payload — explicitly out of #1488 scope.
//   - cellmodules/accesscore/module.go — the bootstrap observer, which hashes the
//     IP via redaction.HashIP before it reaches slog or the wire payload.
//   - examples/ssobff/app.go — the ssobff bootstrap observer (same, keyed hash).
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream: HARD by archtest caller-allowlist. The callee is resolved via
//     go/types (ResolvePackageRef), so import aliases (the access_log.go site
//     uses `pkgctxkeys.RealIPFrom`) and function-value references resolve to the
//     same symbol. Any reader outside the allowlist fails CI.
//   - Upstream: MEDIUM, a GO-LANGUAGE CEILING (not a deferred TODO). Hard
//     upstream would require RealIPFrom to be unreachable outside the allowlist;
//     it cannot be sealed — pkg/ctxkeys must export it for runtime/, cellmodules/
//     and examples/ (different packages) to call, and Go visibility cannot
//     express "only these N files may call this exported func". Same permanent
//     ceiling as CTXKEYS-PRINCIPAL-WRITE-CALLER-01 (#1282) / SPAN-SETATTR-
//     HOLDER-SEAL (#851) / HEALTHZ-HOLDER-SEAL (#893) — a won't-do Go ceiling,
//     not a fixable task. The downstream archtest is the enforcement; the generic
//     replayable-payload PII Hard mechanism is tracked at gh #1605.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - Detection is REFERENCE-based and scans every *ast.Ident (go/types resolves
//     it to RealIPFrom, whether call, function value, qualified `ctxkeys.RealIPFrom`
//     `.Sel`, OR a dot-imported bare `RealIPFrom`). Load-bearing two ways:
//     access_log.go passes RealIPFrom as a function value (not a direct call); and
//     the bare-ident scan closes the former dot-import gap (#1488 F2) —
//     ResolvePackageRef's resolveBarePkgSymbol resolves a bare *ast.Ident to the
//     same *types.Func, so `import . "…/pkg/ctxkeys"; RealIPFrom(ctx)` is now caught.
//   - //go:build-gated production files under a non-default tag are missed by the
//     default-tags scan (the integration test observers are *_test.go + tagged,
//     so out of the Production(Tests:false) scope anyway).
//   - The anti-vacuity guard (every allowlisted file must reference RealIPFrom
//     ≥1×) is the reverse self-check: it proves the scanner resolves the real
//     references and forbids a stale entry becoming a silent bypass slot.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"testing"
)

// realIPReadAllowlist is the set of module-relative production files allowed to
// reference ctxkeys.RealIPFrom. See the file godoc for each entry's rationale.
var realIPReadAllowlist = map[string]struct{}{
	"runtime/auth/bootstrap.go":             {}, // rate-limit keying (in-memory)
	"runtime/http/middleware/rate_limit.go": {}, // rate-limit keying (in-memory)
	"runtime/http/middleware/access_log.go": {}, // operational access log real_ip
	"cellmodules/accesscore/module.go":      {}, // bootstrap observer → redaction.HashIP
	"examples/ssobff/app.go":                {}, // ssobff bootstrap observer → redaction.HashIP
}

// TestCtxkeysRealIPReadCaller01 asserts every production reference of
// ctxkeys.RealIPFrom sits in realIPReadAllowlist, and that no allowlist entry is
// stale (anti-vacuity reverse check).
func TestCtxkeysRealIPReadCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[string]struct{}{}

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			// Scan every *ast.Ident (not just SelectorExpr): ResolvePackageRef
			// resolves both the `.Sel` of a qualified `ctxkeys.RealIPFrom` AND a
			// bare `RealIPFrom` from a dot-import — closing the dot-import gap
			// (#1488 F2). Each callsite has exactly one matching ident, so there
			// is no double-count.
			EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
				if !isRealIPFromRef(p.TypesInfo, id) {
					return
				}
				observed[rel] = struct{}{}
				if _, allowed := realIPReadAllowlist[rel]; !allowed {
					pos := p.Fset.Position(id.Pos())
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"CTXKEYS-REALIP-READ-CALLER-01: ctxkeys.RealIPFrom is referenced from %s, which is "+
								"not a sanctioned reader of the plaintext client IP. Reading the raw IP risks "+
								"piping PII into a new replayable sink (the exact #1488 hole). Legitimate readers "+
								"are rate-limit keying, the operational access log, and the bootstrap observers "+
								"(which hash via redaction.HashIP). If this IS a new sanctioned reader, add it to "+
								"realIPReadAllowlist with a rationale; if it feeds a payload/log, hash via "+
								"redaction.HashIP first.",
							rel,
						),
					})
				}
			})
		}
		return d
	})

	// Anti-vacuity / no-stale reverse self-check.
	allowed := make([]string, 0, len(realIPReadAllowlist))
	for f := range realIPReadAllowlist {
		allowed = append(allowed, f)
	}
	sort.Strings(allowed)
	for _, f := range allowed {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"CTXKEYS-REALIP-READ-CALLER-01: allowlist entry %q is STALE — no live ctxkeys.RealIPFrom "+
						"reference observed. Either the scanner regressed or the reference was removed; drop "+
						"the dead allowlist entry so it cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "CTXKEYS-REALIP-READ-CALLER-01", diags)
}

// isRealIPFromRef reports whether expr is a REFERENCE (call, function value, or
// dot-imported bare ident) to pkg/ctxkeys.RealIPFrom, alias-proof via go/types.
// ResolvePackageRef accepts both *ast.SelectorExpr and *ast.Ident.
func isRealIPFromRef(info *types.Info, expr ast.Expr) bool {
	pkgPath, name, ok := ResolvePackageRef(info, expr)
	return ok && pkgPath == ctxkeysPkgPath && name == "RealIPFrom"
}
