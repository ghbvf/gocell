//go:build archtest

package archtest

// INVARIANT: CODEGEN-BUILDHTTPENDPOINTSPEC-SOLE-CALLER-01
//
// Every HTTP-handler-emitting codegen path under tools/codegen/** MUST flow
// through the contractgen funnel buildHTTPEndpointSpec → handler.tmpl. This
// archtest is the downstream/breadth half of the FMT-34 upstream funnel
// (kernel/governance/rules_fmt.go::validateFMT34): FMT-34's auth-on-internal
// guard validateAuthOnInternalPath runs unconditionally inside
// buildHTTPEndpointSpec, so if a second generator (cellgen / markergen / a
// future one) emitted an http.Handler without going through that funnel,
// FMT-34's "sole HTTP codegen entry" premise would silently degrade.
//
// Hard收口 + archtest closure (four legs; see the per-test godoc for ratings):
//
//	type seal (compile-time Hard): contractgen.httpEndpointSpec is unexported.
//	  ContractGenSpec.Endpoint is *httpEndpointSpec, so no out-of-package code
//	  can construct a non-nil Endpoint. The spec is also never handed to
//	  another package as a mutable value: its sole constructor buildContractSpec
//	  and every render wrapper are package-private, and the exported surface
//	  (Generate / RenderContractArtifacts) returns rendered []byte, never the
//	  spec. So neither constructing nor post-construction mutating (an exported
//	  field flipped to AuthPublic:true after FMT-34 ran) an unvalidated Endpoint
//	  to drive handler.tmpl is expressible cross-package. This is the
//	  load-bearing Hard upgrade #690 calls the "sealed interface / 单一抽象函数" path.
//
//	A1a — the seal is locked: ContractGenSpec.Endpoint's base type is the
//	      unexported sealed name, and no exported alias re-exports it.
//	A1b — within contractgen (Go has no intra-package visibility), the sealed
//	      type is constructed only inside buildHTTPEndpointSpec, and
//	      buildHTTPEndpointSpec is called only from the allowlisted funnel.
//	A2  — across all of tools/codegen/**, the only source file emitting an
//	      http.Handler implementation (ServeHTTP method / HandlerFunc adapter,
//	      any import alias) is handler.tmpl.
//	A3  — handler.tmpl is rendered only inside the contractgen package, whose
//	      only HTTP-spec construction path is buildHTTPEndpointSpec.
//	A4  — no exported contractgen function signature names ContractGenSpec or
//	      httpEndpointSpec (param or result), so no out-of-package code can
//	      obtain a mutable spec to flip Endpoint.Path/AuthPublic/Clients. This
//	      is the cross-package "cannot mutate" reverse proof complementing A1a's
//	      "cannot construct"; it locks buildContractSpec + render wrappers
//	      staying package-private (re-exporting either re-introduces a spec in
//	      an exported signature → A4 RED).
//
// AI-robust ceiling (honest): the type seal + the package-private build/render
// surface make BOTH cross-package construction (A1a) and cross-package mutation
// (A4) unexpressible — these are compile-time/visibility Hard facts, archtest-
// locked against regression. A1b/A2/A3 cover what the Go type system
// fundamentally cannot — a second intra-package constructor (no package-internal
// visibility) and text emission of an http.Handler (a generated string is not
// type-checkable). No lower-cost Hard path exists for those, so there is no
// follow-up Hard-ization backlog; the archtest is the ceiling. (ai-robust.md
// §Review checklist: "Medium 且无低成本升 Hard 路径 → 保留".)

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	// codegenSealedSpecType is the unexported name of the HTTP endpoint spec
	// struct in package contractgen. Renaming the sealed holder must update
	// this constant together with A1a's re-export check — the name is the lock.
	codegenSealedSpecType = "httpEndpointSpec"
	// codegenSpecCtorFunc is the sole constructor of codegenSealedSpecType and
	// the FMT-34 funnel entry; "SOLE-CALLER" in the rule ID refers to it.
	codegenSpecCtorFunc = "buildHTTPEndpointSpec"
	// codegenHandlerTemplate is the only template that emits an http.Handler.
	codegenHandlerTemplate = "handler.tmpl"
	// codegenHandlerTmplRel is the one allowlisted file permitted to carry the
	// http.Handler-emit marker (A2).
	codegenHandlerTmplRel = "tools/codegen/contractgen/templates/handler.tmpl"
	// codegenContractgenPrefix is the contractgen package subtree.
	codegenContractgenPrefix = "tools/codegen/contractgen/"
	// codegenSpecHolderType is the exported outer IR struct whose Endpoint field
	// holds the sealed *httpEndpointSpec. An exported function naming it (A4)
	// would hand a mutable spec to another package, re-opening the
	// post-construction mutation bypass.
	codegenSpecHolderType = "ContractGenSpec"
)

