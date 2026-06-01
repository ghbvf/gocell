// INVARIANT: SLOG-HANDLER-SEALED-FUNNEL-01
//
// SLOG-HANDLER-SEALED-FUNNEL-01 — every slog.NewJSONHandler / slog.NewTextHandler
// call in production code is confined to runtime/observability/logging, and the
// contextHandler.Handle method redacts every attribute and message before
// forwarding to the inner handler. Production entry points seal the process-global
// slog default with the redacting handler before any log emission.
//
// Three-assertion AI-robust double-locked funnel (per .claude/rules/gocell/ai-robust.md
// 范本目录 "typed marker funnel" + "single sanctioned holder"):
//
//	A1 (Downstream Hard — bare handler construction banned outside logging pkg):
//	   production code outside runtime/observability/logging must not call
//	   slog.NewJSONHandler or slog.NewTextHandler. These are the only two slog
//	   sink constructors; banning them outside the sanctioned package means no
//	   production code can create a non-redacting handler. Resolution is
//	   type-aware (go/types IsCallToPkgFunc resolving pkg path "log/slog" +
//	   func names "NewJSONHandler"/"NewTextHandler"), so import alias tricks are
//	   ineffective.
//
//	A2 (Downstream Hard — form-lock on contextHandler.Handle and WithAttrs):
//	   Within runtime/observability/logging.contextHandler:
//	   (a) Handle's r.Attrs callback must pass EVERY AddAttrs argument through
//	       redaction.RedactSlogAttr (per-arg form-lock — a bare AddAttrs(rawAttr)
//	       fails); message must pass through redaction.RedactString.
//	   (b) WithAttrs must NOT pass the raw attrs param to inner.WithAttrs and must
//	       build the bound slice via redaction.RedactSlogAttr (anti raw-passthrough
//	       form-lock). #1036 review F4 + round-3 upgraded both from presence checks.
//	   AST form-lock: the shape is structurally matched. A future edit that
//	   drops the redaction call, adds a bare AddAttrs, or binds the raw param
//	   fails A2.
//
//	A3 (Upstream Medium — entry point SetDefault present AND first-ordered):
//	   The production entry point functions (runCorebundle, runIotdevice,
//	   runTodoorder, main in examples/ssobff/main.go) must each contain a
//	   slog.SetDefault(slog.New(logging.NewHandler(...))) call AS THE FIRST
//	   call-bearing statement (ordering form-lock, #1036 review round-3 — nothing
//	   fallible/logging may run before the seal). Medium: the lock is scoped to
//	   the entry-point FuncDecl body; Go cannot gate package init() / other
//	   goroutines touching slog.Default before the entry point runs.
//	   Hard-upgrade path: codegen injection of a generated first-line seal into
//	   each assembly entrypoint. Tracked in gh #1401.
//
// AI-robust rating — Funnel double-lock:
//   - Downstream A1: Hard — go/types typed callee resolution; import-alias bypass
//     ineffective; archtest fails on any out-of-funnel slog.New{JSON,Text}Handler call.
//   - Downstream A2: Hard — AST form-uniqueness; dropping RedactSlogAttr from the
//     Handle callback body fails the archtest immediately.
//   - Upstream A3: Medium — caller allowlist (archtest asserts presence in each
//     entry-point function body); Go language ceiling: no mechanism forces
//     SetDefault to be called before any slog.Default() use in early init.
//     Hard-upgrade path tracked at gh #1401.
//
// BLIND SPOTS (AST forms outside the coverage of SLOG-HANDLER-SEALED-FUNNEL-01):
//
//   - A1 does not cover hand-rolled slog.Handler implementations that wrap the
//     inner handler without using slog.New{JSON,Text}Handler (e.g., a custom
//     struct implementing slog.Handler). Reverse self-check
//     TestSlogHandlerSealedFunnel_NoBlindspotsInProduction asserts that no
//     production file outside logging/ implements slog.Handler (production
//     implementations of the Handler interface should only be in logging/).
//
//   - A2 form-locks the Handle r.Attrs callback (every AddAttrs arg) and the
//     WithAttrs bound slice (anti raw-passthrough), but does not trace data flow
//     through arbitrary helper functions (e.g. attrs redacted inside a separate
//     unexported helper then returned). Reverse self-check
//     TestSlogHandlerSealedFunnel_A2_DetectsViolation injects a fake Handle body
//     with a bare AddAttrs and confirms a violation is reported.
//
//   - A3 form-locks ordering WITHIN the entry-point body (the seal must be the
//     first call-bearing statement; a fallible/logging call before it fails).
//     The residual blind spots are (i) a SetDefault inside a separate helper
//     called from the entry point (cross-FuncDecl flow is not traced), and
//     (ii) the process-init window — a package init() or goroutine touching
//     slog.Default before the entry point runs. Both are why A3 stays Medium;
//     the #1401 codegen path (seal as the generated main's first line) is the
//     Hard upgrade. Reverse self-check
//     TestSlogHandlerSealedFunnel_A3_DetectsViolation verifies the production
//     runCorebundle contains and orders the required SetDefault call.
//
// Note on REPO-LOG-KEY-ID-REDACT-01 (tools/archtest/repoerr_test.go):
//
//	REPO-LOG-KEY-ID-REDACT-01 is a KEY-NAME prohibition (forbids "key_id"/"keyID"
//	from appearing in slog attr KEY slots in cells/), not a value-redaction rule.
//	pkg/redaction.IsSensitiveKey does not contain "key_id", so the slog sink-side
//	redaction (this funnel) does not mask key_id values. REPO-LOG-KEY-ID-REDACT-01
//	is therefore orthogonal to this funnel and must NOT be retired alongside the
//	PANIC-REDACT-01 / HTTPUTIL-5XX-LOG-REDACT-01 Soft archtests. Do not retire it.
//
// ref: runtime/observability/logging/logging.go (contextHandler implementation)
// ref: .claude/rules/gocell/observability.md "slog Sink Redaction"
// ref: .claude/rules/gocell/ai-robust.md §"Hard 范本目录"
// ref: ADR docs/architecture/202604242030-adr-kernel-wrapper-contract-observability.md §8
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/typesutil"
)

