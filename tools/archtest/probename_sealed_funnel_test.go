// INVARIANT: PROBENAME-SEALED-FUNNEL-01
//
// probename_sealed_funnel_test.go — typed-string concept funnel for
// kernel/healthz.ProbeName (replaces OPS-CONTRACT-STRING-FUNNEL-01 and
// READYZ-PROBE-NAMING-01, which are deleted).
//
// # Sanctioned declaration packages (ProbeName const set)
//
//   - kernel/healthz — framework-level typed const (ConfigWatcherProbeName,
//     ConfigDriftProbeName) and composed-name constructor (EmitterFailOpenProbeName).
//   - adapters/grpc — ProbeReady
//   - adapters/postgres — ProbeReady, ProbeIndexesValidReady
//   - adapters/redis — ProbeReady
//   - adapters/rabbitmq — ProbeReady
//   - adapters/s3 — ProbeReady
//   - adapters/vault — ProbeReady
//   - adapters/mqtt — ProbeReady
//   - adapters/oidc — ProbeReady
//   - runtime/outbox — ProbePoll, ProbeReclaim, ProbeCleanup (relay operation probes, no _ready suffix)
//   - runtime/websocket — ProbeReady
//   - runtime/saga — ProbeCoordinatorReady
//   - cells/{configcore,auditcore,accesscore}/healthz_gen.go — ProbeRepoReady (cellgen marker required)
//   - examples/{iotdevice/cells/devicecell,todoorder/cells/ordercell}/healthz_gen.go — ProbeRepoReady (cellgen marker required)
//
// # Golden inventory (sorted "<module-relative-pkg>.<ConstName>=<value>")
//
// See goldenProbeNames() for the full, authoritative list.  Operator hint: if
// CI red-flags a "golden inventory mismatch" on a new probe, either add the
// const to the sanctioned package set AND add the entry to goldenProbeNames()
// in the same PR, or explain why the new const does not need funnel coverage
// in an ADR.
//
// # Sub-rule index
//
//   - A1: declaration sanction + value shape (archtest, Medium upstream — Go ceiling)
//   - A2: callsite resolves to declared const, 4 sanctioned callees (archtest + type system, Hard downstream)
//   - A3: Aggregator.Register allowlist (archtest + type system, Hard downstream)
//   - A4: NewProbeName caller allowlist (archtest, Medium upstream)
//   - A4b: MustProbeName caller allowlist (archtest, Medium upstream)
//   - A5: EmitterFailOpenProbeName composed-name BinaryExpr (archtest, Medium upstream)
//   - A6: Probe interface sealed marker `isHealthzProbe()` — Go compiler gate;
//     no archtest rule needed; cross-checked by B5 blind-spot for in-package impls.
//     Note: A6 in earlier PR-03 revisions also denoted a projection-prefix string-
//     anchor scan (Soft per ai-robust.md "Soft 严禁立项"). That scan was removed in
//     PR-03 round-3; the projection composed-name funnel is already Hard-closed by
//     A2 (cast ban) + A4 (NewProbeName caller allowlist) + sole constructors
//     (ProjectionStoreReadyProbeName / ProjectionLagProbeName).
//   - B1: reflect.MethodByName("RegisterReadiness") blind-spot
//   - B3: ProbeName(callExpr) cast blind-spot
//   - B5: in-package new isHealthzProbe() implementer blind-spot
//
// # AI-robust grading (per .claude/rules/gocell/ai-robust.md §Funnel 双向锁评级)
//
//   - A1 upstream (declaration sanction + value shape):
//     Medium archtest-bound — permanent Go-language ceiling. Const visibility
//     is package-scoped; Go has no const-seal syntax preventing external
//     packages from declaring `const X ProbeName = "..."`. archtest
//     sanctionedPkgs + value-shape regex + adapter-suffix overlay is the
//     highest achievable form on Go. No follow-up issue (won't-do, lang
//     ceiling).
//   - A2 downstream (callsite resolves to declared const):
//     Hard downstream via **two complementary gates**, not type system alone.
//     The 4 sanctioned callees take `name ProbeName` (not `string`) as their
//     first parameter — RegisterReadiness / NewProbe / HealthToProbe /
//     bootstrap.WithHealthChecker. Go's type system enforcement is partial:
//     (a) typed-var path:  `var s string; Register(s, p)` IS a compile
//     error (`string` and `ProbeName` are distinct named types; no
//     implicit conversion across them in this direction).
//     (b) untyped-literal path:  `Register("foo", p)` COMPILES — Go's
//     untyped string constant "foo" is implicitly converted to
//     ProbeName per Go spec. The type system alone does NOT reject
//     bare string literals at typed-arg positions.
//     archtest A2 closes path (b): the callsite must resolve via info.Uses
//     to a sanctioned `*types.Const`; BasicLit / BinaryExpr / Var / CallExpr
//     fail closed. The combined gate is Hard downstream; neither gate alone
//     would be.
//   - A3 downstream (Aggregator.Register allowlist):
//     Hard downstream via type system (Registrar.Healthz() removed — any
//     attempt is a compile error). Archtest enforces the residual direct-
//     Aggregator.Register callsite set (Medium upstream archtest caller-identity
//     backstop; see HEALTHZ-WRITE-01/A3 for holder allowlist).
//   - A4 upstream (NewProbeName caller allowlist):
//     Medium archtest — Go-language ceiling (no sealed-construction path
//     for a function with arbitrary string arg).
//   - A5 upstream (EmitterFailOpenProbeName sole composed-name constructor):
//     Medium archtest — BinaryExpr string concat `"outbox_failopen_rate_" + x`
//     outside kernel/healthz is rejected.
//   - A6 upstream (Probe interface implementation set):
//     Hard upstream via type system — Probe interface carries an unexported
//     `isHealthzProbe()` marker method; external packages cannot implement
//     Probe (compile error). Only `kernel/healthz.funcProbe` (returned by
//     NewProbe) and `kernel/healthz.ctxSafeProbe` (returned by WrapCtxSafe)
//     can satisfy. No archtest rule needed — Go compiler is the gate.
//     Cross-checked by B5 (in-package new implementer blind-spot).
//
// # Blind-spot assertions (B class)
//
//   - B1 (reflect bypass): reflect.ValueOf(reg).MethodByName("RegisterReadiness")
//     in non-test code — expected absent in production AST.
//   - B2 (helper wrapper indirection): non-allowlisted file defines a
//     func Register*(reg cell.Registrar, ...) body that calls reg.RegisterReadiness —
//     expected absent except in cellgen healthz_gen.go and kernel/cell/healthz.go.
//   - B3 (string-cast bypass): healthz.ProbeName(callExpr) where the argument
//     is a function call (Sprintf, Join, etc.) — expected absent in production AST.
//   - B4 (MustProbeName production bypass): healthz.MustProbeName(runtimeStr) calls
//     in non-allowlist production files — availability risk since MustProbeName
//     panics on invalid input. Covered by A4b scanner (caller allowlist).
//     Post-F1A/F1B, zero production callers outside kernel/healthz/probename.go
//   - healthztest/conformance.go.
//   - B5 (in-package new isHealthzProbe() implementer): a new struct inside
//     kernel/healthz implementing the sealed marker method beyond the two
//     sanctioned types (funcProbe, ctxSafeProbe) — expected absent.
//     The A6 type-system gate only covers external packages; B5 closes the
//     in-package gap via archtest AST scan.
//
// ref: kernel/healthz.ProbeName — typed concept type
// ref: kernel/healthz.NewProbeName — sole validated entry point
// ref: kernel/healthz.MustProbeName — kernel-internal + test-only panic variant
// ref: kernel/healthz.EmitterFailOpenProbeName — sole composed-name constructor
// ref: HEALTHZ-WRITE-01 (healthz_invariants_test.go) — A1 HTTP ban + A3 holder allowlist (orthogonal)
// ref: CELL-REPO-READYZ-PROBE-01 (cell_repo_readyz_probe_test.go) — enrollment backstop (orthogonal)
// ref: ADR docs/architecture/202605271100-adr-probename-sealed-funnel.md
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// ─── Constants ────────────────────────────────────────────────────────────────
//
// healthzPkgPath, cellRegistrarPkgPath, adapterutilPkgPath, bootstrapPkgPath,
// probeNameSanctionedPkgs, cellgenSanctionedPkgs, adapterSanctionedPkgs,
// probenameModPrefix, probenameInRepoLayerPrefixes, and probenameIsInRepoPkg
// are declared in probename_sealed_funnel.go (non-test, same package) so that
// all "github.com/ghbvf/gocell/..." literals are derived from PlatformModulePath
// (Shape B, M3 #1302).  Only non-path constants remain here.