// codegenLeakedSpecTypes are the package-local IR types whose exported fields
// reach FMT-34-validated endpoint state. A4 bans any EXPORTED contractgen
// function from naming them in a parameter or result: doing so would hand a
// mutable spec (or its sealed Endpoint) to another package, letting a holder
// flip Path / AuthPublic / Clients after buildHTTPEndpointSpec already ran
// validateAuthOnInternalPath. The only sanctioned exported surface (Generate /
// RenderContractArtifacts) trades in rendered []byte, never these types.
var codegenLeakedSpecTypes = map[string]bool{
	codegenSpecHolderType: true, // "ContractGenSpec" — has the mutable Endpoint field
	codegenSealedSpecType: true, // "httpEndpointSpec" — the sealed endpoint itself
}

// codegenSpecCtorCallerAllowlist names the production functions permitted to
// CALL buildHTTPEndpointSpec (A1b ②). buildHTTPSpec is the single funnel that
// assigns the result to ContractGenSpec.Endpoint. (Test files call it directly
// for unit assertions; they are out of scope — production-only walk.)
var codegenSpecCtorCallerAllowlist = map[string]string{
	"buildHTTPSpec": "sole funnel: assigns buildHTTPEndpointSpec result to ContractGenSpec.Endpoint",
}

// codegenHandlerEmitMarker matches the two language-mandated shapes by which
// Go source produces an http.Handler:
//
//   - a ServeHTTP method `func (h *Handler) ServeHTTP(<param> <q.>ResponseWriter, ...)`
//     (the http.Handler interface method), and
//   - the <q.>HandlerFunc(<fn>) adapter (an http.Handler without a ServeHTTP
//     method of its own).
//
// Both appear in handler.tmpl today; a new generator could emit a handler via
// EITHER form, so A2 must cover both — the ServeHTTP-only marker would miss a
// HandlerFunc-only emitter (reviewer B-1). These are structural shapes of the
// regulated artifact, not arbitrary string conventions.
//
// Alias / unnamed-param tolerant (PR #904 review F2): the net/http import
// qualifier is matched as an optional `(?:\w+\.)?` so an alias import
// (`stdhttp.ResponseWriter`, `nethttp.HandlerFunc(`) or a dot-import (bare
// `ResponseWriter` / `HandlerFunc(`) cannot slip a handler past the scan; and
// the ServeHTTP param name is optional `(?:\w+\s+)?` so an unnamed-param method
// (`ServeHTTP(http.ResponseWriter, ...)`) is still caught. The `ServeHTTP\(`
// literal prefix is retained so it does not over-match the non-handler
// `visitXxxResponse(ctx, w http.ResponseWriter)` methods emitted by types.tmpl.
var codegenHandlerEmitMarker = regexp.MustCompile(`ServeHTTP\(\s*(?:\w+\s+)?(?:\w+\.)?ResponseWriter|(?:\w+\.)?HandlerFunc\(`)

// ---------------------------------------------------------------------------
// A1a — the type seal is in place and locked (no re-export).
//
// AI-robust: this test asserts the compile-time Hard fact (sealed unexported
// type as the Endpoint field type) plus the re-export must-check
// (ai-robust.md §"JSON-wire-decode struct sealing": 任意名 re-export 必查点).
// Before the seal lands this test is RED — ContractGenSpec.Endpoint is the
// exported *HTTPEndpointSpec — which drives the Wave-1 RED → Wave-2 GREEN flip.
//
// Blind spots (Go type system / AST ceiling; documented, not detected here):
//   - A definition `type Exported httpEndpointSpec` (NOT an alias) creates a
//     distinct type that is NOT assignable to ContractGenSpec.Endpoint, so it
//     does not re-open THIS funnel; only the alias form is banned.
//   - Reflection-built endpoint values are AST-invisible — irrelevant for a
//     codegen package that constructs structs literally.
//
// ---------------------------------------------------------------------------
func TestCodegenBuildHTTPEndpointSpecSoleCaller_A1a_SpecTypeSealed(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"tools/codegen/contractgen"})

	var violations []string
	_ = Run(t, AST(scope), func(p *Pass) []Diagnostic {
		var endpointFound bool
		var endpointBase string
		for _, f := range p.Files {
			if base, ok := contractGenSpecEndpointBaseType(f); ok {
				endpointFound = true
				endpointBase = base
			}
			violations = append(violations, scanExportedSealedAlias(f)...)
		}
		if !endpointFound {
			violations = append(violations,
				"ContractGenSpec.Endpoint field not found in contractgen — has the spec type been renamed?")
			return nil
		}
		if endpointBase != codegenSealedSpecType {
			violations = append(violations, "ContractGenSpec.Endpoint base type is "+endpointBase+
				"; must be the sealed unexported type "+codegenSealedSpecType+
				" (exported spec types let any package hand-build an FMT-34-unvalidated Endpoint)")
		}
		return nil
	})

	reportCodegenFunnel(t, violations)
}

