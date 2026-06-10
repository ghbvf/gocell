package archtest

// ctxkeys_principal_write_caller.go — importable CTXKEYS-PRINCIPAL-WRITE-CALLER-01
// rule logic (#1632 M3 PR-2).
//
// This is the non-test home of the detector logic for CTXKEYS-PRINCIPAL-WRITE-CALLER-01,
// so it can be compiled and run by an external Cell repository (Go never
// compiles a dependency's _test.go, so rule logic external repos must run cannot
// live in a _test.go file). GoCell's own TestCtxkeysPrincipalWriteCaller01 in
// ctxkeys_principal_write_caller_test.go calls the same CheckCtxkeysPrincipalWriteCaller01
// — single source, no parallel rule body.
//
// Not registered in StandardCellRules: allowlist is gocell-hardcoded with no
// ConfigForExternalCell consumer-extension → would false-red an external cell's
// own auth code; kept importable & module-path-agnostic but vacuous-green externally.
//
// # What this guards
//
// outbox.NewEntry injects the Principal family (actor/subject/tenant/session)
// from the construction context — "the single injection trust boundary, no
// producer-facing inject API" (ADR-1042). That claim only holds if the ctx keys
// themselves are written by a trusted writer. The setters
// ctxkeys.With{Actor,Subject,Tenant,Session}ID are exported package functions:
// without enforcement, any same-process producer could call
// ctxkeys.WithActorID(ctx, "evil") and then NewEntry, forging the audit
// identity THROUGH the blessed ctx path — the trust boundary asserted by the
// keys.go godoc ("Authentication middleware populates them") would be
// convention-only.
//
// This archtest pins the callsite identity of all four principal setters to the
// three legitimate writers:
//
//   - runtime/auth/middleware.go — injectPrincipalCtxKeys, the producer-side
//     bridge (shared by the JWT and service-token paths) that runs AFTER
//     authentication at the request trust boundary.
//   - kernel/outbox/principal.go — PrincipalMetadata.RestoreToContext, the
//     consumer-side restore that re-hydrates the entry's principal into handler
//     ctx after the async hop.
//   - kernel/projection/system_principal.go — InstallSystemPrincipal (overwrites all
//     four keys with the "system" sentinel; saga journal replay path, PR-03 #1627)
//     and clearAmbientPrincipal (zeroes all four keys at the Rebuild detach boundary).
//     Both are sanctioned overwrite-writers for the projection principal contract.
//   - kernel/reconcile/identity.go — installSystemProducerIdentity (overwrites all
//     four keys with the "system" sentinel / cleared tenant+session at the Loop's
//     single reconcile chokepoint; #1821). A reconcile loop is a background control
//     loop with no request principal, so the framework positively installs a
//     tenantless system identity for every reconciler's emit instead of inheriting
//     whatever the lifecycle ctx happens to carry. The installer is UNEXPORTED, so
//     only kernel/reconcile.Loop.process can reach it (Go-visibility Hard upstream);
//     this allowlist entry is the Hard downstream lock on the ctxkeys write itself.
//
// No producer (cells/* or examples/*) can write a principal ctx key. Together
// with OUTBOX-RECONSTRUCTION-CALLER-01 (the reconstruction-funnel lock), this
// closes the two non-literal forgery paths the composite-literal seal
// (OUTBOX-ENTRY-SEALED-CONSTRUCTION-01) does not cover.
//
// # Why principal but not observability
//
// The observability ctx setters (WithTraceID / WithSpanID / WithTraceParent /
// WithCorrelationID / WithRequestID) are equally exported and have ~11 production
// callers across runtime/http, adapters/otel, etc. They are deliberately NOT
// locked here: forging a trace id is operationally harmless, whereas forging a
// principal is an audit-identity bypass (P1). The tighter lock on the
// security-sensitive family — and the looser treatment of the observability
// family — is the intended asymmetry, justified by the threat delta, not an
// oversight.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream: HARD by archtest caller-allowlist. The callee is resolved via
//     go/types (ResolvePackageRef), so import aliases and dot-imports resolve to
//     the same symbol. Any setter callsite outside the allowlist fails in CI.
//   - Upstream: MEDIUM, a GO-LANGUAGE CEILING (not a deferred TODO). Hard
//     upstream would require the setters to be unreachable outside the two
//     writers. They cannot be sealed: kernel may depend on pkg/ctxkeys but NOT on
//     runtime/auth, so the setter must live in pkg/ctxkeys and be exported for
//     BOTH writers (different packages) to call — Go visibility cannot express
//     "only runtime/auth + kernel/outbox may call this exported func". Same
//     permanent ceiling as SPAN-SETATTR-HOLDER-SEAL (#851) / HEALTHZ-HOLDER-SEAL
//     (#893 won't-do); tracked here as #1282. The downstream archtest is the enforcement.
//
// # Detection is REFERENCE-based, not call-based
//
// The scanner matches every SelectorExpr that go/types resolves to a setter —
// whether it is the callee of a call OR passed as a function value. This is
// load-bearing here: kernel/outbox.RestoreToContext does NOT call the setters
// directly; it passes them as function values to a higher-order
// withContextMetadata(ctx, val, getter, setter) helper. A call-only scanner
// would miss that legitimate writer AND any producer using the same indirection.
// Reference-based detection catches both at the `ctxkeys.WithActorID` SelectorExpr.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - Dot-import bare-identifier form (import . "…/pkg/ctxkeys"; WithActorID(ctx,x))
//     references the symbol as a bare *ast.Ident, not a SelectorExpr — not matched.
//     Dot-importing pkg/ctxkeys is absent and conspicuous; documented, not enforced.
//   - A bypass in a //go:build-gated production file under a non-default tag is
//     missed by the default-tags scan; principal writers today are default-build.
//   - The anti-vacuity guard (every allowlisted file must reference its setter ≥1×)
//     is the reverse self-check: it proves the scanner resolves the real references
//     and forbids stale allowlist rot (a dead entry is a latent bypass slot).

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"testing"
)