// aggregatorRegisterAllowlist is the set of module-relative path suffixes that
// are allowed to call healthz.Aggregator.Register directly.  All other
// production code must go through reg.RegisterReadiness (which internally
// calls healthz.NewProbe and accumulates into the RegistryRecorder, later
// drained by bootstrap onto the Aggregator).
var aggregatorRegisterAllowlist = map[string]bool{
	// Bootstrap drains RegistrySnapshot.Probes and registers framework probes:
	"runtime/bootstrap/bootstrap_phases.go": true,
	"runtime/bootstrap/phases_lifecycle.go": true,
	// Aggregator conformance test helper (not production code path):
	"runtime/observability/healthz/healthztest/conformance.go": true,
	// kernel/cell is the Registrar implementation — registerProbe body:
	"kernel/cell/registry.go": true,
	// kernel/cell/healthz.go registers emitter probes via reg.RegisterReadiness
	// (not Aggregator.Register) but is listed here defensively to avoid
	// false-positive if the inner funnel is refactored:
	"kernel/cell/healthz.go": true,
}

// newProbeNameAllowlist is the set of module-relative path suffixes that are
// allowed to call healthz.NewProbeName directly in production code (non-test).
// kernel/healthz/probename.go is the validator implementation; all other
// production code constructs typed ProbeName values via declared typed consts
// (A4 allowlist; post-F1A/F1B, ManagedResource.Probes() returns typed
// healthz.Probe values directly — no bare-string ingress at the boundary).
var newProbeNameAllowlist = map[string]bool{
	"kernel/healthz/probename.go": true,
}

// mustProbeNameAllowlist is the set of module-relative path suffixes that are
// allowed to call healthz.MustProbeName in production code (non-test).
// MustProbeName is the panic variant for programmer-error sites. The only
// sanctioned production sites are the declaration file itself (kernel/healthz)
// and conformance test fixtures — all test files (*_test.go suffix) are
// globally exempt. All other production callers fail A4b.
//
// B4 blind-spot: `healthz.MustProbeName(runtimeStr)` calls in non-allowlist
// production files where the arg is a runtime variable rather than a literal
// — detected by A4b caller allowlist; the runtime-string case collapses to the
// same scan since MustProbeName itself is the call to reject regardless of
// argument form.
var mustProbeNameAllowlist = map[string]bool{
	// Declaration site — MustProbeName is defined here.
	"kernel/healthz/probename.go": true,
	// Conformance test infrastructure that calls MustProbeName with literals
	// to set up probe fixtures. The file is production-path (no _test.go
	// suffix) but is explicitly test-infrastructure, not business logic.
	"runtime/observability/healthz/healthztest/conformance.go": true,
}

// ─── Golden inventory ─────────────────────────────────────────────────────────

// goldenProbeNames returns the authoritative sorted list of every
// healthz.ProbeName typed const in the production tree, in the format
// "<module-relative-pkg-path>.<ConstName>=<value>".
//
// Adding a new adapter/framework/cellgen probe REQUIRES adding an entry here
// in the same PR (enforced by A1 golden lock in TestProbenameSealedFunnel).
func goldenProbeNames() []string {
	names := []string{
		// kernel/healthz — framework constants
		"kernel/healthz.ConfigDriftProbeName=config_drift",
		"kernel/healthz.ConfigWatcherProbeName=config_watcher",
		"kernel/healthz.EventRouterProbeName=event_router",
		// adapter probes (all _ready suffix)
		"adapters/grpc.ProbeReady=grpc_ready",
		"adapters/mqtt.ProbeReady=mqtt_ready",
		"adapters/oidc.ProbeReady=oidc_ready",
		"adapters/postgres.ProbeIndexesValidReady=postgres_indexes_valid_ready",
		"adapters/postgres.ProbeReady=postgres_ready",
		"adapters/rabbitmq.ProbeReady=rabbitmq_ready",
		"adapters/redis.ProbeHTTPIdempotencyStoreReady=http_idempotency_store_ready",
		"adapters/redis.ProbeReady=redis_ready",
		"adapters/s3.ProbeReady=s3_ready",
		"adapters/vault.ProbeReady=vault_transit_ready",
		// runtime probes — outbox relay probes (no _ready suffix: relay operation, not dep availability)
		"runtime/outbox.ProbeCleanup=outbox_relay_cleanup",
		"runtime/outbox.ProbePoll=outbox_relay_poll",
		"runtime/outbox.ProbeReclaim=outbox_relay_reclaim",
		// runtime probes (all _ready suffix)
		"runtime/saga.ProbeCoordinatorReady=saga_coordinator_ready",
		"runtime/websocket.ProbeReady=websocket_hub_ready",
		// cellgen repo probes (all _repo_ready suffix)
		"cells/accesscore.ProbeRepoReady=accesscore_repo_ready",
		"cells/auditcore.ProbeRepoReady=auditcore_repo_ready",
		"cells/configcore.ProbeRepoReady=configcore_repo_ready",
		"examples/iotdevice/cells/devicecell.ProbeRepoReady=devicecell_repo_ready",
		"examples/orderfulfillment/cells/orderfulfillmentcell.ProbeRepoReady=orderfulfillmentcell_repo_ready",
		"examples/todoorder/cells/ordercell.ProbeRepoReady=ordercell_repo_ready",
		"examples/webhookdemo/cells/hooks.ProbeRepoReady=hooks_repo_ready",
	}
	sort.Strings(names)
	return names
}