// ---------------------------------------------------------------------------
// A1b — sole constructor + sole caller, within the contractgen package.
//
// AI-robust: Medium. Go has no intra-package visibility, so a second
// constructor of the (unexported) sealed type or a non-funnel caller is not
// compile-rejectable; this archtest is the ceiling. Production-only walk
// (Tests=false) so unit tests that call buildHTTPEndpointSpec directly are out
// of scope by construction.
//
// Detection is by token-position containment (not enclosing-FuncDecl name), so
// constructions / calls inside function literals, package-level var
// initializers, or init() — not just top-level FuncDecls — are all covered
// (reviewer B-2). Both construction forms are detected: httpEndpointSpec{}
// composite literals AND new(httpEndpointSpec) (reviewer A-1).
//
// Blind spots (documented, not detected):
//   - Method-value indirection: `f := buildHTTPEndpointSpec; f()` — the call
//     site spells the variable, not the func name. buildHTTPEndpointSpec is an
//     unexported package func returning (*T, error); a direct call is the only
//     realistic form. Asserted absent in production by
//     TestCodegenFunnel_A1b_NoMethodValueIndirectionInProduction.
//   - reflect-built values: AST-invisible; irrelevant for literal codegen.
//
// ---------------------------------------------------------------------------
func TestCodegenBuildHTTPEndpointSpecSoleCaller_A1b_SoleConstructorAndCaller(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"tools/codegen/contractgen"})

	var violations []string
	_ = Run(t, AST(scope), func(p *Pass) []Diagnostic {
		ctorBody, callerBodies, typeFound, ctorFound := collectA1bRanges(p.Files)
		if !typeFound {
			violations = append(violations,
				"sealed spec type "+codegenSealedSpecType+" not found in contractgen — renamed without updating this lock?")
		}
		if !ctorFound {
			violations = append(violations,
				"constructor "+codegenSpecCtorFunc+" not found in contractgen — renamed without updating this lock?")
			return nil
		}
		for _, f := range p.Files {
			violations = append(violations, scanSealedSpecViolations(f, p.Rel(f), ctorBody, callerBodies)...)
		}
		return nil
	})

	reportCodegenFunnel(t, violations)
}

// ---------------------------------------------------------------------------
// A2 — http.Handler emit-template uniqueness across all of tools/codegen/**.
//
// AI-robust: Medium (deny-by-default content scan). Text emission of an
// http.Handler is not type-checkable, so a content scan for the two
// language-mandated handler shapes (ServeHTTP method + http.HandlerFunc
// adapter) is the ceiling tool — not a Soft string-anchor convention. The
// found-set == {handler.tmpl} assertion also proves .tmpl files are walked
// (handler.tmpl is itself a .tmpl).
//
// Blind spots (documented): a ServeHTTP signature split / printf-built so the
// contiguous marker text never appears; a template in a non-(.tmpl|.go)
// extension; or re-rendering handler.tmpl by an os.ReadFile path (that form is
// caught by A3, which bans the "handler.tmpl" literal outside contractgen).
// A handler emitted as a raw struct implementing http.Handler without ever
// writing the ServeHTTP-with-http.ResponseWriter signature contiguously is the
// residual text-emission ceiling.
// ---------------------------------------------------------------------------
func TestCodegenBuildHTTPEndpointSpecSoleCaller_A2_HandlerEmitTemplateUniqueness(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(
		root, []string{"tools/codegen"},
		MatchRels(func(rel string) bool {
			if strings.Contains(rel, "/testdata/") {
				return false // golden snapshots are codegen output, not generator source
			}
			if strings.HasSuffix(rel, "_test.go") {
				return false
			}
			return strings.HasSuffix(rel, ".tmpl") || strings.HasSuffix(rel, ".go")
		}),
	)

	var emitFiles []string
	EachContentFile(t, scope, []string{".tmpl", ".go"}, func(_ *testing.T, fc ContentContext) {
		if codegenHandlerEmitMarker.Match(fc.Bytes) {
			emitFiles = append(emitFiles, fc.Rel)
		}
	})
	sort.Strings(emitFiles)

	want := []string{codegenHandlerTmplRel}
	if !slices.Equal(emitFiles, want) {
		t.Errorf("CODEGEN-BUILDHTTPENDPOINTSPEC-SOLE-CALLER-01 (A2): files emitting an http.Handler "+
			"(ServeHTTP method) under tools/codegen/** = %v; want exactly %v. A new HTTP-handler-emitting "+
			"generator path must route through buildHTTPEndpointSpec (contractgen handler.tmpl), not a "+
			"second template/string.", emitFiles, want)
	}
}