// ctxkeysPkgPath is the canonical import path of pkg/ctxkeys, anchored to
// PlatformModulePath so a module rename updates exactly one place.
const ctxkeysPkgPath = PlatformModulePath + "/pkg/ctxkeys"

// principalSetterAllowlist maps each principal ctx-key setter to the
// module-relative production files allowed to call it. All four setters share
// four writers: the auth request-boundary bridge (producer), the consumer-side
// RestoreToContext, the projection system-principal writer (InstallSystemPrincipal
// + clearAmbientPrincipal, PR-03 #1627), and the reconcile-loop system-producer
// identity installer (installSystemProducerIdentity, #1821).
//
// WithTenantID additionally allows cells/configcore/configcoretest/fakes.go
// (CtxWithTenant helper), which simulates the JWT authenticator's ctx injection
// in test scenarios — the same path used by the JWT authenticator in production
// (per the file's own godoc). This is a test-helper package (configcoretest),
// not a production business code path; the call is semantically equivalent to
// the auth bridge and thus sanctioned. This entry was introduced alongside the
// multi-tenancy epic (#1337 PR-1) but missed the initial allowlist update.
// cells/auditcore/auditcoretest/canonical.go (NewSessionCreatedEntry) is the
// same kind of sanctioned test-helper: it stamps a tenant into the ctx so the
// canonical session.created entry it builds carries one, matching real post-auth
// emits — required since the auditcore appender fail-closed rejects a tenant-less
// business event (#1618 F1).
//
// WithTenantID gained its producer writer (runtime/auth/middleware.go) with the
// multi-tenancy epic (#1337 PR-1): injectPrincipalCtxKeys now writes the JWT
// "tenant_id" claim. This lifted the former ctx-write half of
// INV-SINGLE-TENANT-ONLY (#1289) — the single-writer entry was a deliberate
// placeholder for exactly this moment, not an oversight. The persistence-reach
// tripwire in cells/auditcore/internal/appender was RETIRED in PR-2a (#1340)
// once tenant-scoped audit query filtering landed (the auditquery handler passes
// the authenticated principal's tenant to the tenant-scoped read path): a
// non-empty principal.TenantID now flows cleanly to audit persistence and is
// isolated at query time, so the write-time alarm is no longer needed.
// See ADR 202605281200-1042 §Amendment 2026-05-30.
var principalSetterAllowlist = map[string]map[string]struct{}{
	"WithActorID": {
		"runtime/auth/middleware.go":            {}, // producer bridge (JWT + service-token)
		"kernel/outbox/principal.go":            {}, // consumer-side RestoreToContext
		"kernel/projection/system_principal.go": {}, // saga journal carrier: InstallSystemPrincipal + clearAmbientPrincipal (PR-03 #1627)
		"kernel/reconcile/identity.go":          {}, // reconcile loop system-producer identity: installSystemProducerIdentity (#1821)
	},
	"WithSubjectID": {
		"runtime/auth/middleware.go":            {},
		"kernel/outbox/principal.go":            {},
		"kernel/projection/system_principal.go": {}, // saga journal carrier (PR-03 #1627)
		"kernel/reconcile/identity.go":          {}, // reconcile loop system-producer identity (#1821)
	},
	"WithSessionID": {
		"runtime/auth/middleware.go":            {},
		"kernel/outbox/principal.go":            {},
		"kernel/projection/system_principal.go": {}, // saga journal carrier (PR-03 #1627)
		"kernel/reconcile/identity.go":          {}, // reconcile loop system-producer identity (#1821)
	},
	"WithTenantID": {
		"runtime/auth/middleware.go":                 {}, // producer bridge — JWT tenant_id claim (#1337)
		"kernel/outbox/principal.go":                 {}, // consumer-side RestoreToContext
		"kernel/projection/system_principal.go":      {}, // saga journal carrier (PR-03 #1627)
		"kernel/reconcile/identity.go":               {}, // reconcile loop system-producer identity — clears tenant (#1821)
		"cells/configcore/configcoretest/fakes.go":   {}, // CtxWithTenant test-helper — simulates JWT auth ctx (#1337, missed allowlist)
		"cells/auditcore/auditcoretest/canonical.go": {}, // NewSessionCreatedEntry test-helper — simulates post-auth ctx (#1618 F1)
	},
}