// ─── Helper predicates ────────────────────────────────────────────────────────

// isProbeNameTypedConst reports whether obj is a *types.Const whose type
// resolves to kernel/healthz.ProbeName.
func isProbeNameTypedConst(obj types.Object) bool {
	c, ok := obj.(*types.Const)
	if !ok {
		return false
	}
	named, ok := c.Type().(*types.Named)
	if !ok {
		return false
	}
	tobj := named.Obj()
	return tobj.Pkg() != nil &&
		tobj.Pkg().Path() == healthzPkgPath &&
		tobj.Name() == "ProbeName"
}

// isProbeNameTypeConversion reports whether expr is a type-conversion of the
// form healthz.ProbeName(<arg>) where arg is not a simple identifier / const.
// Returns (arg, true) when the form matches.
func isProbeNameTypeConversion(call *ast.CallExpr, info *types.Info) (ast.Expr, bool) {
	if len(call.Args) != 1 || call.Ellipsis != token.NoPos {
		return nil, false
	}
	// The "callee" of a type conversion is the type itself, which appears as
	// a SelectorExpr (healthz.ProbeName) or Ident (after dot-import).
	tv, ok := info.Types[call.Fun]
	if !ok || !tv.IsType() {
		return nil, false
	}
	named, ok := tv.Type.(*types.Named)
	if !ok {
		return nil, false
	}
	tobj := named.Obj()
	if tobj.Pkg() == nil || tobj.Pkg().Path() != healthzPkgPath || tobj.Name() != "ProbeName" {
		return nil, false
	}
	return call.Args[0], true
}

// isRegisterReadinessCall reports whether call is a call to
// cell.Registrar.RegisterReadiness (via *types.Info.Selections).
func isRegisterReadinessCall(call *ast.CallExpr, info *types.Info) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "RegisterReadiness" {
		return false
	}
	fn, ok := ResolveMethodCall(info, sel)
	if !ok || fn == nil {
		return false
	}
	return fn.Pkg() != nil && fn.Pkg().Path() == cellRegistrarPkgPath
}

// isNewProbeCall reports whether call is a call to healthz.NewProbe.
func isNewProbeCall(call *ast.CallExpr, info *types.Info) bool {
	pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
	return ok && pkgPath == healthzPkgPath && name == "NewProbe"
}

// isHealthToProbeCall reports whether call is a call to
// adapterutil.HealthToProbe.
func isHealthToProbeCall(call *ast.CallExpr, info *types.Info) bool {
	pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
	return ok && pkgPath == adapterutilPkgPath && name == "HealthToProbe"
}

// isWithHealthCheckerCall reports whether call is bootstrap.WithHealthChecker.
// This is the 4th sanctioned ProbeName funnel ingress (composition-root option
// pattern). Without scanning this callee, an author writing
// `WithHealthChecker("raw_literal", fn)` would compile via untyped-const →
// ProbeName implicit conversion and bypass the funnel.
func isWithHealthCheckerCall(call *ast.CallExpr, info *types.Info) bool {
	pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
	return ok && pkgPath == bootstrapPkgPath && name == "WithHealthChecker"
}

// firstArgResolvesToConst reports whether the first argument of call resolves,
// via info.Uses, to a declared *types.Const of type ProbeName.
func firstArgResolvesToConst(call *ast.CallExpr, info *types.Info) bool {
	if len(call.Args) == 0 {
		return false
	}
	arg := call.Args[0]
	// Unwrap a single-level ProbeName(...) cast — adapter callsites write
	// adapterutil.HealthToProbe(string(postgres.ProbeReady), ...) or
	// adapterutil.HealthToProbe(postgres.ProbeReady, ...) — both are
	// legitimate only when the inner value is a sanctioned const.
	if castCall, ok := arg.(*ast.CallExpr); ok {
		if inner, isCast := isProbeNameTypeConversion(castCall, info); isCast {
			arg = inner
		}
	}
	// Resolve the (possibly unwrapped) argument to a const.
	ident, ok := arg.(*ast.Ident)
	if !ok {
		if sel, ok2 := arg.(*ast.SelectorExpr); ok2 {
			ident = sel.Sel
		} else {
			return false
		}
	}
	obj, ok := info.Uses[ident]
	if !ok {
		return false
	}
	return isProbeNameTypedConst(obj)
}

// isNewProbeNameCall reports whether call is a direct call to
// healthz.NewProbeName.
func isNewProbeNameCall(call *ast.CallExpr, info *types.Info) bool {
	pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
	return ok && pkgPath == healthzPkgPath && name == "NewProbeName"
}

// isMustProbeNameCall reports whether call is a direct call to
// healthz.MustProbeName.
func isMustProbeNameCall(call *ast.CallExpr, info *types.Info) bool {
	pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
	return ok && pkgPath == healthzPkgPath && name == "MustProbeName"
}

// ─── Scanner functions ────────────────────────────────────────────────────────