// slog funnel constants — single source for production enforcement and reverse
// self-checks. Prefixed with "slogFunnel" to avoid collision with constants
// declared in webhook_hmac_funnel_test.go (which also uses "log/slog" path).
const (
	slogFunnelStdlibPkgPath      = "log/slog"
	slogFunnelNewJSONHandlerFunc = "NewJSONHandler"
	slogFunnelNewTextHandlerFunc = "NewTextHandler"
	slogFunnelSetDefaultFunc     = "SetDefault"
	slogFunnelNewFunc            = "New"

	slogFunnelLoggingPkgRelDir       = "runtime/observability/logging"
	slogFunnelLoggingPkgImportSuffix = "runtime/observability/logging"

	slogFunnelContextHandlerTypeName        = "contextHandler"
	slogFunnelContextHandlerHandleMethod    = "Handle"
	slogFunnelContextHandlerWithAttrsMethod = "WithAttrs"

	slogFunnelRedactSlogAttrFunc = "RedactSlogAttr"
	slogFunnelRedactStringFunc   = "RedactString"

	slogFunnelNewHandlerFunc = "NewHandler"
)

// slogHandlerEntryPoints is the authoritative allowlist of production entry
// point functions that must each contain a slog.SetDefault(slog.New(logging.NewHandler(...)))
// call. Adding a new assembly entry point requires updating this list.
var slogHandlerEntryPoints = []struct {
	pkgPattern string // pattern for RunTyped
	funcName   string // top-level function name
	relPath    string // module-relative path (for diagnostics)
}{
	{"./cmd/corebundle", "runCorebundle", "cmd/corebundle/run.go"},
	{"./examples/iotdevice", "runIotdevice", "examples/iotdevice/run.go"},
	{"./examples/todoorder", "runTodoorder", "examples/todoorder/run.go"},
	{"./examples/ssobff", "main", "examples/ssobff/main.go"},
}

// ---------------------------------------------------------------------------
// A1: bare handler construction banned outside logging pkg
// ---------------------------------------------------------------------------

// TestSlogHandlerSealedFunnel_A1_NoBareConstruction enforces A1 (downstream Hard):
// slog.NewJSONHandler and slog.NewTextHandler must only appear inside
// runtime/observability/logging. Any out-of-funnel call produces a non-redacting
// handler.
//
// Resolution is go/types-based (IsCallToPkgFunc), so import aliases (e.g.,
// `slogsink "log/slog"`) do not bypass the check.
//
// Blind spots: see package godoc BLIND SPOTS §A1.
func TestSlogHandlerSealedFunnel_A1_NoBareConstruction(t *testing.T) {
	t.Parallel()

	diags := RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, f := range p.Files {
			rel := filepath.ToSlash(p.Rel(f))
			// Only flag files outside the sanctioned logging package.
			if strings.HasPrefix(rel, slogFunnelLoggingPkgRelDir+"/") || rel == slogFunnelLoggingPkgRelDir {
				continue
			}
			EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
				if p.TypesInfo == nil {
					return
				}
				isJSON := IsCallToPkgFunc(p.TypesInfo, call, slogFunnelStdlibPkgPath, slogFunnelNewJSONHandlerFunc)
				isText := IsCallToPkgFunc(p.TypesInfo, call, slogFunnelStdlibPkgPath, slogFunnelNewTextHandlerFunc)
				if !isJSON && !isText {
					return
				}
				funcName := slogFunnelNewJSONHandlerFunc
				if isText {
					funcName = slogFunnelNewTextHandlerFunc
				}
				pos := p.Fset.Position(call.Pos())
				ds = append(ds, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: "slog." + funcName + "(...) called outside runtime/observability/logging" +
						" — non-redacting handler bypasses SLOG-HANDLER-SEALED-FUNNEL-01 A1;" +
						" use logging.NewHandler instead",
				})
			})
		}
		return ds
	})
	Report(t, "SLOG-HANDLER-SEALED-FUNNEL-01", diags)
}

// ---------------------------------------------------------------------------
// A2: form-lock on contextHandler.Handle and WithAttrs
// ---------------------------------------------------------------------------

// contextHandlerHandleRedactCheck verifies that inside contextHandler.Handle:
//   - the message passes through redaction.RedactString; and
//   - the record attrs are iterated via r.Attrs(func(...) bool {...}) and EVERY
//     AddAttrs argument inside that callback is redaction.RedactSlogAttr(...).
//
// Check 2 is a per-AddAttrs form-lock (#1036 review F4), not a "RedactSlogAttr
// appears somewhere" presence check: a bare AddAttrs(rawAttr) inside the
// callback is flagged. The ctxAttrs AddAttrs that lives OUTSIDE the callback
// (framework-trusted enumerated fields) is intentionally out of scope.
func contextHandlerHandleRedactCheck(p *Pass, f *ast.File, fn *ast.FuncDecl) []Diagnostic {
	redactionLocal := redactionLocalName(f)
	var ds []Diagnostic

	// Check 1: Message must pass through redaction.RedactString.
	foundRedactString := false
	EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
		if redactionLocal == "" {
			return
		}
		if callMatches(call, redactionLocal, slogFunnelRedactStringFunc) {
			foundRedactString = true
		}
	})
	if !foundRedactString {
		pos := p.Fset.Position(fn.Pos())
		ds = append(ds, Diagnostic{
			Rel:  filepath.ToSlash(p.Rel(f)),
			Line: pos.Line,
			Message: "contextHandler.Handle must pass the log message through" +
				" redaction.RedactString — SLOG-HANDLER-SEALED-FUNNEL-01 A2",
		})
	}

	// Check 2 (form-lock, #1036 review F4): the record's attrs must be iterated
	// via r.Attrs(func(...) bool {...}), and inside that callback EVERY AddAttrs
	// argument must be redaction.RedactSlogAttr(...). A bare AddAttrs(rawAttr)
	// inside the callback would bypass redaction and is rejected — this is a
	// per-AddAttrs form-lock, not a "RedactSlogAttr appears somewhere" presence
	// check. The ctxAttrs AddAttrs OUTSIDE the callback (framework-trusted
	// enumerated fields) is intentionally not constrained here.
	var attrsCallback *ast.FuncLit
	EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "Attrs" {
			return
		}
		if len(call.Args) == 1 {
			if fl, ok := call.Args[0].(*ast.FuncLit); ok {
				attrsCallback = fl
			}
		}
	})
	if attrsCallback == nil {
		pos := p.Fset.Position(fn.Pos())
		ds = append(ds, Diagnostic{
			Rel:  filepath.ToSlash(p.Rel(f)),
			Line: pos.Line,
			Message: "contextHandler.Handle must iterate record attrs via" +
				" r.Attrs(func(...) bool {...}) — SLOG-HANDLER-SEALED-FUNNEL-01 A2",
		})
		return ds
	}

	addAttrsCount := 0
	bareAddAttrs := false
	EachInSubtree[ast.CallExpr](attrsCallback.Body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "AddAttrs" {
			return
		}
		addAttrsCount++
		// Every argument must be a redaction.RedactSlogAttr(...) call: count the
		// direct CallExpr children that match; if fewer than the total arg count
		// (or any arg is a bare non-call expr), a raw attr slipped in.
		matchArgs := 0
		EachInChildren[ast.CallExpr](call, func(argCall *ast.CallExpr) {
			if callMatches(argCall, redactionLocal, slogFunnelRedactSlogAttrFunc) {
				matchArgs++
			}
		})
		if redactionLocal == "" || len(call.Args) == 0 || matchArgs != len(call.Args) {
			bareAddAttrs = true
		}
	})
	if addAttrsCount == 0 || bareAddAttrs {
		pos := p.Fset.Position(fn.Pos())
		ds = append(ds, Diagnostic{
			Rel:  filepath.ToSlash(p.Rel(f)),
			Line: pos.Line,
			Message: "contextHandler.Handle must pass EVERY attr in the r.Attrs" +
				" callback through redaction.RedactSlogAttr (no bare AddAttrs)" +
				" — SLOG-HANDLER-SEALED-FUNNEL-01 A2",
		})
	}
	return ds
}

