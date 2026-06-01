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
//	A3 (entry point SetDefault present AND first-ordered) — TWO SEGMENTS:
//	   * Generated segment (Hard, self-covering): the seal is injected by the
//	     assembly template (kernel/assembly/gentpl/main.go.tpl) into the generated
//	     run() of every `gocell generate assembly` main. The archtest derives its
//	     coverage from the generated marker (no hand-maintained list) and requires
//	     the seal as the first call-bearing statement in run(). Upstream Hard =
//	     regenerate-and-diff byte lock (cmd/gocell generated verify test); downstream
//	     Hard = this marker-derived invariant scan. Adding a new assembly is covered
//	     automatically — #1401 is delivered for generated entry points.
//	   * Handwritten segment (Medium): the bounded slogHandwrittenEntryPoints
//	     allowlist (ssobff, corebundlestarter) must each seal in main() as the first
//	     call-bearing statement. Codegen cannot reach hand-written mains, so this is
//	     a Go-language permanent ceiling (A1's Hard bare-handler ban is the backstop).
//	     Deliberate won't-do, tracked at gh #1424.
//	   Both use the ordering form-lock (#1036 review round-3 — nothing fallible/
//	   logging may run before the seal).
//
// AI-robust rating — Funnel double-lock:
//   - Downstream A1: Hard — go/types typed callee resolution; import-alias bypass
//     ineffective; archtest fails on any out-of-funnel slog.New{JSON,Text}Handler call.
//   - Downstream A2: Hard — AST form-uniqueness; dropping RedactSlogAttr from the
//     Handle callback body fails the archtest immediately.
//   - A3 generated segment: Hard — codegen funnel (template injection) + regenerate
//     byte-diff lock + marker-derived invariant scan; #1401 delivered.
//   - A3 handwritten segment: Medium — bounded allowlist; Go-language ceiling (no
//     mechanism forces a hand-written main to SetDefault first). Won't-do gh #1424.
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
//     slog.Default before the entry point runs. For the generated segment, the
//     codegen template injects the seal as the first statement of run(), closing
//     (i) structurally; (ii) remains the residual ceiling for the handwritten
//     segment (gh #1424). Reverse self-check
//     TestSlogHandlerSealedFunnel_A3_DetectsViolation verifies the generated
//     cmd/corebundle run() seals (and the marker discriminator does not absorb
//     hand-written mains).
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

	// slogFunnelAssemblyGenMarker is the leading marker comment emitted by
	// `gocell generate assembly` into every assembly main.go. The A3 generated
	// segment derives its coverage set from this marker (no hand-maintained
	// list), so every current and future generated assembly entry point is
	// automatically required to seal — the seal itself is injected by
	// kernel/assembly/gentpl/main.go.tpl into the generated run() function.
	slogFunnelAssemblyGenMarker = "Code generated by gocell generate assembly"

	// slogFunnelGeneratedRunFunc is the function name the assembly template
	// emits the seal into (the generated main's run() wrapper that main() calls).
	slogFunnelGeneratedRunFunc = "run"
)

// slogHandwrittenEntryPoints is the bounded allowlist of HAND-WRITTEN production
// entry points (NOT generated by `gocell generate assembly`) that must each
// contain a slog.SetDefault(slog.New(logging.NewHandler(...))) call as the first
// call-bearing statement. Generated assembly mains are covered by the A3
// generated segment (marker-derived, Hard self-covering) and MUST NOT appear
// here. This residual list is the Go-language permanent Medium ceiling: codegen
// cannot reach hand-written mains, so their seal is hand-maintained (A1's Hard
// ban on bare handler construction outside logging/ is the backstop). Tracked as
// a deliberate won't-do at gh #1424 (analogous to #851 / #893 holder-seal
// ceilings).
var slogHandwrittenEntryPoints = []struct {
	pkgPattern string // pattern for RunTyped
	funcName   string // top-level function name
	relPath    string // module-relative path (for diagnostics)
}{
	{"./examples/ssobff", "main", "examples/ssobff/main.go"},
	{"./examples/corebundlestarter", "main", "examples/corebundlestarter/main.go"},
	// cmd/gocell is the governance/codegen CLI: it does NOT import runtime/bootstrap
	// (so C3 reverse-coverage does not require it) but it emits production
	// slog.Warn/Error, so it seals (FormatText, human-readable) and is enrolled here
	// for A3 handwritten seal verification.
	{"./cmd/gocell", "main", "cmd/gocell/main.go"},
}