// scanA1DeclarationSanction scans file for healthz.ProbeName typed consts
// declared outside the sanctioned package set or (for cellgen packages)
// missing the cellgen marker.
func scanA1DeclarationSanction(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
	pkg *types.Package,
	absPath string,
) []Diagnostic {
	if info == nil || pkg == nil {
		return nil
	}
	pkgPath := pkg.Path()

	var out []Diagnostic

	EachInSubtree[ast.GenDecl](file, func(gd *ast.GenDecl) {
		EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
			for i, name := range vs.Names {
				// Multi-const ValueSpec (e.g. `const ( a, b ProbeName = x, y )`)
				// pairs Names[i] with Values[i] — taking Values[0] for every
				// name lets the 2nd+ const's value-shape and adapter-suffix
				// checks slip through silently. Same indexing as the golden
				// inventory collector (collectProbeNameConsts) downstream.
				if len(vs.Values) <= i {
					continue
				}
				obj, ok := info.Defs[name]
				if !ok {
					continue
				}
				if !isProbeNameTypedConst(obj) {
					continue
				}

				// Value-shape applies to every ProbeName const regardless of
				// package: the value must pass the same NewProbeName validator
				// that runtime callers run. Without this, an author could
				// declare a typed const with a wire-illegal value (hyphens,
				// uppercase, double-underscore, >64 chars) and the only
				// failure point would be far from the declaration site when
				// a composed-name constructor re-validates the prefix or the
				// metric backend rejects the label. Same regex + length budget
				// as kernel/healthz.NewProbeName. Checked first so a single
				// `continue` for the package-sanction violation does not skip
				// it; value-shape and package-sanction are independent axes
				// and both must surface when both fail.
				if val, ok := EvaluateConstString(info, vs.Values[i]); ok {
					if _, err := healthz.NewProbeName(val); err != nil {
						pos := fset.Position(name.Pos())
						out = append(out, Diagnostic{
							Rel:  rel,
							Line: pos.Line,
							Message: fmt.Sprintf(
								"PROBENAME-SEALED-FUNNEL-01/A1: ProbeName const %q in %q "+
									"has value %q that fails healthz.NewProbeName validator: %v",
								name.Name, pkgPath, val, err,
							),
						})
					}
				}

				// Const is of type ProbeName — enforce package sanction.
				if !probeNameSanctionedPkgs[pkgPath] {
					pos := fset.Position(name.Pos())
					out = append(out, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"PROBENAME-SEALED-FUNNEL-01/A1: ProbeName const %q declared in "+
								"non-sanctioned package %q — only adapter/framework/cellgen "+
								"packages may declare ProbeName consts",
							name.Name, pkgPath,
						),
					})
					continue
				}

				// Cellgen packages require the marker.
				if cellgenSanctionedPkgs[pkgPath] && !fileHasCellgenMarker(absPath) {
					pos := fset.Position(name.Pos())
					out = append(out, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"PROBENAME-SEALED-FUNNEL-01/A1: ProbeName const %q in cellgen "+
								"package %q must be in a file with cellgen marker %q",
							name.Name, pkgPath, cellgenMarkerLine,
						),
					})
					continue
				}

				// Adapter / runtime packages require _ready suffix.
				if adapterSanctionedPkgs[pkgPath] {
					val, ok := EvaluateConstString(info, vs.Values[i])
					if ok && !strings.HasSuffix(val, "_ready") {
						pos := fset.Position(name.Pos())
						out = append(out, Diagnostic{
							Rel:  rel,
							Line: pos.Line,
							Message: fmt.Sprintf(
								"PROBENAME-SEALED-FUNNEL-01/A1: adapter/runtime ProbeName const "+
									"%q in %q has value %q — must end with \"_ready\" suffix",
								name.Name, pkgPath, val,
							),
						})
					}
				}
			}
		})
	})

	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// a2FunnelInternalAllowlist names files that implement the funnel itself —
// they legitimately pass ProbeName values that originated upstream (method
// call on Probe, struct field, function parameter) rather than referencing a
// declared const at their callsite. A2's resolve-to-const discipline is the
// caller-side contract for *consumer* code; the funnel implementation is the
// authority that consumers terminate against.
var a2FunnelInternalAllowlist = map[string]struct{}{
	// adapterutil.HealthToProbe wraps its ProbeName parameter through to
	// healthz.NewProbe — the parameter originates as a sanctioned const at
	// the caller's adapter declaration site (e.g. redis.ProbeReady).
	"adapters/adapterutil/health.go": {},
	"kernel/cell/healthz.go":         {},
	"kernel/cell/registry.go":        {},
	"kernel/outbox/emitter.go":       {},
	// projection.Coordinator.Probes composes probe names via the two sanctioned
	// constructors ProjectionStoreReadyProbeName / ProjectionLagProbeName and
	// passes the results to NewProbe — same funnel-internal shape as emitter.go
	// (names are runtime compositions of cellID+projectionID, not consts).
	"kernel/projection/probe.go":                               {},
	"runtime/bootstrap/bootstrap_phases.go":                    {},
	"runtime/bootstrap/phases_lifecycle.go":                    {},
	"runtime/observability/healthz/healthztest/conformance.go": {},
}