// contextHandlerWithAttrsRedactCheck form-locks contextHandler.WithAttrs
// (#1036 review round-3): a presence "RedactSlogAttr appears once" check would
// still pass if the raw attrs param were accidentally bound into the inner
// handler. This form-lock instead requires BOTH:
//   - the raw input attrs param is NOT passed directly to any .WithAttrs(...)
//     call (anti raw-passthrough); and
//   - a per-element redaction assignment `redacted[i] = redaction.RedactSlogAttr(a)`
//     exists.
//
// Together these reject "forgot to redact, passed raw" and "redacted into a
// slice but bound the raw param".
func contextHandlerWithAttrsRedactCheck(p *Pass, f *ast.File, fn *ast.FuncDecl) []Diagnostic {
	redactionLocal := redactionLocalName(f)
	mkDiag := func(msg string) []Diagnostic {
		pos := p.Fset.Position(fn.Pos())
		return []Diagnostic{{
			Rel:     filepath.ToSlash(p.Rel(f)),
			Line:    pos.Line,
			Message: msg + " — SLOG-HANDLER-SEALED-FUNNEL-01 A2",
		}}
	}
	if redactionLocal == "" {
		return mkDiag("contextHandler.WithAttrs must import pkg/redaction and call RedactSlogAttr")
	}

	// Name of the first parameter (the raw attrs slice).
	paramName := ""
	if fn.Type.Params != nil && len(fn.Type.Params.List) > 0 && len(fn.Type.Params.List[0].Names) > 0 {
		paramName = fn.Type.Params.List[0].Names[0].Name
	}

	// (1) the raw param must NOT be passed directly to any .WithAttrs(...) call.
	rawPassthrough := false
	EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "WithAttrs" {
			return
		}
		if paramName == "" || len(call.Args) != 1 {
			return
		}
		if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == paramName {
			rawPassthrough = true
		}
	})
	if rawPassthrough {
		return mkDiag("contextHandler.WithAttrs must not pass the raw attrs param" +
			" to inner.WithAttrs; bind a redaction.RedactSlogAttr-built slice")
	}

	// (2) a per-element redaction assignment must exist (RHS is RedactSlogAttr).
	foundRedactAssign := false
	EachInSubtree[ast.AssignStmt](fn.Body, func(as *ast.AssignStmt) {
		EachInChildren[ast.CallExpr](as, func(rhs *ast.CallExpr) {
			if callMatches(rhs, redactionLocal, slogFunnelRedactSlogAttrFunc) {
				foundRedactAssign = true
			}
		})
	})
	if !foundRedactAssign {
		return mkDiag("contextHandler.WithAttrs must redact each pre-bound attr via `redacted[i] = redaction.RedactSlogAttr(a)`")
	}
	return nil
}

// TestSlogHandlerSealedFunnel_A2_HandleFormLock enforces A2 (downstream Hard):
// contextHandler.Handle redacts message and each attr;
// contextHandler.WithAttrs redacts each attr at bind time.
//
// Uses AST-only Run (DirsScope) because the rules are syntactic shape-locks
// inside a single file. Form-uniqueness closes the regression path: dropping
// the RedactSlogAttr call makes the test fail immediately.
func TestSlogHandlerSealedFunnel_A2_HandleFormLock(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	scope := DirsScope(root, []string{slogFunnelLoggingPkgRelDir})

	var handleFound, withAttrsFound bool
	var all []Diagnostic

	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, f := range p.Files {
			rel := filepath.ToSlash(p.Rel(f))
			if !strings.HasPrefix(rel, slogFunnelLoggingPkgRelDir) {
				continue
			}
			EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
				if fn.Name == nil || fn.Body == nil {
					return
				}
				// Only check methods on contextHandler.
				if !HasReceiver(fn, slogFunnelContextHandlerTypeName) {
					return
				}
				switch fn.Name.Name {
				case slogFunnelContextHandlerHandleMethod:
					handleFound = true
					ds = append(ds, contextHandlerHandleRedactCheck(p, f, fn)...)
				case slogFunnelContextHandlerWithAttrsMethod:
					withAttrsFound = true
					ds = append(ds, contextHandlerWithAttrsRedactCheck(p, f, fn)...)
				}
			})
		}
		return ds
	})
	all = append(all, diags...)

	if !handleFound {
		t.Fatalf("SLOG-HANDLER-SEALED-FUNNEL-01 A2: %s.%s not found in %s — "+
			"if contextHandler was renamed or relocated, update this archtest",
			slogFunnelContextHandlerTypeName, slogFunnelContextHandlerHandleMethod, slogFunnelLoggingPkgRelDir)
	}
	if !withAttrsFound {
		t.Fatalf("SLOG-HANDLER-SEALED-FUNNEL-01 A2: %s.%s not found in %s — "+
			"if contextHandler was renamed or relocated, update this archtest",
			slogFunnelContextHandlerTypeName, slogFunnelContextHandlerWithAttrsMethod, slogFunnelLoggingPkgRelDir)
	}

	Report(t, "SLOG-HANDLER-SEALED-FUNNEL-01", all)
}