// ---------------------------------------------------------------------------
// A3 — handler.tmpl is rendered only inside the contractgen package.
//
// AI-robust: Medium. Complements A2's emit-side check with the render side:
// even though A2 guarantees handler.tmpl is the only http.Handler emitter, an
// out-of-package generator could re-render it (by path) with a hand-built,
// FMT-34-unvalidated spec. Banning a reference to the "handler.tmpl"
// template-name constant outside contractgen closes that. A positive sanity
// (≥1 occurrence inside contractgen) prevents a vacuous pass if the template
// were renamed/removed.
//
// Typed const evaluation (PR #904 review F3): the scan resolves each
// BasicLit / Ident / SelectorExpr / BinaryExpr through go/types constant
// folding (EvaluateConstString), so a const indirection
// (`const tmpl = "handler.tmpl"`) or string concatenation
// (`"handler" + ".tmpl"`) folds to the same value and is caught — the prior
// BasicLit-only scan missed both. Run(t, Typed(...)) (not the AST-only Run)
// supplies the types.Info the folding needs.
//
// Blind spot (documented, not detected): the template name assembled from
// runtime-only data (e.g. filepath.Join of a value read from disk) — no
// compile-time constant exists to fold. A computed-but-const path still folds
// and is caught.
// ---------------------------------------------------------------------------
func TestCodegenBuildHTTPEndpointSpecSoleCaller_A3_HandlerTmplRenderedOnlyInContractgen(t *testing.T) {
	t.Parallel()

	var outside []string
	var insideCount int
	_ = Run(t, Typed(TypedOpts{}, []string{"./tools/codegen/..."}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		for _, f := range p.Files {
			rel := p.Rel(f)
			if strings.Contains(rel, "/testdata/") || strings.HasSuffix(rel, "_test.go") || !strings.HasSuffix(rel, ".go") {
				continue
			}
			lines := scanHandlerTmplConstExprLines(f, p.TypesInfo, p.Fset)
			if strings.HasPrefix(rel, codegenContractgenPrefix) {
				insideCount += len(lines)
				continue
			}
			for _, ln := range lines {
				outside = append(outside, rel+":"+strconv.Itoa(ln))
			}
		}
		return nil
	})

	sort.Strings(outside)
	for _, v := range outside {
		t.Errorf("CODEGEN-BUILDHTTPENDPOINTSPEC-SOLE-CALLER-01 (A3): %s references template %q outside "+
			"the contractgen package — handler.tmpl must be rendered only by the buildHTTPEndpointSpec funnel.",
			v, codegenHandlerTemplate)
	}

	if insideCount == 0 {
		t.Errorf("CODEGEN-BUILDHTTPENDPOINTSPEC-SOLE-CALLER-01 (A3): template %q is never referenced inside "+
			"contractgen — A3 would pass vacuously; update the rule if the template was renamed/removed.",
			codegenHandlerTemplate)
	}
}

// ---------------------------------------------------------------------------
// A4 — no exported contractgen API hands out the mutable spec.
//
// AI-robust: Hard (compile-time/visibility fact, archtest-locked against
// regression). A1a proves out-of-package code cannot CONSTRUCT a non-nil
// Endpoint (httpEndpointSpec unexported). But exported fields on a held
// *httpEndpointSpec / *ContractGenSpec are mutable, so a holder could flip
// Path / AuthPublic / Clients AFTER buildHTTPEndpointSpec ran FMT-34 and then
// drive handler.tmpl with the mutated spec. That mutation path is closed by
// the holder being unreachable cross-package: the only producer
// (buildContractSpec) and consumers (render wrappers) are package-private, and
// the exported surface returns rendered []byte. A4 locks that — it fails if
// any EXPORTED contractgen function names ContractGenSpec or httpEndpointSpec
// in a parameter or result (re-exporting buildContractSpec or a render wrapper
// is exactly such a signature). This is the cross-package "cannot mutate"
// reverse proof the #690 review asked for.
//
// Scope notes:
//   - Receiver types are NOT scanned (a method ON the sealed type, e.g.
//     httpEndpointSpec.IsPagination, is in-package and fine); only Params +
//     Results are walked, recursively, so []*ContractGenSpec / map / func-typed
//     nestings are caught too.
//   - Production-only walk (non-_test.go): white-box tests legitimately hold
//     the spec.
//
// Blind spot (documented, not detected): an exported method whose RECEIVER is
// an exported type and that returns the spec via an interface the caller can
// type-assert. No exported contractgen type carries the spec today; adding one
// would itself be an unusual API change. Asserted clean by the production walk.
// ---------------------------------------------------------------------------
func TestCodegenBuildHTTPEndpointSpecSoleCaller_A4_NoExportedSpecLeak(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(
		root, []string{"tools/codegen/contractgen"},
		MatchRels(func(rel string) bool {
			return strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go")
		}),
	)
	var violations []string
	_ = Run(t, AST(scope), func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			violations = append(violations, scanExportedSpecLeak(f, p.Rel(f))...)
		}
		return nil
	})

	reportCodegenFunnel(t, violations)
}

// ===========================================================================
// Detection helpers (shared by production scans above and the reverse
// self-checks below). Each operates on a single *ast.File so the same logic
// runs over Pass.Files and over inline-parsed fixture sources.
// ===========================================================================

// contractGenSpecEndpointBaseType returns the base type name of the Endpoint
// field of the ContractGenSpec struct in f (dereferencing one *), and ok=true
// if that struct/field is present in f.
func contractGenSpecEndpointBaseType(f *ast.File) (string, bool) {
	var base string
	var ok bool
	EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
		if ts.Name == nil || ts.Name.Name != "ContractGenSpec" {
			return
		}
		st, isStruct := ts.Type.(*ast.StructType)
		if !isStruct || st.Fields == nil {
			return
		}
		for _, field := range st.Fields.List {
			if !fieldHasName(field, "Endpoint") {
				continue
			}
			base = starExprBaseIdentName(field.Type)
			ok = true
		}
	})
	return base, ok
}