// scanA2CallsiteResolves scans file for RegisterReadiness / NewProbe /
// HealthToProbe calls where the name argument does not resolve to a
// sanctioned *types.Const.  Also catches ProbeName(callExpr) casts where the
// argument is not a const (string-conversion bypass).
//
// Files in a2FunnelInternalAllowlist are exempt from the resolve-to-const
// check (they pass ProbeName through from parameters / fields / method calls),
// but the ProbeName(callExpr) string-cast detection (case 4) still applies
// universally — there is no legitimate runtime-string-to-ProbeName cast.
func scanA2CallsiteResolves(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
) []Diagnostic {
	if info == nil {
		return nil
	}
	var out []Diagnostic

	_, funnelInternal := a2FunnelInternalAllowlist[rel]

	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		// Case 1: RegisterReadiness(name, ...) — name must be sanctioned const
		// (consumer-side discipline). Funnel-internal files are exempt.
		if isRegisterReadinessCall(call, info) {
			if !funnelInternal && !firstArgResolvesToConst(call, info) {
				pos := fset.Position(call.Pos())
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"PROBENAME-SEALED-FUNNEL-01/A2: RegisterReadiness name arg at %s:%d "+
							"does not resolve to a declared ProbeName const "+
							"(bare string, var, or dynamic expr prohibited)",
						rel, pos.Line,
					),
				})
			}
			return
		}

		// Case 1.5: bootstrap.WithHealthChecker(name, fn) — name must
		// resolve to sanctioned const. WithHealthChecker is the 4th
		// ProbeName funnel ingress (composition-root option path; phase4
		// drains into RegistryRecorder). Funnel-internal files exempt.
		if isWithHealthCheckerCall(call, info) {
			if !funnelInternal && !firstArgResolvesToConst(call, info) {
				pos := fset.Position(call.Pos())
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"PROBENAME-SEALED-FUNNEL-01/A2: WithHealthChecker name arg at %s:%d "+
							"does not resolve to a declared ProbeName const "+
							"(bare string, var, or dynamic expr prohibited)",
						rel, pos.Line,
					),
				})
			}
			return
		}

		// Case 2: healthz.NewProbe(name, fn) — name must be sanctioned const
		// (consumer-side). Funnel-internal files exempt.
		if isNewProbeCall(call, info) {
			if !funnelInternal && !firstArgResolvesToConst(call, info) {
				pos := fset.Position(call.Pos())
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"PROBENAME-SEALED-FUNNEL-01/A2: NewProbe name arg at %s:%d "+
							"does not resolve to a declared ProbeName const "+
							"(bare string, var, or dynamic expr prohibited)",
						rel, pos.Line,
					),
				})
			}
			return
		}

		// Case 3: adapterutil.HealthToProbe(name, ...) — name must be sanctioned const.
		if isHealthToProbeCall(call, info) {
			if !firstArgResolvesToConst(call, info) {
				pos := fset.Position(call.Pos())
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"PROBENAME-SEALED-FUNNEL-01/A2: HealthToProbe name arg at %s:%d "+
							"does not resolve to a declared ProbeName const "+
							"(bare string, var, or dynamic expr prohibited)",
						rel, pos.Line,
					),
				})
			}
			return
		}

		// Case 4: ProbeName(callExpr) type conversion — the arg must not be a CallExpr.
		// e.g. healthz.ProbeName(fmt.Sprintf("x_%s", id)) is prohibited.
		if inner, isCast := isProbeNameTypeConversion(call, info); isCast {
			if _, isCallArg := inner.(*ast.CallExpr); isCallArg {
				pos := fset.Position(call.Pos())
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"PROBENAME-SEALED-FUNNEL-01/A2: ProbeName(callExpr) at %s:%d "+
							"is a string-cast bypass — use healthz.NewProbeName or a "+
							"sanctioned composed-name constructor",
						rel, pos.Line,
					),
				})
			}
		}
	})

	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// scanA3AggregatorRegisterAllowlist scans file for direct Aggregator.Register
// calls outside the sanctioned allowlist.
func scanA3AggregatorRegisterAllowlist(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
) []Diagnostic {
	if info == nil {
		return nil
	}

	// Check if this file is in the allowlist.
	relSlash := filepath.ToSlash(rel)
	for suffix := range aggregatorRegisterAllowlist {
		if strings.HasSuffix(relSlash, suffix) {
			return nil
		}
	}

	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isAggregatorRegisterCall(call, info) {
			return
		}
		pos := fset.Position(call.Pos())
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: pos.Line,
			Message: fmt.Sprintf(
				"PROBENAME-SEALED-FUNNEL-01/A3: direct healthz.Aggregator.Register call "+
					"at %s:%d is outside the sanctioned allowlist — "+
					"use reg.RegisterReadiness(name ProbeName, prober) instead; "+
					"Registrar.Healthz() has been removed (compile error if called); "+
					"direct Aggregator.Register is only allowed in bootstrap and celltest conformance",
				rel, pos.Line,
			),
		})
	})

	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// scanA4NewProbeNameCallerAllowlist scans file for production (non-test) calls
// to healthz.NewProbeName outside the sanctioned allowlist.
func scanA4NewProbeNameCallerAllowlist(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
) []Diagnostic {
	if info == nil {
		return nil
	}

	relSlash := filepath.ToSlash(rel)
	for suffix := range newProbeNameAllowlist {
		if strings.HasSuffix(relSlash, suffix) {
			return nil
		}
	}

	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isNewProbeNameCall(call, info) {
			return
		}
		pos := fset.Position(call.Pos())
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: pos.Line,
			Message: fmt.Sprintf(
				"PROBENAME-SEALED-FUNNEL-01/A4: healthz.NewProbeName called at %s:%d "+
					"from outside the sanctioned caller set "+
					"(only kernel/healthz/probename.go may call NewProbeName directly "+
					"in production code; tests use MustProbeName or err-check directly)",
				rel, pos.Line,
			),
		})
	})

	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// scanA4bMustProbeNameCallerAllowlist scans file for production (non-test) calls
// to healthz.MustProbeName outside the sanctioned allowlist.
//
// MustProbeName is the panic variant of NewProbeName. After fixing F1A/F1B, it
// must have zero production callers outside kernel/healthz/probename.go and the
// healthztest conformance file. All *_test.go callers are globally exempt.
//
// B4 blind-spot: a production caller passes a runtime string to MustProbeName
// (availability risk — panics if the adapter returns a non-snake_case key).
// A4b closes this by rejecting any MustProbeName call outside the allowlist
// regardless of the argument form, so even literal callers are locked out.
func scanA4bMustProbeNameCallerAllowlist(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
) []Diagnostic {
	if info == nil {
		return nil
	}

	relSlash := filepath.ToSlash(rel)
	for suffix := range mustProbeNameAllowlist {
		if strings.HasSuffix(relSlash, suffix) {
			return nil
		}
	}

	var out []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isMustProbeNameCall(call, info) {
			return
		}
		pos := fset.Position(call.Pos())
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: pos.Line,
			Message: fmt.Sprintf(
				"PROBENAME-SEALED-FUNNEL-01/A4b: healthz.MustProbeName called at %s:%d "+
					"from outside the sanctioned caller set "+
					"(only kernel/healthz/probename.go and healthztest/conformance.go may "+
					"call MustProbeName in production code; all test files are exempt)",
				rel, pos.Line,
			),
		})
	})

	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// scanA5EmitterFailOpenPrefixBypass scans file for bare BinaryExpr string
// concatenation of the form `"outbox_failopen_rate_" + x` outside
// kernel/healthz/probename.go — the sole sanctioned site.
func scanA5EmitterFailOpenPrefixBypass(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
) []Diagnostic {
	if info == nil {
		return nil
	}
	const probePrefixFile = "kernel/healthz/probename.go"
	if strings.HasSuffix(filepath.ToSlash(rel), probePrefixFile) {
		return nil
	}

	const emitterPrefix = "outbox_failopen_rate_"
	var out []Diagnostic

	EachInSubtree[ast.BinaryExpr](file, func(bin *ast.BinaryExpr) {
		// Catch `"outbox_failopen_rate_" + x`
		if s, ok := EvaluateConstString(info, bin.X); ok && strings.Contains(s, emitterPrefix) {
			pos := fset.Position(bin.Pos())
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"PROBENAME-SEALED-FUNNEL-01/A5: bare string concat of emitter probe prefix "+
						"%q at %s:%d — use healthz.EmitterFailOpenProbeName(cellID) instead",
					emitterPrefix, rel, pos.Line,
				),
			})
		}
		// Catch `x + "outbox_failopen_rate_"` (reversed — rare but defensive)
		if s, ok := EvaluateConstString(info, bin.Y); ok && strings.Contains(s, emitterPrefix) {
			pos := fset.Position(bin.Pos())
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"PROBENAME-SEALED-FUNNEL-01/A5: bare string concat of emitter probe prefix "+
						"%q at %s:%d — use healthz.EmitterFailOpenProbeName(cellID) instead",
					emitterPrefix, rel, pos.Line,
				),
			})
		}
	})

	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// ─── Golden inventory collector ───────────────────────────────────────────────