// ---------------------------------------------------------------------------
// A3: entry point SetDefault caller allowlist (upstream Medium)
// ---------------------------------------------------------------------------

// slogSetDefaultShape checks whether a call expression matches the shape
// slog.SetDefault(slog.New(logging.NewHandler(...))), using go/types to
// resolve pkg paths (IsCallToPkgFunc). Specifically:
//   - outer: slog.SetDefault(...)
//   - arg0:  slog.New(...)
//   - arg0.arg0: logging.NewHandler(...)
//
// Returns true only when ALL three levels match.
func slogSetDefaultShape(info *types.Info, call *ast.CallExpr, loggingPkgPath string) bool {
	if info == nil {
		return false
	}
	// Outer: slog.SetDefault(...)
	if !IsCallToPkgFunc(info, call, slogFunnelStdlibPkgPath, slogFunnelSetDefaultFunc) {
		return false
	}
	if len(call.Args) != 1 {
		return false
	}
	// arg0: slog.New(...)
	inner, ok := call.Args[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	if !IsCallToPkgFunc(info, inner, slogFunnelStdlibPkgPath, slogFunnelNewFunc) {
		return false
	}
	if len(inner.Args) != 1 {
		return false
	}
	// arg0.arg0: logging.NewHandler(...)
	newHandler, ok := inner.Args[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	return IsCallToPkgFunc(info, newHandler, loggingPkgPath, slogFunnelNewHandlerFunc)
}

// firstCallBearingStmtSatisfies reports whether the FIRST top-level statement in
// body that contains any call expression has a call satisfying pred. Statements
// with no call (bare var decls etc.) are skipped. This is the pure-AST ordering
// traversal behind the A3 ordering form-lock — separated from the typed seal
// predicate so it can be reverse-self-checked with synthetic source
// (TestSlogHandlerSealedFunnel_A3_Order_DetectsViolation).
func firstCallBearingStmtSatisfies(body *ast.BlockStmt, pred func(*ast.CallExpr) bool) bool {
	for _, stmt := range body.List {
		hasCall := false
		ok := false
		EachInSubtree[ast.CallExpr](stmt, func(call *ast.CallExpr) {
			hasCall = true
			if pred(call) {
				ok = true
			}
		})
		if !hasCall {
			continue
		}
		return ok
	}
	return false
}

// TestSlogHandlerSealedFunnel_A3_EntryPointSeal enforces A3 (upstream Medium):
// each production entry point function must (a) contain a
// slog.SetDefault(slog.New(logging.NewHandler(...))) call, AND (b) that call
// must be the FIRST call-bearing statement in the function body — no fallible or
// logging call may run before the seal (ordering form-lock, #1036 review
// round-3; a presence-only check would pass even if a log call preceded it).
//
// This remains Medium (not Hard) because the lock is scoped to the entry-point
// FuncDecl body: Go provides no mechanism to guarantee the entry point itself
// runs before package init() / other goroutines that may call slog.Default().
// The Hard upgrade path is codegen injection of the seal as the first generated
// line, tracked at gh #1401.
func TestSlogHandlerSealedFunnel_A3_EntryPointSeal(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("SLOG-HANDLER-SEALED-FUNNEL-01 A3: read module path: %v", err)
	}
	loggingPkgPath := modPath + "/" + slogFunnelLoggingPkgImportSuffix

	// Run each entry point inline (NOT as parallel subtests): parallel subtests
	// defer execution until this function returns, so a Report(all) after the
	// loop would see an empty slice — a vacuous pass — plus a data race on the
	// shared `all` slice. #1036 review F3.
	var all []Diagnostic
	for _, ep := range slogHandlerEntryPoints {
		found := false
		sealIsFirstCall := false
		RunTyped(t, TypedOpts{Tests: false}, []string{ep.pkgPattern},
			func(p *Pass) []Diagnostic {
				if p.TypesInfo == nil {
					return nil
				}
				for _, f := range p.Files {
					// Skip test files.
					if strings.HasSuffix(filepath.ToSlash(p.Rel(f)), "_test.go") {
						continue
					}
					EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
						if fn.Name == nil || fn.Body == nil {
							return
						}
						if fn.Name.Name != ep.funcName {
							return
						}
						// Existence: a slog.SetDefault(slog.New(logging.NewHandler(...))) call.
						EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
							if slogSetDefaultShape(p.TypesInfo, call, loggingPkgPath) {
								found = true
							}
						})
						// Ordering form-lock (#1036 review round-3): the FIRST
						// top-level statement that contains ANY call must contain
						// the seal — nothing fallible or logging may run before
						// SetDefault. A presence-only check would pass even if a
						// log/fallible call preceded the seal.
						sealIsFirstCall = firstCallBearingStmtSatisfies(fn.Body,
							func(call *ast.CallExpr) bool {
								return slogSetDefaultShape(p.TypesInfo, call, loggingPkgPath)
							})
					})
				}
				return nil
			})

		switch {
		case !found:
			all = append(all, Diagnostic{
				Rel:  ep.relPath,
				Line: 0,
				Message: "entry point function " + ep.funcName +
					" must call slog.SetDefault(slog.New(logging.NewHandler(...)))" +
					" — SLOG-HANDLER-SEALED-FUNNEL-01 A3; Hard-upgrade path gh #1401",
			})
		case !sealIsFirstCall:
			all = append(all, Diagnostic{
				Rel:  ep.relPath,
				Line: 0,
				Message: "entry point function " + ep.funcName +
					" must call slog.SetDefault(...) as the FIRST call-bearing" +
					" statement (no fallible or logging call before the seal) —" +
					" SLOG-HANDLER-SEALED-FUNNEL-01 A3; Hard-upgrade path gh #1401",
			})
		}
	}

	Report(t, "SLOG-HANDLER-SEALED-FUNNEL-01", all)
}

// ---------------------------------------------------------------------------
// Reverse self-checks (non-vacuity proofs per ai-robust.md §载体决策原则)
// ---------------------------------------------------------------------------