// isGocellAssemblyGenerated reports whether f carries the `gocell generate
// assembly` marker comment. Note this matches BOTH the entry main.go AND sibling
// generated files (e.g. modules_gen.go) emitted by the same command — callers
// that want only the entry point must additionally gate on fileDeclaresFunc(f,
// "main"). Comments are available because the archtest Pass parses with
// parser.ParseComments (see pass.go).
func isGocellAssemblyGenerated(f *ast.File) bool {
	for _, cg := range f.Comments {
		if strings.Contains(cg.Text(), slogFunnelAssemblyGenMarker) {
			return true
		}
	}
	return false
}

// isGocellAssemblyEntryMain reports whether f is the assembly ENTRY main.go: it
// carries the assembly marker AND declares a top-level func main. This excludes
// sibling generated files (modules_gen.go) that share the marker but have no
// entry point / no run() seal.
func isGocellAssemblyEntryMain(f *ast.File) bool {
	return isGocellAssemblyGenerated(f) && fileDeclaresFunc(f, "main")
}

// fileDeclaresFunc reports whether f declares a top-level (non-method) function
// with the given name.
func fileDeclaresFunc(f *ast.File, name string) bool {
	found := false
	EachInChildren[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
		if fn.Recv == nil && fn.Name != nil && fn.Name.Name == name {
			found = true
		}
	})
	return found
}

// sealStatusInFunc locates the FuncDecl named funcName in f and reports whether
// its body (a) contains a slog.SetDefault(slog.New(logging.NewHandler(...))) call
// and (b) has that call as the first call-bearing statement, plus the FuncDecl's
// position (token.NoPos if the func is absent) so callers can emit a line-anchored
// diagnostic. Shared by the A3 generated segment (funcName="run") and handwritten
// segment (funcName="main").
func sealStatusInFunc(info *types.Info, f *ast.File, funcName, loggingPkgPath string) (found, sealFirst bool, funcPos token.Pos) {
	if info == nil {
		return false, false, token.NoPos
	}
	funcPos = token.NoPos
	EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
		if fn.Name == nil || fn.Body == nil || fn.Name.Name != funcName {
			return
		}
		funcPos = fn.Pos()
		EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
			if slogSetDefaultShape(info, call, loggingPkgPath) {
				found = true
			}
		})
		sealFirst = firstCallBearingStmtSatisfies(fn.Body, func(call *ast.CallExpr) bool {
			return slogSetDefaultShape(info, call, loggingPkgPath)
		})
	})
	return found, sealFirst, funcPos
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
			ds = append(ds, slogBareHandlerViolations(p, f, rel)...)
		}
		return ds
	})
	Report(t, "SLOG-HANDLER-SEALED-FUNNEL-01", diags)
}