// collectProbeNameConsts collects all healthz.ProbeName typed const entries
// from the production tree and returns them sorted as
// "<module-relative-pkg>.<ConstName>=<value>".
func collectProbeNameConsts(t *testing.T) []string {
	t.Helper()

	var entries []string

	// Production (ModeWorkspace) — NOT Typed (ModeModule / GOWORK=off). The golden
	// inventory includes per-cell repo probes declared in the examples/* go.work
	// satellite modules (devicecell/ordercell/orderfulfillmentcell ProbeRepoReady);
	// a ModeModule load resolves only the root module and would silently drop them
	// (#1556). Production enumerates every go.work member via
	// LoadProductionPackages, so satellite probe consts stay covered.
	_ = Run(t, Production(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}

			modPrefix := probenameModPrefix
			pkgPath := p.Pkg.Path()
			if !strings.HasPrefix(pkgPath, modPrefix) {
				return nil
			}
			relPkg := strings.TrimPrefix(pkgPath, modPrefix)

			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.GenDecl](f, func(gd *ast.GenDecl) {
					EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
						for i, name := range vs.Names {
							obj, ok := p.TypesInfo.Defs[name]
							if !ok {
								continue
							}
							if !isProbeNameTypedConst(obj) {
								continue
							}
							if len(vs.Values) <= i {
								continue
							}
							val, ok := EvaluateConstString(p.TypesInfo, vs.Values[i])
							if !ok {
								continue
							}
							entries = append(entries, fmt.Sprintf("%s.%s=%s", relPkg, name.Name, val))
						}
					})
				})
			}
			return nil
		})

	sort.Strings(entries)
	return entries
}

// ─── Main production scan ─────────────────────────────────────────────────────

// TestProbenameSealedFunnel enforces PROBENAME-SEALED-FUNNEL-01 (A1–A5)
// across the production tree.
//
// Sub-tests:
//
//   - A1_DeclarationSanction — every ProbeName const in production code must be
//     declared in a sanctioned package; adapter packages must use _ready suffix;
//     cellgen packages must have the cellgen marker.
//   - A1_GoldenInventory — the full set of ProbeName consts must exactly match
//     goldenProbeNames(); additions or removals must be acknowledged in the
//     same PR by updating the golden list.
//   - A2_CallsiteResolves — every RegisterReadiness / NewProbe / HealthToProbe
//     name arg must resolve to a declared ProbeName const; ProbeName(callExpr)
//     casts are rejected.
//   - A3_AggregatorRegisterAllowlist — direct Aggregator.Register calls outside
//     the sanctioned allowlist are rejected.
//   - A4_NewProbeNameCallerAllowlist — healthz.NewProbeName must not be called
//     from production code outside kernel/healthz/probename.go.
//   - A5_EmitterFailOpenPrefixBypass — the "outbox_failopen_rate_" string prefix
//     must not appear in bare BinaryExpr concat outside probename.go.
//
// Note: A6 (projection prefix bypass scan) was removed in PR-03 round-3.
// The projection composed-name funnel is already Hard-closed by A2 (cast ban)
// + A4 (NewProbeName caller allowlist) + the two sole constructors
// ProjectionStoreReadyProbeName / ProjectionLagProbeName; A6 was a bypassable
// string-anchor scan (Soft per ai-robust.md) that added no additional closure.
func TestProbenameSealedFunnel(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var a1Diags, a2Diags, a3Diags, a4Diags, a4bDiags, a5Diags []Diagnostic

	_ = Run(t, Production(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			pkgPath := p.Pkg.Path()
			if !probenameIsInRepoPkg(pkgPath) {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				absPath := p.Abs(f)
				a1Diags = append(a1Diags, scanA1DeclarationSanction(p.Fset, f, rel, p.TypesInfo, p.Pkg, absPath)...)
				a2Diags = append(a2Diags, scanA2CallsiteResolves(p.Fset, f, rel, p.TypesInfo)...)
				a3Diags = append(a3Diags, scanA3AggregatorRegisterAllowlist(p.Fset, f, rel, p.TypesInfo)...)
				a4Diags = append(a4Diags, scanA4NewProbeNameCallerAllowlist(p.Fset, f, rel, p.TypesInfo)...)
				a4bDiags = append(a4bDiags, scanA4bMustProbeNameCallerAllowlist(p.Fset, f, rel, p.TypesInfo)...)
				a5Diags = append(a5Diags, scanA5EmitterFailOpenPrefixBypass(p.Fset, f, rel, p.TypesInfo)...)
			}
			return nil
		})

	t.Run("A1_DeclarationSanction", func(t *testing.T) {
		t.Parallel()
		Report(t, "PROBENAME-SEALED-FUNNEL-01/A1", a1Diags)
	})

	t.Run("A1_GoldenInventory", func(t *testing.T) {
		t.Parallel()
		got := collectProbeNameConsts(t)
		want := goldenProbeNames()
		assert.Equal(t, want, got,
			"PROBENAME-SEALED-FUNNEL-01/A1: ProbeName const inventory mismatch — "+
				"adding or removing a probe const requires updating goldenProbeNames() "+
				"in the same PR (see probename_sealed_funnel_test.go)")
	})

	t.Run("A2_CallsiteResolves", func(t *testing.T) {
		t.Parallel()
		Report(t, "PROBENAME-SEALED-FUNNEL-01/A2", a2Diags)
	})

	t.Run("A3_AggregatorRegisterAllowlist", func(t *testing.T) {
		t.Parallel()
		Report(t, "PROBENAME-SEALED-FUNNEL-01/A3", a3Diags)
	})

	t.Run("A4_NewProbeNameCallerAllowlist", func(t *testing.T) {
		t.Parallel()
		Report(t, "PROBENAME-SEALED-FUNNEL-01/A4", a4Diags)
	})

	t.Run("A4b_MustProbeNameCallerAllowlist", func(t *testing.T) {
		t.Parallel()
		Report(t, "PROBENAME-SEALED-FUNNEL-01/A4b", a4bDiags)
	})

	t.Run("A5_EmitterFailOpenPrefixBypass", func(t *testing.T) {
		t.Parallel()
		Report(t, "PROBENAME-SEALED-FUNNEL-01/A5", a5Diags)
	})
}