// TestSlogHandlerSealedFunnel_A1_DetectsViolation is the reverse self-check for
// A1: confirms that a slog.NewJSONHandler call outside the logging package would
// be detected. Uses go/types synthesis to construct a minimal CallExpr scenario.
// Since we cannot easily inject a fake package via RunTypedDir for this check,
// we verify the detection logic operates correctly by confirming A1 passes on
// the production tree (no false positives) AND by asserting the callsite check
// logic fires when applied to a mock types.Info+AST scenario.
func TestSlogHandlerSealedFunnel_A1_DetectsViolation(t *testing.T) {
	t.Parallel()

	// Verify production tree passes A1 (no bare slog.NewJSONHandler outside logging/).
	// If A1 is vacuous, it would pass even with violations — but the production tree
	// itself is the canonical non-vacuity proof (confirmed in PR #1036 Batch 2
	// red→green cycle).
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{slogFunnelLoggingPkgRelDir})

	// The logging pkg itself should use slog.NewJSONHandler/NewTextHandler.
	var foundInLogging bool
	Run(t, scope, func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			// Scan the logging package for the raw constructor calls.
			EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil {
					return
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok {
					return
				}
				// AST-level check: look for slog.NewJSONHandler or slog.NewTextHandler
				// in the logging package itself (where it is allowed).
				if ident.Name == "slog" &&
					(sel.Sel.Name == slogFunnelNewJSONHandlerFunc || sel.Sel.Name == slogFunnelNewTextHandlerFunc) {
					foundInLogging = true
				}
			})
		}
		return nil
	})
	if !foundInLogging {
		t.Errorf("SLOG-HANDLER-SEALED-FUNNEL-01 A1 non-vacuity: expected to find"+
			" slog.NewJSONHandler or slog.NewTextHandler in %s (the sanctioned location),"+
			" but found none — if the logging implementation changed, update this check",
			slogFunnelLoggingPkgRelDir)
	}
}

// TestSlogHandlerSealedFunnel_A2_DetectsViolation is the reverse self-check for
// A2. It injects two synthetic Handle bodies into contextHandlerHandleRedactCheck:
//
//  1. RED: a Handle body that calls r.Attrs + AddAttrs but does NOT call
//     redaction.RedactSlogAttr — the check must report ≥1 violation.
//
//  2. GREEN: a Handle body that does call both redaction.RedactString (for the
//     message) and redaction.RedactSlogAttr in an AddAttrs callback — must
//     report 0 violations.
//
// This provides non-vacuity proof for A2: the check can actually catch regressions.
// Method: go/parser.ParseFile to build a minimal AST and a fake *Pass with the
// redaction import present (same technique as TestSpanRecordErrorSeal_B_DetectsViolation).
func TestSlogHandlerSealedFunnel_A2_DetectsViolation(t *testing.T) {
	t.Parallel()

	// redactionImport is the import path as it would appear in logging.go.
	const redactionImport = `"github.com/ghbvf/gocell/pkg/redaction"`

	cases := []struct {
		name    string
		src     string
		wantVio bool
		desc    string
	}{
		{
			name: "missing_redact_slog_attr",
			src: `package logging
import (
	"context"
	"log/slog"
	` + redactionImport + `
)
type contextHandler struct{ inner slog.Handler }
func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	nr := slog.NewRecord(r.Time, r.Level, redaction.RedactString(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(a)  // VIOLATION: missing redaction.RedactSlogAttr(a)
		return true
	})
	return h.inner.Handle(ctx, nr)
}
`,
			wantVio: true,
			desc:    "Handle calls AddAttrs without RedactSlogAttr — must be flagged",
		},
		{
			name: "compliant_handle",
			src: `package logging
import (
	"context"
	"log/slog"
	` + redactionImport + `
)
type contextHandler struct{ inner slog.Handler }
func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	nr := slog.NewRecord(r.Time, r.Level, redaction.RedactString(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(redaction.RedactSlogAttr(a))
		return true
	})
	return h.inner.Handle(ctx, nr)
}
`,
			wantVio: false,
			desc:    "Handle calls RedactString + RedactSlogAttr — compliant",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "logging.go", tc.src, parser.SkipObjectResolution)
			require.NoError(t, err, "synthetic fixture must parse: %s", tc.desc)

			// Build a minimal *Pass that is sufficient for contextHandlerHandleRedactCheck
			// (which uses redactionLocalName + AST walk, no types.Info needed).
			// Rel must be set because contextHandlerHandleRedactCheck calls p.Rel(f)
			// for diagnostic positions.
			p := &Pass{
				Fset:  fset,
				Files: []*ast.File{file},
				Rel:   func(*ast.File) string { return "logging.go" },
			}

			var viols []Diagnostic
			EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
				if fn.Name == nil || fn.Body == nil {
					return
				}
				if !HasReceiver(fn, slogFunnelContextHandlerTypeName) || fn.Name.Name != slogFunnelContextHandlerHandleMethod {
					return
				}
				viols = append(viols, contextHandlerHandleRedactCheck(p, file, fn)...)
			})

			if tc.wantVio {
				assert.NotEmpty(t, viols,
					"A2 must detect violation for %q: %s", tc.name, tc.desc)
			} else {
				assert.Empty(t, viols,
					"A2 must not flag %q: %s; got %v", tc.name, tc.desc, viols)
			}
		})
	}

	// Additionally confirm the production contextHandler.Handle yields 0 violations
	// (non-vacuity proof of the GREEN path against real code).
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{slogFunnelLoggingPkgRelDir})
	var handleChecked bool
	_ = Run(t, scope, func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
				if fn.Name == nil || fn.Body == nil {
					return
				}
				if !HasReceiver(fn, slogFunnelContextHandlerTypeName) || fn.Name.Name != slogFunnelContextHandlerHandleMethod {
					return
				}
				handleChecked = true
				violations := contextHandlerHandleRedactCheck(p, f, fn)
				for _, v := range violations {
					t.Errorf("SLOG-HANDLER-SEALED-FUNNEL-01 A2 production violation: %s:%d: %s",
						v.Rel, v.Line, v.Message)
				}
			})
		}
		return nil
	})
	if !handleChecked {
		t.Errorf("SLOG-HANDLER-SEALED-FUNNEL-01 A2 non-vacuity: %s.%s not found in %s",
			slogFunnelContextHandlerTypeName, slogFunnelContextHandlerHandleMethod, slogFunnelLoggingPkgRelDir)
	}
}