// scanExportedSealedAlias flags an exported type alias `type X = httpEndpointSpec`
// (the re-export form that re-opens cross-package construction).
func scanExportedSealedAlias(f *ast.File) []string {
	var out []string
	EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
		if ts.Name == nil || !ts.Assign.IsValid() || !ts.Name.IsExported() {
			return
		}
		if id := exprToIdent(ts.Type); id != nil && id.Name == codegenSealedSpecType {
			out = append(out, "exported alias "+ts.Name.Name+" = "+codegenSealedSpecType+
				" re-exports the sealed spec type — drop it (it re-opens cross-package construction)")
		}
	})
	return out
}

// scanExportedSpecLeak flags an EXPORTED package-level function (A4) whose
// parameter or result type names a codegenLeakedSpecTypes member. Receiver
// types are deliberately NOT scanned — a method on the sealed type
// (httpEndpointSpec.IsPagination) is in-package and harmless. The Params /
// Results field types are walked recursively, so nested forms
// ([]*ContractGenSpec, func() *httpEndpointSpec, …) are caught too.
func scanExportedSpecLeak(f *ast.File, rel string) []string {
	var out []string
	EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Name == nil || !fd.Name.IsExported() || fd.Type == nil {
			return
		}
		for _, grp := range []*ast.FieldList{fd.Type.Params, fd.Type.Results} {
			if grp == nil {
				continue
			}
			for _, field := range grp.List {
				for _, name := range leakedSpecIdentsIn(field.Type) {
					out = append(out, rel+": exported func "+fd.Name.Name+
						" names IR type "+name+" in its signature — the build product must not "+
						"escape contractgen (hands out a mutable spec → re-opens post-construction "+
						"Endpoint mutation); keep buildContractSpec / render wrappers package-private")
				}
			}
		}
	})
	return out
}

// leakedSpecIdentsIn returns the distinct codegenLeakedSpecTypes identifiers
// appearing anywhere in the type expression expr (root included).
func leakedSpecIdentsIn(expr ast.Expr) []string {
	seen := map[string]bool{}
	var out []string
	EachInSubtree[ast.Ident](expr, func(id *ast.Ident) {
		if codegenLeakedSpecTypes[id.Name] && !seen[id.Name] {
			seen[id.Name] = true
			out = append(out, id.Name)
		}
	})
	return out
}

// posRange is a half-open-inclusive token.Pos span [lo, hi] of a function body.
type posRange struct{ lo, hi token.Pos }

func (r posRange) contains(p token.Pos) bool { return r.lo <= p && p <= r.hi }

func anyContains(rs []posRange, p token.Pos) bool {
	for _, r := range rs {
		if r.contains(p) {
			return true
		}
	}
	return false
}

// collectA1bRanges scans every file for the sole-constructor body
// (buildHTTPEndpointSpec) and the allowlisted-caller bodies, returning their
// token.Pos spans plus whether the sealed type and the constructor were found.
// Position spans (not enclosing-FuncDecl name) let scanSealedSpecViolations
// catch constructions/calls inside function literals, var initializers, and
// init() — anywhere the FuncDecl-name approach would miss.
func collectA1bRanges(files []*ast.File) (ctor posRange, callers []posRange, typeFound, ctorFound bool) {
	for _, f := range files {
		if hasTypeSpecNamed(f, codegenSealedSpecType) {
			typeFound = true
		}
		EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
			if fd.Recv != nil || fd.Name == nil || fd.Body == nil {
				return
			}
			if fd.Name.Name == codegenSpecCtorFunc {
				ctorFound = true
				ctor = posRange{fd.Body.Pos(), fd.Body.End()}
			}
			if _, ok := codegenSpecCtorCallerAllowlist[fd.Name.Name]; ok {
				callers = append(callers, posRange{fd.Body.Pos(), fd.Body.End()})
			}
		})
	}
	return ctor, callers, typeFound, ctorFound
}

// scanSealedSpecViolations flags, anywhere in f (by token position):
//   - a httpEndpointSpec{} composite literal or new(httpEndpointSpec) outside
//     the sole constructor body, and
//   - a call to buildHTTPEndpointSpec outside every allowlisted caller body.
func scanSealedSpecViolations(f *ast.File, rel string, ctor posRange, callers []posRange) []string {
	var out []string
	EachInSubtree[ast.CompositeLit](f, func(cl *ast.CompositeLit) {
		if id := exprToIdent(cl.Type); id != nil && id.Name == codegenSealedSpecType && !ctor.contains(cl.Pos()) {
			out = append(out, rel+": "+codegenSealedSpecType+
				"{} constructed outside "+codegenSpecCtorFunc+" — single sanctioned constructor")
		}
	})
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		switch {
		case isNewOfSealedSpec(call) && !ctor.contains(call.Pos()):
			out = append(out, rel+": new("+codegenSealedSpecType+
				") outside "+codegenSpecCtorFunc+" — single sanctioned constructor")
		case isCallTo(call, codegenSpecCtorFunc) && !anyContains(callers, call.Pos()):
			out = append(out, rel+": "+codegenSpecCtorFunc+
				" called outside the funnel — only "+allowlistKeys(codegenSpecCtorCallerAllowlist)+" may call it")
		}
	})
	return out
}