// ─── Reverse fixture self-tests ───────────────────────────────────────────────

// TestProbenameSealedFunnel_ReverseFixtures loads the synthetic RED fixtures and
// asserts each rule fires on its corresponding violation.
func TestProbenameSealedFunnel_ReverseFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	fixtureDir := filepath.Join(root, "tools", "archtest", "testdata", "probename_sealed_funnel_fixtures")

	type subDir struct {
		name     string
		want     string // which Ax/Bx rule must fire
		contains string // optional substring required in diag.Message (branch assertion)
	}
	cases := []subDir{
		{name: "bare_literal_arg_red", want: "A2"},
		{name: "phases_with_health_checker_red", want: "A2", contains: "WithHealthChecker name arg"},
		{name: "decl_bypass_red", want: "A1", contains: "non-sanctioned package"},
		{name: "invalid_value_red", want: "A1", contains: "fails healthz.NewProbeName validator"},
		{name: "value_shape_multi_const_red", want: "A1", contains: "fails healthz.NewProbeName validator"},
		{name: "value_shape_uppercase_red", want: "A1", contains: "fails healthz.NewProbeName validator"},
		{name: "value_shape_double_underscore_red", want: "A1", contains: "fails healthz.NewProbeName validator"},
		{name: "non_funnel_register_red", want: "A3"},
		{name: "newprobename_dynamic_red", want: "A4"},
		{name: "string_cast_bypass_red", want: "B3"},
		{name: "reflect_bypass_red", want: "B1"},
		{name: "reflect_bypass_const_red", want: "B1"},
		// projection_prefix_bypass_red (A6) removed: A6 was a bypassable
		// string-anchor scan (Soft). The projection composed-name funnel is
		// already Hard-closed by A2+A4+sole constructors; see TestProbenameSealedFunnel.
		// helper_wrapper_red (B2) is a *non-bypass*: the type system already
		// enforces healthz.ProbeName at the wrapper's signature, so a wrapper
		// inheriting the typed signature provides no escape from the typed
		// funnel. The plan's B2 envisioned wrappers that hide name from
		// cell-side callers, but with ProbeName as the funnel parameter that
		// cannot happen (a wrapper that takes a bare string would not type-
		// check). Keep the fixture for documentation purposes.
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			subFixtureDir := filepath.Join(fixtureDir, tc.name)

			var a1, a2, a3, a4, b1b2, b3 []Diagnostic

			_ = Run(t, StandaloneModule(subFixtureDir, TypedOpts{Tests: false}, []string{"./..."}),
				func(p *Pass) []Diagnostic {
					if p.Pkg == nil {
						return nil
					}
					for _, f := range p.Files {
						rel := p.Rel(f)
						if strings.HasSuffix(rel, "_test.go") {
							continue
						}
						absPath := p.Abs(f)
						a1 = append(a1, scanA1DeclarationSanction(p.Fset, f, rel, p.TypesInfo, p.Pkg, absPath)...)
						a2 = append(a2, scanA2CallsiteResolves(p.Fset, f, rel, p.TypesInfo)...)
						a3 = append(a3, scanA3AggregatorRegisterAllowlist(p.Fset, f, rel, p.TypesInfo)...)
						a4 = append(a4, scanA4NewProbeNameCallerAllowlist(p.Fset, f, rel, p.TypesInfo)...)

						b3 = append(b3, scanA2CallsiteResolves(p.Fset, f, rel, p.TypesInfo)...)

						EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
							sel, ok := call.Fun.(*ast.SelectorExpr)
							if !ok || sel.Sel.Name != "MethodByName" {
								return
							}
							if len(call.Args) != 1 {
								return
							}
							val, ok := EvaluateConstString(p.TypesInfo, call.Args[0])
							if !ok {
								return
							}
							if strings.Contains(val, "RegisterReadiness") {
								pos := p.Fset.Position(call.Pos())
								b1b2 = append(b1b2, Diagnostic{
									Rel:  rel,
									Line: pos.Line,
									Message: fmt.Sprintf(
										"PROBENAME-SEALED-FUNNEL-01/B1: reflect bypass of "+
											"RegisterReadiness at %s:%d",
										rel, pos.Line,
									),
								})
							}
						})
					}
					return nil
				})

			var bucket []Diagnostic
			switch tc.want {
			case "A1":
				require.NotEmpty(t, a1, "fixture %q: expected A1 to fire on declaration bypass", tc.name)
				bucket = a1
			case "A2":
				require.NotEmpty(t, a2, "fixture %q: expected A2 to fire on non-const name arg", tc.name)
				bucket = a2
			case "A3":
				require.NotEmpty(t, a3, "fixture %q: expected A3 to fire on direct Aggregator.Register", tc.name)
				bucket = a3
			case "A4":
				require.NotEmpty(t, a4, "fixture %q: expected A4 to fire on NewProbeName outside allowlist", tc.name)
				bucket = a4
			case "B1":
				require.NotEmpty(t, b1b2, "fixture %q: expected B1 reflect bypass to be detected", tc.name)
				bucket = b1b2
			case "B2":
				require.NotEmpty(t, b1b2, "fixture %q: expected B2 helper-wrapper to be detected via A3 or b1b2", tc.name)
				bucket = b1b2
			case "B3":
				require.NotEmpty(t, b3, "fixture %q: expected B3 string-cast bypass to be detected by A2", tc.name)
				bucket = b3
			}

			// Branch assertion: when fixture targets a specific sub-branch
			// (e.g. A1 value-shape vs package-sanction, or A2 WithHealthChecker
			// vs RegisterReadiness), require an explicit substring match on
			// the diag message to prevent accidental matches via the wrong
			// sub-rule. Without this, a fixture meant to verify "A2 fires on
			// WithHealthChecker bare literal" could pass by accidentally
			// firing A2 RegisterReadiness logic instead, masking a real gap.
			if tc.contains != "" {
				matched := false
				for _, d := range bucket {
					if strings.Contains(d.Message, tc.contains) {
						matched = true
						break
					}
				}
				if !matched {
					t.Fatalf("fixture %q (want=%s): expected diag containing %q, got %d diags: %#v",
						tc.name, tc.want, tc.contains, len(bucket), bucket)
				}
			}
		})
	}
}