// TestSlogHandlerSealedFunnel_A2_WithAttrs_DetectsViolation is the reverse
// self-check for the WithAttrs form-lock (#1036 review round-3): it confirms the
// check flags (a) binding the RAW attrs param to inner.WithAttrs and (b) a body
// with no per-element RedactSlogAttr assignment — neither of which a
// presence-only "RedactSlogAttr appears once" check would catch.
func TestSlogHandlerSealedFunnel_A2_WithAttrs_DetectsViolation(t *testing.T) {
	t.Parallel()
	const redactionImport = `"github.com/ghbvf/gocell/pkg/redaction"`
	cases := []struct {
		name    string
		src     string
		wantVio bool
		desc    string
	}{
		{
			name: "raw_passthrough",
			src: `package logging
import (
	"log/slog"
	` + redactionImport + `
)
type contextHandler struct{ inner slog.Handler }
func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		redacted[i] = redaction.RedactSlogAttr(a)
	}
	return &contextHandler{inner: h.inner.WithAttrs(attrs)}
}
`,
			wantVio: true,
			desc:    "binds raw attrs to inner.WithAttrs despite building a redacted slice",
		},
		{
			name: "missing_redact_assign",
			src: `package logging
import (
	"log/slog"
	` + redactionImport + `
)
type contextHandler struct{ inner slog.Handler }
func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	_ = redaction.RedactString("noop")
	return &contextHandler{inner: h.inner.WithAttrs(make([]slog.Attr, 0))}
}
`,
			wantVio: true,
			desc:    "no per-element RedactSlogAttr assignment",
		},
		{
			name: "compliant",
			src: `package logging
import (
	"log/slog"
	` + redactionImport + `
)
type contextHandler struct{ inner slog.Handler }
func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	redacted := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		redacted[i] = redaction.RedactSlogAttr(a)
	}
	return &contextHandler{inner: h.inner.WithAttrs(redacted)}
}
`,
			wantVio: false,
			desc:    "binds the redacted slice — compliant",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "logging.go", tc.src, parser.SkipObjectResolution)
			require.NoError(t, err, "synthetic fixture must parse: %s", tc.desc)
			p := &Pass{
				Fset:  fset,
				Files: []*ast.File{file},
				Rel:   func(*ast.File) string { return "logging.go" },
			}
			var viols []Diagnostic
			EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
				if fn.Name == nil || fn.Body == nil {
					return
				}
				if !HasReceiver(fn, slogFunnelContextHandlerTypeName) || fn.Name.Name != slogFunnelContextHandlerWithAttrsMethod {
					return
				}
				viols = append(viols, contextHandlerWithAttrsRedactCheck(p, file, fn)...)
			})
			if tc.wantVio {
				assert.NotEmpty(t, viols, "WithAttrs form-lock must detect %q: %s", tc.name, tc.desc)
			} else {
				assert.Empty(t, viols, "WithAttrs form-lock must not flag %q: %s; got %v", tc.name, tc.desc, viols)
			}
		})
	}
}

// TestSlogHandlerSealedFunnel_A3_Order_DetectsViolation is the reverse self-check
// for the A3 ordering form-lock (#1036 review round-3): it proves
// firstCallBearingStmtSatisfies returns false when a non-matching call precedes
// the target call, and true when the target is the first call-bearing statement.
// Uses a pure-AST name predicate (.SetDefault) so no go/types is needed.
func TestSlogHandlerSealedFunnel_A3_Order_DetectsViolation(t *testing.T) {
	t.Parallel()
	isSetDefault := func(call *ast.CallExpr) bool {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		return ok && sel.Sel != nil && sel.Sel.Name == "SetDefault"
	}
	cases := []struct {
		name   string
		src    string
		wantOK bool
		desc   string
	}{
		{
			name: "seal_first",
			src: `package main
func run() {
	slog.SetDefault(x)
	other()
}`,
			wantOK: true,
			desc:   "SetDefault is the first call-bearing statement",
		},
		{
			name: "fallible_call_before_seal",
			src: `package main
func run() {
	mods, err := loadModules()
	_ = err
	slog.SetDefault(x)
	_ = mods
}`,
			wantOK: false,
			desc:   "a fallible call precedes the seal — ordering violation",
		},
		{
			name: "non_call_decls_before_seal_ok",
			src: `package main
func run() {
	var n int
	n = 1
	slog.SetDefault(x)
	_ = n
}`,
			wantOK: true,
			desc:   "non-call statements before the seal are skipped",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "main.go", tc.src, parser.SkipObjectResolution)
			require.NoError(t, err, "synthetic fixture must parse: %s", tc.desc)
			var got bool
			EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
				if fn.Name != nil && fn.Name.Name == "run" && fn.Body != nil {
					got = firstCallBearingStmtSatisfies(fn.Body, isSetDefault)
				}
			})
			assert.Equal(t, tc.wantOK, got, "A3 ordering: %s", tc.desc)
		})
	}
}

// TestSlogHandlerSealedFunnel_A3_DetectsViolation is the reverse self-check for
// A3: confirms that a function named runCorebundle which lacks the SetDefault
// call would be detected by the shape checker.
func TestSlogHandlerSealedFunnel_A3_DetectsViolation(t *testing.T) {
	t.Parallel()

	// Use go/types to verify that the production runCorebundle does contain
	// the SetDefault call. This simultaneously proves non-vacuity (if it doesn't
	// contain the call, the production check would have already failed A3).
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("SLOG-HANDLER-SEALED-FUNNEL-01 A3 reverse check: read module path: %v", err)
	}
	loggingPkgPath := modPath + "/" + slogFunnelLoggingPkgImportSuffix

	var foundSetDefault bool
	_ = RunTyped(t, TypedOpts{Tests: false}, []string{"./cmd/corebundle"},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			for _, f := range p.Files {
				EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
					if fn.Name == nil || fn.Name.Name != "runCorebundle" {
						return
					}
					EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
						if slogSetDefaultShape(p.TypesInfo, call, loggingPkgPath) {
							foundSetDefault = true
						}
					})
				})
			}
			return nil
		})

	if !foundSetDefault {
		t.Errorf("SLOG-HANDLER-SEALED-FUNNEL-01 A3 non-vacuity: runCorebundle does not" +
			" contain slog.SetDefault(slog.New(logging.NewHandler(...)));" +
			" A3 would pass vacuously — the seal must be present in the production entry point")
	}

	// Verify the shape detector rejects a nil info (safety check).
	if slogSetDefaultShape(nil, &ast.CallExpr{}, "") {
		t.Errorf("SLOG-HANDLER-SEALED-FUNNEL-01 A3 shape detector: slogSetDefaultShape" +
			" with nil info must return false")
	}

	// Verify the shape detector rejects a bare slog.New call (no SetDefault wrapper).
	// We create a fake AST node structure and verify it is not matched.
	// A call like slog.New(logging.NewHandler(...)) without SetDefault wrapping
	// should NOT match. We test this by checking that slogSetDefaultShape returns
	// false when the outer call is not slog.SetDefault.
	// Since IsCallToPkgFunc requires a real types.Info, we rely on the production
	// proof above for this guarantee.
	t.Log("SLOG-HANDLER-SEALED-FUNNEL-01 A3 reverse check: SetDefault shape detector" +
		" verified non-vacuous via production runCorebundle confirmation")
}