// slogBareHandlerViolations is the A1 per-file detector: it reports every
// slog.NewJSONHandler / slog.NewTextHandler call in f, resolved via go/types
// (IsCallToPkgFunc, so import aliases do not bypass it). Shared by the production
// A1 scan (which skips the logging package) and the A1 reverse self-check, which
// runs it on an external fixture package to prove the detector is non-vacuous.
func slogBareHandlerViolations(p *Pass, f *ast.File, rel string) []Diagnostic {
	if p.TypesInfo == nil {
		return nil
	}
	var ds []Diagnostic
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
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
	return ds
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

	// Check 1 (form-lock, #1432): the redacted record must be built via
	// slog.NewRecord(..., redaction.RedactString(<recordParam>.Message), ...).
	// The message argument (3rd positional) is bound to RedactString applied to
	// the record formal parameter's .Message — a bare <r>.Message, or RedactString
	// of some other expression, is rejected. Upgraded from a presence check
	// ("RedactString appears somewhere"), which would pass even if the message
	// itself was forwarded raw and RedactString was only applied elsewhere.
	recordParam := recordParamName(fn)
	var newRecordCall *ast.CallExpr
	EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel != nil && sel.Sel.Name == "NewRecord" {
			newRecordCall = call
		}
	})
	msgOK := false
	if newRecordCall != nil && len(newRecordCall.Args) >= 3 && redactionLocal != "" && recordParam != "" {
		if msgArg, ok := newRecordCall.Args[2].(*ast.CallExpr); ok &&
			callMatches(msgArg, redactionLocal, slogFunnelRedactStringFunc) &&
			len(msgArg.Args) == 1 && isSelectorOf(msgArg.Args[0], recordParam, "Message") {
			msgOK = true
		}
	}
	if !msgOK {
		pos := p.Fset.Position(fn.Pos())
		ds = append(ds, Diagnostic{
			Rel:  filepath.ToSlash(p.Rel(f)),
			Line: pos.Line,
			Message: "contextHandler.Handle must build the record message via" +
				" slog.NewRecord(..., redaction.RedactString(<record>.Message), ...)" +
				" — SLOG-HANDLER-SEALED-FUNNEL-01 A2 message form-lock",
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

// recordParamName returns the name of the slog.Record formal parameter of fn
// (the parameter whose type is a selector expression ending in "Record"), or ""
// if not found. Used to bind the A2 message form-lock to the actual record param.
func recordParamName(fn *ast.FuncDecl) string {
	if fn.Type == nil || fn.Type.Params == nil {
		return ""
	}
	for _, field := range fn.Type.Params.List {
		sel, ok := field.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "Record" {
			continue
		}
		if len(field.Names) > 0 {
			return field.Names[0].Name
		}
	}
	return ""
}

// isSelectorOf reports whether expr is the selector `<xName>.<selName>` with a
// plain identifier receiver named xName (xName must be non-empty).
func isSelectorOf(expr ast.Expr, xName, selName string) bool {
	if xName == "" {
		return false
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != selName {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == xName
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

// TestSlogHandlerSealedFunnel_A3_EntryPointSeal enforces A3 in two segments:
//
//	Generated segment (Hard, self-covering): every assembly main.go emitted by
//	`gocell generate assembly` (detected via the marker comment, NOT a hand-
//	maintained list) must seal in its generated run() function as the first
//	call-bearing statement. The seal is injected by the assembly template
//	(kernel/assembly/gentpl/main.go.tpl); regeneration-and-diff (the generated
//	verify test in cmd/gocell) is the byte-level upstream lock, and this scan is
//	the downstream invariant lock. Adding a new assembly never requires touching
//	this archtest — the marker derivation covers it automatically.
//
//	Handwritten segment (Medium, bounded): each entry in slogHandwrittenEntryPoints
//	(ssobff, corebundlestarter) must seal in its main() as the first call-bearing
//	statement. Codegen cannot reach hand-written mains, so this stays a Go-language
//	permanent Medium ceiling, backstopped by A1's Hard bare-handler ban. Tracked
//	as a deliberate won't-do at gh #1424.
//
// Both segments use the ordering form-lock (firstCallBearingStmtSatisfies): the
// seal must be the FIRST call-bearing statement — nothing fallible or logging may
// run before it. A presence-only check would pass even if a log call preceded it.
func TestSlogHandlerSealedFunnel_A3_EntryPointSeal(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("SLOG-HANDLER-SEALED-FUNNEL-01 A3: read module path: %v", err)
	}
	loggingPkgPath := modPath + "/" + slogFunnelLoggingPkgImportSuffix

	var all []Diagnostic

	// --- Generated segment (Hard, self-covering): scan the whole production tree
	// for assembly-generated main.go files and require the seal in run().
	generatedCount := 0
	RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		for _, f := range p.Files {
			if !isGocellAssemblyEntryMain(f) {
				continue
			}
			generatedCount++
			rel := filepath.ToSlash(p.Rel(f))
			found, sealFirst, funcPos := sealStatusInFunc(p.TypesInfo, f, slogFunnelGeneratedRunFunc, loggingPkgPath)
			line := 0
			if funcPos != token.NoPos {
				line = p.Fset.Position(funcPos).Line
			}
			switch {
			case !found:
				all = append(all, Diagnostic{
					Rel:  rel,
					Line: line,
					Message: "generated assembly entry point (" + slogFunnelGeneratedRunFunc +
						") must call slog.SetDefault(slog.New(logging.NewHandler(...))) —" +
						" the seal is injected by kernel/assembly/gentpl/main.go.tpl;" +
						" regenerate with `gocell generate assembly --all`" +
						" (SLOG-HANDLER-SEALED-FUNNEL-01 A3 generated segment)",
				})
			case !sealFirst:
				all = append(all, Diagnostic{
					Rel:  rel,
					Line: line,
					Message: "generated assembly entry point (" + slogFunnelGeneratedRunFunc +
						") must call slog.SetDefault(...) as the FIRST call-bearing statement" +
						" (SLOG-HANDLER-SEALED-FUNNEL-01 A3 generated segment)",
				})
			}
		}
		return nil
	})
	// Non-vacuity: the generated segment must find at least one assembly main.
	if generatedCount == 0 {
		t.Errorf("SLOG-HANDLER-SEALED-FUNNEL-01 A3 generated segment: found 0"+
			" assembly-generated main.go files (marker %q) — the scan is vacuous;"+
			" expected ≥1 (cmd/corebundle + example assemblies)", slogFunnelAssemblyGenMarker)
	}

	// --- Handwritten segment (Medium, bounded 2-entry allowlist).
	for _, ep := range slogHandwrittenEntryPoints {
		found := false
		sealIsFirstCall := false
		funcLine := 0
		RunTyped(t, TypedOpts{Tests: false}, []string{ep.pkgPattern},
			func(p *Pass) []Diagnostic {
				if p.TypesInfo == nil {
					return nil
				}
				for _, f := range p.Files {
					if strings.HasSuffix(filepath.ToSlash(p.Rel(f)), "_test.go") {
						continue
					}
					// Hand-written mains must not carry the generated marker.
					if isGocellAssemblyGenerated(f) {
						continue
					}
					fFound, fFirst, fPos := sealStatusInFunc(p.TypesInfo, f, ep.funcName, loggingPkgPath)
					if fFound {
						found = true
					}
					if fFirst {
						sealIsFirstCall = true
					}
					if fPos != token.NoPos {
						funcLine = p.Fset.Position(fPos).Line
					}
				}
				return nil
			})

		switch {
		case !found:
			all = append(all, Diagnostic{
				Rel:  ep.relPath,
				Line: funcLine,
				Message: "hand-written entry point function " + ep.funcName +
					" must call slog.SetDefault(slog.New(logging.NewHandler(...)))" +
					" — SLOG-HANDLER-SEALED-FUNNEL-01 A3 handwritten segment; gh #1424",
			})
		case !sealIsFirstCall:
			all = append(all, Diagnostic{
				Rel:  ep.relPath,
				Line: funcLine,
				Message: "hand-written entry point function " + ep.funcName +
					" must call slog.SetDefault(...) as the FIRST call-bearing" +
					" statement (no fallible or logging call before the seal) —" +
					" SLOG-HANDLER-SEALED-FUNNEL-01 A3 handwritten segment; gh #1424",
			})
		}
	}

	Report(t, "SLOG-HANDLER-SEALED-FUNNEL-01", all)
}

// ---------------------------------------------------------------------------
// Reverse self-checks (non-vacuity proofs per ai-robust.md §载体决策原则)
// ---------------------------------------------------------------------------

// TestSlogHandlerSealedFunnel_A1_DetectsViolation is the reverse self-check for
// A1. It runs the production A1 detector (slogBareHandlerViolations) against a
// real external fixture package (testdata/slog_bare_handler_fixtures/external_violation,
// loaded type-checked via RunTypedFixture) that calls slog.NewJSONHandler /
// NewTextHandler outside runtime/observability/logging — and asserts BOTH call
// sites are flagged. This is a true external-violation RED proof (replacing the
// earlier "production tree self-proof", which only showed the logging package
// uses the constructors, not that an external violation is detected).
//
// The fixture imports log/slog under a non-default alias (slogsink), so a passing
// result also proves the detector resolves the callee by go/types package path,
// not by a textual "slog." prefix.
func TestSlogHandlerSealedFunnel_A1_DetectsViolation(t *testing.T) {
	t.Parallel()

	const fixturePkgPath = "github.com/ghbvf/gocell/tools/archtest/testdata/slog_bare_handler_fixtures/external_violation"

	diags := RunTypedFixture(t, FixtureOpts{Tests: false},
		[]string{"./tools/archtest/testdata/slog_bare_handler_fixtures/external_violation"},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != fixturePkgPath {
				return nil
			}
			var ds []Diagnostic
			for _, f := range p.Files {
				ds = append(ds, slogBareHandlerViolations(p, f, filepath.ToSlash(p.Rel(f)))...)
			}
			return ds
		})

	require.Len(t, diags, 2,
		"A1 detector must flag exactly 2 external violations (NewJSONHandler +"+
			" NewTextHandler) in the fixture; got: %v", diags)
	joined := ""
	for _, d := range diags {
		joined += d.Message + "\n"
	}
	assert.Contains(t, joined, slogFunnelNewJSONHandlerFunc)
	assert.Contains(t, joined, slogFunnelNewTextHandlerFunc)
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
		{
			name: "bare_message_no_redact",
			src: `package logging
import (
	"context"
	"log/slog"
	` + redactionImport + `
)
type contextHandler struct{ inner slog.Handler }
func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	nr := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)  // VIOLATION: raw r.Message
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(redaction.RedactSlogAttr(a))
		return true
	})
	return h.inner.Handle(ctx, nr)
}
`,
			wantVio: true,
			desc:    "message form-lock: bare r.Message (not RedactString-wrapped) must be flagged",
		},
		{
			name: "redact_wrong_expr",
			src: `package logging
import (
	"context"
	"log/slog"
	` + redactionImport + `
)
type contextHandler struct{ inner slog.Handler }
func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	var other string
	// VIOLATION: RedactString applied to some other value, not r.Message — the
	// presence-check predecessor would pass this since RedactString "appears".
	nr := slog.NewRecord(r.Time, r.Level, redaction.RedactString(other), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(redaction.RedactSlogAttr(a))
		return true
	})
	return h.inner.Handle(ctx, nr)
}
`,
			wantVio: true,
			desc:    "message form-lock: RedactString of a non-Message expression must be flagged",
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
// A3. It proves non-vacuity of BOTH segments:
//   - generated segment: the generated cmd/corebundle run() contains the seal
//     (and isGocellAssemblyGenerated discriminates the marker correctly);
//   - the seal shape detector rejects nil info.
//
// The marker discriminator check (generated main → true, hand-written main →
// false) proves the generated-segment derivation is not vacuous and that
// hand-written mains are not silently absorbed into the generated segment.
func TestSlogHandlerSealedFunnel_A3_DetectsViolation(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("SLOG-HANDLER-SEALED-FUNNEL-01 A3 reverse check: read module path: %v", err)
	}
	loggingPkgPath := modPath + "/" + slogFunnelLoggingPkgImportSuffix

	// Generated-segment non-vacuity: cmd/corebundle's generated main.go must be
	// marker-detected AND seal in run().
	var sawGeneratedMain, generatedSealed bool
	_ = RunTyped(t, TypedOpts{Tests: false}, []string{"./cmd/corebundle"},
		func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil {
				return nil
			}
			for _, f := range p.Files {
				if !isGocellAssemblyEntryMain(f) {
					continue
				}
				sawGeneratedMain = true
				if found, sealFirst, _ := sealStatusInFunc(p.TypesInfo, f, slogFunnelGeneratedRunFunc, loggingPkgPath); found && sealFirst {
					generatedSealed = true
				}
			}
			return nil
		})
	if !sawGeneratedMain {
		t.Errorf("SLOG-HANDLER-SEALED-FUNNEL-01 A3 reverse check: cmd/corebundle has no" +
			" assembly-generated main.go (marker not detected) — the generated segment" +
			" derivation would be vacuous")
	}
	if !generatedSealed {
		t.Errorf("SLOG-HANDLER-SEALED-FUNNEL-01 A3 non-vacuity: cmd/corebundle generated" +
			" run() does not seal as the first call-bearing statement; A3 generated" +
			" segment would pass vacuously")
	}

	// Marker discriminator: a hand-written main (ssobff) must NOT be flagged as
	// generated — otherwise the generated segment would silently absorb it.
	var sawHandwrittenMain, handwrittenIsGenerated bool
	_ = RunTyped(t, TypedOpts{Tests: false}, []string{"./examples/ssobff"},
		func(p *Pass) []Diagnostic {
			for _, f := range p.Files {
				if strings.HasSuffix(filepath.ToSlash(p.Rel(f)), "_test.go") {
					continue
				}
				rel := filepath.ToSlash(p.Rel(f))
				if rel != "examples/ssobff/main.go" {
					continue
				}
				sawHandwrittenMain = true
				if isGocellAssemblyGenerated(f) {
					handwrittenIsGenerated = true
				}
			}
			return nil
		})
	if !sawHandwrittenMain {
		t.Errorf("SLOG-HANDLER-SEALED-FUNNEL-01 A3 reverse check: examples/ssobff/main.go" +
			" not found — discriminator check is vacuous")
	}
	if handwrittenIsGenerated {
		t.Errorf("SLOG-HANDLER-SEALED-FUNNEL-01 A3 reverse check: hand-written" +
			" examples/ssobff/main.go was detected as assembly-generated —" +
			" isGocellAssemblyGenerated must not match hand-written mains")
	}

	// Verify the shape detector rejects a nil info (safety check).
	if slogSetDefaultShape(nil, &ast.CallExpr{}, "") {
		t.Errorf("SLOG-HANDLER-SEALED-FUNNEL-01 A3 shape detector: slogSetDefaultShape" +
			" with nil info must return false")
	}
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
// examples/ that imports runtime/bootstrap must be covered — either it contains
// an assembly-generated main.go (covered by the A3 generated segment) or it is
// listed in slogHandwrittenEntryPoints. This prevents an entirely new assembly
// from being wired up without a seal.
//
// Exemption: a package whose main.go carries the `gocell generate assembly`
// marker is covered structurally by the generated segment (the seal is in the
// generated run()), so it does NOT need a handwritten-allowlist entry. Only
// hand-written bootstrap entry points must be enrolled in the allowlist.
//
// Exclusion: cmd/gocell does not import runtime/bootstrap (CLI tool, not a
// long-running service) — naturally excluded by the bootstrap-import filter.
//
// Blind spot: the trigger filter uses p.Pkg.Imports() (DIRECT imports only), so a
// hand-written entry point that reaches runtime/bootstrap only TRANSITIVELY (via a
// wrapper package) would not be flagged. In practice every bootstrap entry point
// imports it directly (it calls bootstrap.Run / bootstrap.New), so this is a
// Go-language Medium ceiling, not a practical gap. Folded into the #1424 won't-do.
//
// AI-robust rating: generated coverage is Hard (marker-derived, self-covering);
// the residual hand-written allowlist is Medium (gh #1424).
func TestSlogHandlerSealedFunnel_A3_EntryPointsCoverage(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("A3 entry-point coverage: read module path: %v", err)
	}
	bootstrapPkgPath := modPath + "/runtime/bootstrap"

	// Build a lookup set of hand-written packages already in the allowlist.
	handwrittenPkgs := make(map[string]bool) // pkgPattern → true
	for _, ep := range slogHandwrittenEntryPoints {
		handwrittenPkgs[ep.pkgPattern] = true
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

			// Exempt packages whose entry main.go is assembly-generated — covered
			// by the A3 generated segment (seal lives in the generated run()).
			for _, f := range p.Files {
				if strings.HasSuffix(filepath.ToSlash(p.Rel(f)), "_test.go") {
					continue
				}
				if isGocellAssemblyEntryMain(f) {
					return nil
				}
			}

			// Derive the ./relative/pkg pattern form (strip module path prefix).
			relPkg := strings.TrimPrefix(pkgPath, modPath+"/")
			pkgPattern := "./" + relPkg

			if handwrittenPkgs[pkgPattern] {
				return nil // hand-written entry already enrolled — OK
			}

			// Hand-written package importing bootstrap but not enrolled: flag it.
			for _, f := range p.Files {
				if strings.HasSuffix(filepath.ToSlash(p.Rel(f)), "_test.go") {
					continue
				}
				pos := p.Fset.Position(f.Pos())
				ds = append(ds, Diagnostic{
					Rel:  filepath.ToSlash(p.Rel(f)),
					Line: pos.Line,
					Message: "package " + pkgPattern + " imports runtime/bootstrap" +
						" with a hand-written main (no `gocell generate assembly` marker)" +
						" but has NO entry in slogHandwrittenEntryPoints" +
						" — seal it with logging.NewHandler and enroll it;" +
						" SLOG-HANDLER-SEALED-FUNNEL-01 A3 reverse coverage (gh #1424)",
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
// enumeration of LogValuer implementations — a separate upgrade from the A3
// seal codegen, not delivered by the #1401 work).
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