// CheckCtxkeysPrincipalWriteCaller01 runs CTXKEYS-PRINCIPAL-WRITE-CALLER-01 over
// the running module and returns its diagnostics. It is the importable rule body;
// GoCell's TestCtxkeysPrincipalWriteCaller01 calls it directly — single source,
// no parallel rule body.
//
// Not registered in StandardCellRules: the allowlist is gocell-hardcoded
// (runtime/auth/middleware.go, kernel/outbox/principal.go); an external cell's
// own auth code would false-red.
func CheckCtxkeysPrincipalWriteCaller01(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[string]map[string]struct{}{}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		return scanPrincipalSetterCallers(p, observed)
	})
	// Anti-vacuity / no-stale reverse self-check.
	diags = append(diags, stalePrincipalAllowlistDiags(observed)...)
	return diags
}

// scanPrincipalSetterCallers records every principal-setter callsite into
// observed and returns a diagnostic for each callsite outside the per-setter
// allowlist.
func scanPrincipalSetterCallers(p *Pass, observed map[string]map[string]struct{}) []Diagnostic {
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
			setter, matched := principalSetterName(p.TypesInfo, sel)
			if !matched {
				return
			}
			recordPrincipalSetterObserved(observed, setter, rel)
			if _, allowed := principalSetterAllowlist[setter][rel]; allowed {
				return
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: p.Fset.Position(sel.Pos()).Line,
				Message: fmt.Sprintf(
					"CTXKEYS-PRINCIPAL-WRITE-CALLER-01: ctxkeys.%s is called from %s, which is not a "+
						"sanctioned principal writer. Writing a principal ctx key lets outbox.NewEntry stamp "+
						"that identity onto produced entries — only the auth request-boundary bridge "+
						"(runtime/auth) and the consumer-side RestoreToContext (kernel/outbox) may write it. "+
						"Producers MUST NOT set principal ctx keys; the identity comes from authentication. "+
						"If this IS a new sanctioned writer, add it to principalSetterAllowlist with rationale.",
					setter, rel,
				),
			})
		})
	}
	return diags
}

// recordPrincipalSetterObserved marks (setter, rel) as a live callsite for the
// anti-vacuity reverse self-check.
func recordPrincipalSetterObserved(observed map[string]map[string]struct{}, setter, rel string) {
	if observed[setter] == nil {
		observed[setter] = map[string]struct{}{}
	}
	observed[setter][rel] = struct{}{}
}

// stalePrincipalAllowlistDiags is the anti-vacuity reverse self-check: every
// allowlist entry must correspond to a live observed callsite, else it is a dead
// bypass slot.
func stalePrincipalAllowlistDiags(observed map[string]map[string]struct{}) []Diagnostic {
	var diags []Diagnostic
	for setter, files := range principalSetterAllowlist {
		allowed := make([]string, 0, len(files))
		for f := range files {
			allowed = append(allowed, f)
		}
		sort.Strings(allowed)
		for _, f := range allowed {
			if _, seen := observed[setter][f]; seen {
				continue
			}
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"CTXKEYS-PRINCIPAL-WRITE-CALLER-01: allowlist entry %q for ctxkeys.%s is STALE — no "+
						"live call observed. Either the scanner regressed or the call was removed; drop the dead "+
						"allowlist entry so it cannot become a silent bypass slot.",
					f, setter,
				),
			})
		}
	}
	return diags
}

// principalSetterName resolves a SelectorExpr REFERENCE (call or function value)
// to one of the four principal ctx-key setters in pkg/ctxkeys (alias-proof via
// go/types). Returns ("", false) otherwise.
func principalSetterName(info *types.Info, sel *ast.SelectorExpr) (string, bool) {
	pkgPath, name, ok := ResolvePackageRef(info, sel)
	if !ok || pkgPath != ctxkeysPkgPath {
		return "", false
	}
	if _, isSetter := principalSetterAllowlist[name]; !isSetter {
		return "", false
	}
	return name, true
}