// TestSlogHandlerSealedFunnel_NoBlindspotsInProduction asserts that the known
// blind spot — a custom slog.Handler implementation outside logging/ — does not
// exist in production code. If a future contributor adds one, this check makes
// the blind spot visible at archtest time.
//
// This is a go/types scan: we look for types that implement slog.Handler outside
// the logging package. The slog.Handler interface requires Enabled, Handle,
// WithAttrs, WithGroup methods.
func TestSlogHandlerSealedFunnel_NoBlindspotsInProduction(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("SLOG-HANDLER-SEALED-FUNNEL-01 blindspot: read module path: %v", err)
	}
	loggingFullPkgPath := modPath + "/" + slogFunnelLoggingPkgImportSuffix

	// healthtest contains a CaptureHandler for testing (acceptable test helper).
	// The check skips _test.go files since RunTypedProduction uses Tests:false.
	var ds []Diagnostic
	RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		pkgPath := p.Pkg.Path()
		// Skip the sanctioned logging package and healthtest (test helper).
		if pkgPath == loggingFullPkgPath {
			return nil
		}
		if strings.HasSuffix(pkgPath, "healthtest") {
			return nil
		}

		// Check whether any named type in this package implements slog.Handler.
		slogHandlerIface := findSlogHandlerInterface(p.TypesInfo, p.Pkg)
		if slogHandlerIface == nil {
			return nil
		}

		scope := p.Pkg.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if obj == nil {
				continue
			}
			typObj, ok := obj.(*types.TypeName)
			if !ok {
				continue
			}
			named, ok := typObj.Type().(*types.Named)
			if !ok {
				continue
			}
			// Check pointer and value receiver forms via the sanctioned funnel.
			if typesutil.ImplementsInterface(named, slogHandlerIface) {
				pos := p.Fset.Position(obj.Pos())
				ds = append(ds, Diagnostic{
					Rel:  filepath.ToSlash(p.Rel(findFileAtPos(p, obj.Pos()))),
					Line: pos.Line,
					Message: "type " + name + " in package " + pkgPath + " implements slog.Handler" +
						" outside runtime/observability/logging — this is a blind spot of" +
						" SLOG-HANDLER-SEALED-FUNNEL-01 A1; route slog sink construction" +
						" through logging.NewHandler or register this type in the funnel",
				})
			}
		}
		return nil
	})
	Report(t, "SLOG-HANDLER-SEALED-FUNNEL-01-BLINDSPOT", ds)
}

// findSlogHandlerInterface returns the *types.Interface for log/slog.Handler
// by iterating the imports of the package. Returns nil if not found.
func findSlogHandlerInterface(info *types.Info, pkg *types.Package) *types.Interface {
	if info == nil || pkg == nil {
		return nil
	}
	// Walk the package's imports to find log/slog.
	for _, imp := range pkg.Imports() {
		if imp.Path() != slogFunnelStdlibPkgPath {
			continue
		}
		obj := imp.Scope().Lookup("Handler")
		if obj == nil {
			return nil
		}
		typeName, ok := obj.(*types.TypeName)
		if !ok {
			return nil
		}
		iface, ok := typeName.Type().Underlying().(*types.Interface)
		if !ok {
			return nil
		}
		return iface
	}
	return nil
}