// ─── Blind-spot reverse self-check tests ──────────────────────────────────────

// TestProbenameSealedFunnel_ReverseBlindSpot_NoReflectBypass (B1) asserts no
// production file calls reflect.MethodByName("RegisterReadiness") or equivalent
// reflect-based bypass patterns.
func TestProbenameSealedFunnel_ReverseBlindSpot_NoReflectBypass(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	var diags []Diagnostic

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		prodscan.PatternsExtended(root)),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "MethodByName" {
						return
					}
					if len(call.Args) != 1 {
						return
					}

					val, ok := EvaluateConstString(p.TypesInfo, call.Args[0])
					if !ok {
						return
					}
					if strings.Contains(val, "RegisterReadiness") {
						pos := p.Fset.Position(call.Pos())
						diags = append(diags, Diagnostic{
							Rel:  rel,
							Line: pos.Line,
							Message: fmt.Sprintf(
								"PROBENAME-SEALED-FUNNEL-01/B1: blind spot detected — "+
									"reflect bypass of RegisterReadiness at %s:%d",
								rel, pos.Line,
							),
						})
					}
				})
			}
			return nil
		})

	assert.Empty(t, diags, "PROBENAME-SEALED-FUNNEL-01/B1 blind-spot self-check failed — "+
		"production code uses reflect to bypass RegisterReadiness funnel")
}

// TestProbenameSealedFunnel_ReverseBlindSpot_NoStringCastBypass (B3) asserts
// no production file contains a ProbeName(callExpr) type conversion where the
// argument is a dynamic function call (e.g. fmt.Sprintf, strings.Join).
func TestProbenameSealedFunnel_ReverseBlindSpot_NoStringCastBypass(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	var diags []Diagnostic

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		prodscan.PatternsExtended(root)),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					inner, isCast := isProbeNameTypeConversion(call, p.TypesInfo)
					if !isCast {
						return
					}
					if _, isCallArg := inner.(*ast.CallExpr); isCallArg {
						pos := p.Fset.Position(call.Pos())
						diags = append(diags, Diagnostic{
							Rel:  rel,
							Line: pos.Line,
							Message: fmt.Sprintf(
								"PROBENAME-SEALED-FUNNEL-01/B3: ProbeName(callExpr) string-cast bypass "+
									"at %s:%d — use healthz.NewProbeName or a sanctioned constructor",
								rel, pos.Line,
							),
						})
					}
				})
			}
			return nil
		})

	assert.Empty(t, diags, "PROBENAME-SEALED-FUNNEL-01/B3 blind-spot self-check failed — "+
		"production code bypasses ProbeName funnel via string-cast of dynamic expression")
}

// TestProbenameSealedFunnel_ReverseBlindSpot_NoHelperWrapper (B2) asserts no
// production file outside the sanctioned set defines a helper function that
// wraps reg.RegisterReadiness to create an indirection layer that could
// bypass the A2 callsite check.
//
// Sanctioned wrapper files: kernel/cell/healthz.go (RegisterEmitterHealthProbes)
// and cells/<cell>/healthz_gen.go (cellgen-generated RegisterReadiness funnel).
func TestProbenameSealedFunnel_ReverseBlindSpot_NoHelperWrapper(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// Sanctioned files that are allowed to define a RegisterReadiness wrapper.
	sanctionedWrapperSuffixes := []string{
		"kernel/cell/healthz.go",
	}
	// cellgen healthz_gen.go files are also allowed — detected by cellgen marker.
	isSanctionedWrapper := func(rel, absPath string) bool {
		relSlash := filepath.ToSlash(rel)
		for _, s := range sanctionedWrapperSuffixes {
			if strings.HasSuffix(relSlash, s) {
				return true
			}
		}
		if strings.HasSuffix(relSlash, "healthz_gen.go") && fileHasCellgenMarker(absPath) {
			return true
		}
		return false
	}

	root := findModuleRoot(t)
	var diags []Diagnostic

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		prodscan.PatternsExtended(root)),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				absPath := p.Abs(f)
				if isSanctionedWrapper(rel, absPath) {
					continue
				}

				EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
					if fd.Body == nil {
						return
					}
					EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
						if !isRegisterReadinessCall(call, p.TypesInfo) {
							return
						}
						pos := p.Fset.Position(fd.Pos())
						diags = append(diags, Diagnostic{
							Rel:  rel,
							Line: pos.Line,
							Message: fmt.Sprintf(
								"PROBENAME-SEALED-FUNNEL-01/B2: function %q at %s:%d wraps "+
									"reg.RegisterReadiness but is not in the sanctioned "+
									"wrapper set — helper indirection can bypass A2 scanning",
								fd.Name.Name, rel, pos.Line,
							),
						})
					})
				})
			}
			return nil
		})

	assert.Empty(t, diags, "PROBENAME-SEALED-FUNNEL-01/B2 blind-spot self-check failed — "+
		"unsanctioned RegisterReadiness wrapper found in production code")
}

// TestProbenameSealedFunnel_ReverseBlindSpot_NoNewProbeImpl (B5) asserts no
// new struct inside kernel/healthz package implements isHealthzProbe() beyond
// the two sanctioned types (funcProbe, ctxSafeProbe). The Probe sealed marker
// is a type-system Hard gate against external packages but not against
// new in-package implementations — this reverse self-check closes that gap.
func TestProbenameSealedFunnel_ReverseBlindSpot_NoNewProbeImpl(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	sanctioned := map[string]bool{"funcProbe": true, "ctxSafeProbe": true}
	var found []string

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{healthzPkgPath + "/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != healthzPkgPath {
				return nil
			}

			for _, f := range p.Files {
				EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
					if fd.Recv == nil || len(fd.Recv.List) != 1 {
						return
					}
					if fd.Name.Name != "isHealthzProbe" {
						return
					}
					recvType := fd.Recv.List[0].Type
					if star, ok := recvType.(*ast.StarExpr); ok {
						recvType = star.X
					}
					ident, ok := recvType.(*ast.Ident)
					if !ok {
						return
					}
					if !sanctioned[ident.Name] {
						found = append(found, ident.Name)
					}
				})
			}
			return nil
		})

	assert.Empty(t, found, "PROBENAME-SEALED-FUNNEL-01/B5 blind-spot detected — "+
		"new type(s) implementing isHealthzProbe() inside kernel/healthz beyond "+
		"{funcProbe, ctxSafeProbe}: %v", found)
}