// isNewOfSealedSpec reports whether call is new(httpEndpointSpec).
func isNewOfSealedSpec(call *ast.CallExpr) bool {
	if id := exprToIdent(call.Fun); id == nil || id.Name != "new" || len(call.Args) != 1 {
		return false
	}
	arg := exprToIdent(call.Args[0])
	return arg != nil && arg.Name == codegenSealedSpecType
}

// isCallTo reports whether call invokes the bare package-level function name.
func isCallTo(call *ast.CallExpr, name string) bool {
	id := exprToIdent(call.Fun)
	return id != nil && id.Name == name
}

// scanHandlerTmplConstExprLines returns the (deduped, sorted) line numbers in f
// where a compile-time constant string expression folds to "handler.tmpl". It
// walks BasicLit / Ident / SelectorExpr / BinaryExpr and resolves each via
// go/types constant folding (EvaluateConstString), so a literal, a const-ident
// reference, a package-qualified const, and a "+" concatenation are all caught
// (A3, PR #904 F3). info MUST come from the same typed load that produced f.
func scanHandlerTmplConstExprLines(f *ast.File, info *types.Info, fset *token.FileSet) []int {
	seen := map[int]bool{}
	var lines []int
	record := func(expr ast.Expr) {
		if v, ok := EvaluateConstString(info, expr); ok && v == codegenHandlerTemplate {
			ln := fset.Position(expr.Pos()).Line
			if !seen[ln] {
				seen[ln] = true
				lines = append(lines, ln)
			}
		}
	}
	EachInSubtree[ast.BasicLit](f, func(n *ast.BasicLit) { record(n) })
	EachInSubtree[ast.Ident](f, func(n *ast.Ident) { record(n) })
	EachInSubtree[ast.SelectorExpr](f, func(n *ast.SelectorExpr) { record(n) })
	EachInSubtree[ast.BinaryExpr](f, func(n *ast.BinaryExpr) { record(n) })
	sort.Ints(lines)
	return lines
}

// ---- small AST utilities (single type assertions, no range-over-AST-slice) --

func fieldHasName(field *ast.Field, name string) bool {
	for _, n := range field.Names {
		if n != nil && n.Name == name {
			return true
		}
	}
	return false
}

func starExprBaseIdentName(expr ast.Expr) string {
	if se, ok := expr.(*ast.StarExpr); ok {
		if id := exprToIdent(se.X); id != nil {
			return id.Name
		}
		return ""
	}
	if id := exprToIdent(expr); id != nil {
		return id.Name
	}
	return ""
}

func hasTypeSpecNamed(f *ast.File, name string) bool {
	found := false
	EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
		if ts.Name != nil && ts.Name.Name == name {
			found = true
		}
	})
	return found
}

func allowlistKeys(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

func reportCodegenFunnel(t *testing.T, violations []string) {
	t.Helper()
	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("CODEGEN-BUILDHTTPENDPOINTSPEC-SOLE-CALLER-01: %s", v)
	}
}

// ===========================================================================
// Reverse self-checks — prove the detectors fire on planted violations
// (ai-robust.md §"工具选定后强制盲区自检": detection must be demonstrated, not
// assumed). Inline-parsed fixtures keep the proofs hermetic.
// ===========================================================================

func parseCodegenFixtureSrc(t *testing.T, src string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return f
}

// typeCheckCodegenFixture parses + type-checks an inline (import-free) fixture
// so EvaluateConstString can fold const idents / concatenations in the A3
// reverse self-check. The fixtures need no imports, so importer.Default is
// never actually invoked.
func typeCheckCodegenFixture(t *testing.T, src string) (*ast.File, *types.Info, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Defs:  map[*ast.Ident]types.Object{},
		Uses:  map[*ast.Ident]types.Object{},
	}
	conf := types.Config{Importer: importer.Default()}
	if _, err := conf.Check("codegenfixture", fset, []*ast.File{f}, info); err != nil {
		t.Fatalf("type-check fixture: %v", err)
	}
	return f, info, fset
}

func TestCodegenFunnel_A1a_DetectsExportedEndpointType(t *testing.T) {
	t.Parallel()
	// RED fixture: Endpoint typed with an EXPORTED spec type.
	f := parseCodegenFixtureSrc(t, "package contractgen\n"+
		"type HTTPEndpointSpec struct{}\n"+
		"type ContractGenSpec struct { Endpoint *HTTPEndpointSpec }\n")
	base, ok := contractGenSpecEndpointBaseType(f)
	if !ok {
		t.Fatal("detector failed to locate ContractGenSpec.Endpoint")
	}
	if base != "HTTPEndpointSpec" {
		t.Fatalf("base type = %q, want HTTPEndpointSpec", base)
	}
	if base == codegenSealedSpecType {
		t.Fatal("exported type must not equal the sealed unexported name (would mask the violation)")
	}
	// GREEN fixture: sealed unexported type passes.
	g := parseCodegenFixtureSrc(t, "package contractgen\n"+
		"type httpEndpointSpec struct{}\n"+
		"type ContractGenSpec struct { Endpoint *httpEndpointSpec }\n")
	gb, _ := contractGenSpecEndpointBaseType(g)
	if gb != codegenSealedSpecType {
		t.Fatalf("sealed fixture base = %q, want %q", gb, codegenSealedSpecType)
	}
}