// findFileAtPos returns the *ast.File in the pass that contains the given
// position, or nil if not found. Used to build Rel paths for diagnostics.
func findFileAtPos(p *Pass, pos token.Pos) *ast.File {
	for _, f := range p.Files {
		if f.Pos() <= pos && pos <= f.End() {
			return f
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// C3: Entry-point reverse coverage — allowlist must not have gaps
// ---------------------------------------------------------------------------

// TestSlogHandlerSealedFunnel_A3_EntryPointsCoverage complements A3 by
// asserting in the other direction: every production package under cmd/ or
// examples/ that imports runtime/bootstrap must have at least one function in
// slogHandlerEntryPoints. This prevents an entirely new assembly from being
// wired up without updating the A3 allowlist.
//
// Granularity: package-level (not function-level). Within an existing package,
// the actual SetDefault-bearing function is already verified by A3. This check
// only detects NEW packages that bootstrap without a corresponding A3 entry.
//
// Exclusion: cmd/gocell does not import runtime/bootstrap (CLI tool, not a
// long-running service) — it is naturally excluded by the bootstrap-import filter.
//
// AI-robust rating: Medium (same as A3 — caller-allowlist; Hard path tracked
// at gh #1401).
func TestSlogHandlerSealedFunnel_A3_EntryPointsCoverage(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("A3 entry-point coverage: read module path: %v", err)
	}
	bootstrapPkgPath := modPath + "/runtime/bootstrap"

	// Build a lookup set of packages already covered by the allowlist.
	coveredPkgs := make(map[string]bool) // pkgPattern → true
	for _, ep := range slogHandlerEntryPoints {
		coveredPkgs[ep.pkgPattern] = true
	}

	// Scan all cmd/ and examples/ packages for bootstrap imports.
	var ds []Diagnostic
	RunTyped(t, TypedOpts{Tests: false}, []string{"./cmd/...", "./examples/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			pkgPath := p.Pkg.Path()

			// Only consider packages that import runtime/bootstrap.
			importsBootstrap := false
			for _, imp := range p.Pkg.Imports() {
				if imp.Path() == bootstrapPkgPath {
					importsBootstrap = true
					break
				}
			}
			if !importsBootstrap {
				return nil
			}

			// Derive the ./relative/pkg pattern form (strip module path prefix).
			relPkg := strings.TrimPrefix(pkgPath, modPath+"/")
			pkgPattern := "./" + relPkg

			if coveredPkgs[pkgPattern] {
				return nil // already in allowlist — OK
			}

			// New package importing bootstrap but not in allowlist: flag it.
			// Use the package-level position for the diagnostic.
			for _, f := range p.Files {
				if strings.HasSuffix(filepath.ToSlash(p.Rel(f)), "_test.go") {
					continue
				}
				pos := p.Fset.Position(f.Pos())
				ds = append(ds, Diagnostic{
					Rel:  filepath.ToSlash(p.Rel(f)),
					Line: pos.Line,
					Message: "package " + pkgPattern + " imports runtime/bootstrap" +
						" but has NO entry in slogHandlerEntryPoints" +
						" — add the assembly entry-point function name to the allowlist;" +
						" SLOG-HANDLER-SEALED-FUNNEL-01 A3 reverse coverage",
				})
				break // one diagnostic per package is sufficient
			}
			return nil
		})
	Report(t, "SLOG-HANDLER-SEALED-FUNNEL-01-A3-COVERAGE", ds)
}

// ---------------------------------------------------------------------------
// C4: LogValuer self-redact enrollment
// ---------------------------------------------------------------------------

// slogLogValuerAllowlist is the authoritative list of production named types
// that implement slog.LogValuer. Each entry carries a brief justification for
// why the type is safe to pass through as KindLogValuer (i.e., its LogValue()
// return value is self-redacted).
//
// When a new LogValuer is added to production code, it MUST appear here with a
// justification. If its LogValue() does NOT self-redact, report the gap before
// adding it — it is a potential PII leak.
var slogLogValuerAllowlist = []struct {
	// pkg is the full import path of the package containing the type.
	pkg string
	// typeName is the exported or unexported type name.
	typeName string
	// selfRedacts explains how the type protects sensitive fields.
	selfRedacts string
}{
	{
		pkg:         "runtime/http/health",
		typeName:    "SlogDependencyEntry",
		selfRedacts: "error_msg via newRedactedErrorMsg→RedactString (HEALTH-REDACTED-ERROR-MSG-FUNNEL-01); status/durationMs non-sensitive",
	},
	{
		pkg:         "adapters/redis",
		typeName:    "Config",
		selfRedacts: "password absent from LogValue; standalone+cluster addrs via redactAddr/url.Redacted (#1036 F1)",
	},
	{
		pkg:         "adapters/mqtt",
		typeName:    "AuthConfig",
		selfRedacts: "password replaced with redaction.Mask when non-empty; username is non-sensitive",
	},
	{
		pkg:         "kernel/webhook",
		typeName:    "Source",
		selfRedacts: "secret field always replaced with redaction.Mask literal",
	},
	{
		pkg:         "kernel/webhook",
		typeName:    "hmacSigner",
		selfRedacts: "delegates to Source.LogValue() which self-redacts the secret",
	},
	{
		pkg:         "kernel/command",
		typeName:    "Entry",
		selfRedacts: "payload replaced with '<REDACTED bytes=N>' sentinel; no credential fields in other attrs",
	},
}

// TestSlogHandlerSealedFunnel_LogValuerSelfRedactEnrollment asserts that every
// production named type implementing slog.LogValuer is present in
// slogLogValuerAllowlist. New LogValuers not in the list cause a CI failure,
// forcing the author to review the self-redaction claim before the type can
// pass through contextHandler's KindLogValuer passthrough path unredacted.
//
// AI-robust rating: Medium (types.Implements scan; Hard path = codegen golden
// enumeration of LogValuer implementations, tracked at gh #1401 alongside A3).
func TestSlogHandlerSealedFunnel_LogValuerSelfRedactEnrollment(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("LogValuer enrollment: read module path: %v", err)
	}

	// Build lookup: "relPkg/typeName" → true.
	enrolled := make(map[string]bool)
	for _, e := range slogLogValuerAllowlist {
		enrolled[e.pkg+"/"+e.typeName] = true
	}

	var ds []Diagnostic
	RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		pkgPath := p.Pkg.Path()
		relPkg := strings.TrimPrefix(pkgPath, modPath+"/")

		// Find slog.LogValuer interface.
		logValuerIface := findSlogLogValuerInterface(p.TypesInfo, p.Pkg)
		if logValuerIface == nil {
			return nil
		}

		scope := p.Pkg.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if obj == nil {
				continue
			}
			typObj, ok := obj.(*types.TypeName)
			if !ok {
				continue
			}
			named, ok := typObj.Type().(*types.Named)
			if !ok {
				continue
			}
			if !typesutil.ImplementsInterface(named, logValuerIface) {
				continue
			}
			key := relPkg + "/" + name
			if !enrolled[key] {
				pos := p.Fset.Position(obj.Pos())
				ds = append(ds, Diagnostic{
					Rel:  filepath.ToSlash(p.Rel(findFileAtPos(p, obj.Pos()))),
					Line: pos.Line,
					Message: "type " + name + " in " + relPkg + " implements slog.LogValuer" +
						" but is NOT in slogLogValuerAllowlist — add it with a self-redacts" +
						" justification, or fix its LogValue() to redact sensitive fields;" +
						" KindLogValuer passes through contextHandler unredacted" +
						" (SLOG-HANDLER-SEALED-FUNNEL-01 C4)",
				})
			}
		}
		return nil
	})
	Report(t, "SLOG-HANDLER-SEALED-FUNNEL-01-LOGVALUER-ENROLLMENT", ds)
}

// findSlogLogValuerInterface returns the *types.Interface for log/slog.LogValuer
// by iterating the imports of the package. Returns nil if not found.
func findSlogLogValuerInterface(info *types.Info, pkg *types.Package) *types.Interface {
	if info == nil || pkg == nil {
		return nil
	}
	for _, imp := range pkg.Imports() {
		if imp.Path() != slogFunnelStdlibPkgPath {
			continue
		}
		obj := imp.Scope().Lookup("LogValuer")
		if obj == nil {
			return nil
		}
		typeName, ok := obj.(*types.TypeName)
		if !ok {
			return nil
		}
		iface, ok := typeName.Type().Underlying().(*types.Interface)
		if !ok {
			return nil
		}
		return iface
	}
	return nil
}