func TestCodegenFunnel_A1a_DetectsExportedReExportAlias(t *testing.T) {
	t.Parallel()
	f := parseCodegenFixtureSrc(t, "package contractgen\n"+
		"type httpEndpointSpec struct{}\n"+
		"type PublicEndpointSpec = httpEndpointSpec\n")
	if got := scanExportedSealedAlias(f); len(got) == 0 {
		t.Fatal("detector missed exported alias re-export of the sealed type")
	}
	// Negative: an unexported alias is harmless (not a cross-package re-export).
	g := parseCodegenFixtureSrc(t, "package contractgen\n"+
		"type httpEndpointSpec struct{}\n"+
		"type localAlias = httpEndpointSpec\n")
	if got := scanExportedSealedAlias(g); len(got) != 0 {
		t.Fatalf("detector over-flagged unexported alias: %v", got)
	}
}

func TestCodegenFunnel_A4_DetectsExportedSpecLeak(t *testing.T) {
	t.Parallel()
	// RED: exported funcs that hand out the mutable spec — by result, by param,
	// the sealed endpoint directly, and a nested []*ContractGenSpec form.
	f := parseCodegenFixtureSrc(t, "package contractgen\n"+
		"type ContractGenSpec struct{}\n"+
		"type httpEndpointSpec struct{}\n"+
		"func BuildLeak() *ContractGenSpec { return nil }\n"+
		"func RenderLeak(s *ContractGenSpec) {}\n"+
		"func EndpointLeak() *httpEndpointSpec { return nil }\n"+
		"func SliceLeak() []*ContractGenSpec { return nil }\n")
	got := scanExportedSpecLeak(f, "fixture.go")
	for _, want := range []string{
		"exported func BuildLeak names IR type ContractGenSpec",
		"exported func RenderLeak names IR type ContractGenSpec",
		"exported func EndpointLeak names IR type httpEndpointSpec",
		"exported func SliceLeak names IR type ContractGenSpec",
	} {
		if !strings.Contains(strings.Join(got, "\n"), want) {
			t.Errorf("A4 detector missed %q; got %v", want, got)
		}
	}
	// GREEN: unexported producer/consumers, an exported method ON the sealed
	// type (receiver not scanned), and an exported func trading in []byte only.
	g := parseCodegenFixtureSrc(t, "package contractgen\n"+
		"type ContractGenSpec struct{}\n"+
		"type httpEndpointSpec struct{}\n"+
		"func buildContractSpec() *ContractGenSpec { return nil }\n"+
		"func renderHandler(s *ContractGenSpec) ([]byte, error) { return nil, nil }\n"+
		"func (e *httpEndpointSpec) IsPagination() bool { return false }\n"+
		"func Generate() ([]byte, error) { return nil, nil }\n")
	if got := scanExportedSpecLeak(g, "fixture.go"); len(got) != 0 {
		t.Fatalf("A4 over-flagged sanctioned package-private / []byte surface: %v", got)
	}
}

func scanA1bFixture(f *ast.File) []string {
	ctor, callers, _, _ := collectA1bRanges([]*ast.File{f})
	return scanSealedSpecViolations(f, "fixture.go", ctor, callers)
}

func TestCodegenFunnel_A1b_DetectsRogueConstructionAndCaller(t *testing.T) {
	t.Parallel()
	// RED: rogue composite-literal + new() construction + a package-level var
	// function literal calling the ctor outside the funnel (the position-based
	// scan catches the func-literal that an enclosing-FuncDecl walk would miss).
	f := parseCodegenFixtureSrc(t, "package contractgen\n"+
		"type httpEndpointSpec struct{}\n"+
		"func buildHTTPEndpointSpec() *httpEndpointSpec { return &httpEndpointSpec{} }\n"+
		"func rogueLit() *httpEndpointSpec { return &httpEndpointSpec{} }\n"+
		"func rogueNew() *httpEndpointSpec { return new(httpEndpointSpec) }\n"+
		"var rogueVar = func() { _, _ = buildHTTPEndpointSpec(), 0 }\n")
	got := scanA1bFixture(f)
	joined := strings.Join(got, "\n")
	for _, want := range []string{
		codegenSealedSpecType + "{} constructed outside",
		"new(" + codegenSealedSpecType + ") outside",
		codegenSpecCtorFunc + " called outside the funnel",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("A1b detector missed %q; got %v", want, got)
		}
	}
	// GREEN: composite literal + new() inside the ctor, ctor call inside the
	// allowlisted funnel (incl. a nested func literal still within buildHTTPSpec).
	g := parseCodegenFixtureSrc(t, "package contractgen\n"+
		"type httpEndpointSpec struct{}\n"+
		"func buildHTTPEndpointSpec() *httpEndpointSpec { _ = new(httpEndpointSpec); return &httpEndpointSpec{} }\n"+
		"func buildHTTPSpec() { run := func() { _, _ = buildHTTPEndpointSpec(), 0 }; run() }\n")
	if got := scanA1bFixture(g); len(got) != 0 {
		t.Errorf("A1b over-flagged sanctioned construction/caller: %v", got)
	}
}

// TestCodegenFunnel_A1b_NoMethodValueIndirectionInProduction asserts the one
// documented A1b blind spot — method-value indirection `f := buildHTTPEndpointSpec`
// — does not occur in contractgen production source, so the gap is documented
// AND verified empty (ai-robust.md §盲区自检).
func TestCodegenFunnel_A1b_NoMethodValueIndirectionInProduction(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"tools/codegen/contractgen"})
	var hits []string
	_ = Run(t, AST(scope), func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			rel := p.Rel(f)

			calleeIdents := map[*ast.Ident]bool{}
			EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
				if id := exprToIdent(call.Fun); id != nil {
					calleeIdents[id] = true
				}
			})
			EachInSubtree[ast.Ident](f, func(id *ast.Ident) {
				if id.Name == codegenSpecCtorFunc && !calleeIdents[id] {
					hits = append(hits, rel+": value reference to "+codegenSpecCtorFunc)
				}
			})
		}
		return nil
	})

	// The decl of buildHTTPEndpointSpec is itself an Ident occurrence that is not
	// a callee; tolerate exactly the declaration, flag any additional value ref.
	if len(hits) > 1 {
		t.Errorf("A1b blind spot exploited: method-value reference(s) to %s in production: %v",
			codegenSpecCtorFunc, hits)
	}
}

func TestCodegenFunnel_A2_MarkerDiscriminates(t *testing.T) {
	t.Parallel()
	// Positive: both http.Handler emission shapes (ServeHTTP method, any param
	// name; and the http.HandlerFunc adapter — the B-1 HandlerFunc-only path),
	// across the stdlib qualifier, an import alias, a dot-import (no qualifier),
	// and an unnamed ServeHTTP param (PR #904 F2 — alias/unnamed tolerance).
	for _, s := range []string{
		"func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {",
		"func (h *Handler) ServeHTTP(rw http.ResponseWriter, req *http.Request) {",
		"Handler: http.HandlerFunc(h.handle),",
		"mux.Handle(p, http.HandlerFunc(generatedHandler))",
		// alias import (the F2 bypass)
		"func (h *Handler) ServeHTTP(w stdhttp.ResponseWriter, r *stdhttp.Request) {",
		"Handler: stdhttp.HandlerFunc(h.handle),",
		"mux.Handle(p, nethttp.HandlerFunc(generatedHandler))",
		// unnamed ServeHTTP param
		"func (h *Handler) ServeHTTP(stdhttp.ResponseWriter, *stdhttp.Request) {",
		// dot-import (no qualifier)
		"func (h *Handler) ServeHTTP(w ResponseWriter, r *Request) {",
	} {
		if !codegenHandlerEmitMarker.MatchString(s) {
			t.Errorf("marker missed http.Handler emission: %q", s)
		}
	}
	// Negative: prose / type references that produce no http.Handler, plus the
	// types.tmpl visit method which carries http.ResponseWriter but is NOT a
	// ServeHTTP (the ServeHTTP\( prefix guards against that over-match).
	for _, s := range []string{
		"// Renders the http.Handler that decodes the request",
		"bootstrapAuth func(http.Handler) http.Handler",
		"var h http.Handler = next",
		"func (r Get200JSONResponse) visitGetResponse(ctx context.Context, w http.ResponseWriter) error {",
	} {
		if codegenHandlerEmitMarker.MatchString(s) {
			t.Errorf("marker over-matched non-emission text: %q", s)
		}
	}
}

func TestCodegenFunnel_A3_DetectsHandlerTmplConstExpr(t *testing.T) {
	t.Parallel()
	// Positive: a bare literal, a const-ident reference, and a "+" concatenation
	// all fold to "handler.tmpl" (PR #904 F3 — typed const eval closes the
	// const/concat blind spot the prior BasicLit-only scan missed).
	for _, src := range []string{
		"package cellgen\nfunc render() { _ = \"handler.tmpl\" }\n",
		"package cellgen\nconst tmpl = \"handler.tmpl\"\nfunc render() { _ = tmpl }\n",
		"package cellgen\nfunc render() { _ = \"handler\" + \".tmpl\" }\n",
	} {
		f, info, fset := typeCheckCodegenFixture(t, src)
		if got := scanHandlerTmplConstExprLines(f, info, fset); len(got) == 0 {
			t.Errorf("detector missed handler.tmpl const expr in:\n%s", src)
		}
	}
	// Negative: a different template name (const-folded) is not flagged.
	f, info, fset := typeCheckCodegenFixture(t, "package cellgen\nconst tmpl = \"cell.tmpl\"\nfunc render() { _ = tmpl }\n")
	if got := scanHandlerTmplConstExprLines(f, info, fset); len(got) != 0 {
		t.Fatalf("detector over-flagged non-handler template: %v", got)
	}
}
