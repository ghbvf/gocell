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
// Hard收口 + archtest closure (three legs; see the per-test godoc for ratings):
//
//	type seal (compile-time Hard): contractgen.httpEndpointSpec is unexported.
//	  ContractGenSpec.Endpoint is *httpEndpointSpec, so no out-of-package code
//	  can construct a non-nil Endpoint → cannot drive handler.tmpl with a
//	  hand-built (FMT-34-unvalidated) spec. This is the load-bearing Hard
//	  upgrade #690 calls the "sealed interface / 单一抽象函数" path.
//
//	A1a — the seal is locked: ContractGenSpec.Endpoint's base type is the
//	      unexported sealed name, and no exported alias re-exports it.
//	A1b — within contractgen (Go has no intra-package visibility), the sealed
//	      type is constructed only inside buildHTTPEndpointSpec, and
//	      buildHTTPEndpointSpec is called only from the allowlisted funnel.
//	A2  — across all of tools/codegen/**, the only source file emitting an
//	      http.Handler implementation (ServeHTTP method) is handler.tmpl.
//	A3  — handler.tmpl is rendered only inside the contractgen package, whose
//	      only HTTP-spec construction path is buildHTTPEndpointSpec.
//
// AI-robust ceiling (honest): the type seal is Hard for the cross-package
// bypass. A1b/A2/A3 cover what the Go type system fundamentally cannot — a
// second intra-package constructor (no package-internal visibility) and text
// emission of an http.Handler (a generated string is not type-checkable). No
// lower-cost Hard path exists for those, so there is no follow-up Hard-ization
// backlog; the archtest is the ceiling. (ai-robust.md §Review checklist:
// "Medium 且无低成本升 Hard 路径 → 保留".)

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
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
)

// codegenSpecCtorCallerAllowlist names the production functions permitted to
// CALL buildHTTPEndpointSpec (A1b ②). buildHTTPSpec is the single funnel that
// assigns the result to ContractGenSpec.Endpoint. (Test files call it directly
// for unit assertions; they are out of scope — production-only walk.)
var codegenSpecCtorCallerAllowlist = map[string]string{
	"buildHTTPSpec": "sole funnel: assigns buildHTTPEndpointSpec result to ContractGenSpec.Endpoint",
}

// codegenHandlerEmitMarker matches the http.Handler interface-method emission
// `func (h *Handler) ServeHTTP(<param> http.ResponseWriter, ...)` as it appears
// in handler.tmpl. The marker is the language-mandated shape of an http.Handler
// implementation (a ServeHTTP method taking http.ResponseWriter is required by
// the http.Handler interface) — not an arbitrary string convention. Param name
// is matched as \w+ so it is not pinned to the literal "w".
var codegenHandlerEmitMarker = regexp.MustCompile(`ServeHTTP\(\s*\w+\s+http\.ResponseWriter`)

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
// ---------------------------------------------------------------------------
func TestCodegenBuildHTTPEndpointSpecSoleCaller_A1a_SpecTypeSealed(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"tools/codegen/contractgen"})

	var violations []string
	_ = Run(t, scope, func(p *Pass) []Diagnostic {
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
// Blind spots (documented, not detected):
//   - Method-value / variable indirection: `f := buildHTTPEndpointSpec; f()`
//     or a package-scope construction/call outside any FuncDecl. buildHTTP-
//     EndpointSpec is an unexported package func returning (*T, error); the
//     only realistic form is a direct call inside a function body.
//   - reflect-built values: AST-invisible; irrelevant for literal codegen.
// ---------------------------------------------------------------------------
func TestCodegenBuildHTTPEndpointSpecSoleCaller_A1b_SoleConstructorAndCaller(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"tools/codegen/contractgen"})

	var violations []string
	_ = Run(t, scope, func(p *Pass) []Diagnostic {
		var typeFound, ctorFound bool
		for _, f := range p.Files {
			if hasTypeSpecNamed(f, codegenSealedSpecType) {
				typeFound = true
			}
			if hasFuncDeclNamed(f, codegenSpecCtorFunc) {
				ctorFound = true
			}
			violations = append(violations, scanSealedSpecConstructionAndCaller(f, p.Rel(f))...)
		}
		if !typeFound {
			violations = append(violations,
				"sealed spec type "+codegenSealedSpecType+" not found in contractgen — renamed without updating this lock?")
		}
		if !ctorFound {
			violations = append(violations,
				"constructor "+codegenSpecCtorFunc+" not found in contractgen — renamed without updating this lock?")
		}
		return nil
	})

	reportCodegenFunnel(t, violations)
}

// ---------------------------------------------------------------------------
// A2 — http.Handler emit-template uniqueness across all of tools/codegen/**.
//
// AI-robust: Medium (deny-by-default content scan). Text emission of an
// http.Handler is not type-checkable, so a content scan for the
// language-mandated ServeHTTP signature is the ceiling tool — not a Soft
// string-anchor convention. The found-set == {handler.tmpl} assertion also
// proves .tmpl files are walked (handler.tmpl is itself a .tmpl).
//
// Blind spots (documented): a handler emitted with a split / printf-built
// ServeHTTP signature, an http.HandlerFunc-only handler with no ServeHTTP
// method, a template in a non-(.tmpl|.go) extension, or re-rendering
// handler.tmpl by os.ReadFile path (caught instead by A3).
// ---------------------------------------------------------------------------
func TestCodegenBuildHTTPEndpointSpecSoleCaller_A2_HandlerEmitTemplateUniqueness(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"tools/codegen"},
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
	if !equalStringSlices(emitFiles, want) {
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
// FMT-34-unvalidated spec. Banning the "handler.tmpl" template-name literal
// outside contractgen closes that. A positive sanity (≥1 occurrence inside
// contractgen) prevents a vacuous pass if the template were renamed/removed.
//
// Blind spot (documented): the template name reached via a const/variable
// rather than a string literal, or a computed path.
// ---------------------------------------------------------------------------
func TestCodegenBuildHTTPEndpointSpecSoleCaller_A3_HandlerTmplRenderedOnlyInContractgen(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	outsideScope := DirsScope(root, []string{"tools/codegen"},
		MatchRels(func(rel string) bool {
			if strings.Contains(rel, "/testdata/") || strings.HasSuffix(rel, "_test.go") {
				return false
			}
			if !strings.HasSuffix(rel, ".go") {
				return false
			}
			return !strings.HasPrefix(rel, codegenContractgenPrefix)
		}),
	)
	var outside []string
	_ = Run(t, outsideScope, func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			for _, line := range scanHandlerTmplLiteralLines(f, p.Fset) {
				outside = append(outside, p.Rel(f)+":"+line)
			}
		}
		return nil
	})
	for _, v := range outside {
		t.Errorf("CODEGEN-BUILDHTTPENDPOINTSPEC-SOLE-CALLER-01 (A3): %s references template %q outside "+
			"the contractgen package — handler.tmpl must be rendered only by the buildHTTPEndpointSpec funnel.",
			v, codegenHandlerTemplate)
	}

	// Positive sanity: the funnel target must exist inside contractgen, else
	// the guard above passes vacuously.
	insideScope := DirsScope(root, []string{"tools/codegen/contractgen"},
		MatchRels(func(rel string) bool {
			return strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go")
		}),
	)
	var insideCount int
	_ = Run(t, insideScope, func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			insideCount += len(scanHandlerTmplLiteralLines(f, p.Fset))
		}
		return nil
	})
	if insideCount == 0 {
		t.Errorf("CODEGEN-BUILDHTTPENDPOINTSPEC-SOLE-CALLER-01 (A3): template %q is never rendered inside "+
			"contractgen — A3 would pass vacuously; update the rule if the template was renamed/removed.",
			codegenHandlerTemplate)
	}
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

// scanSealedSpecConstructionAndCaller flags, in file f:
//   - a composite literal of the sealed type outside buildHTTPEndpointSpec, and
//   - a call to buildHTTPEndpointSpec from a function not in the allowlist.
func scanSealedSpecConstructionAndCaller(f *ast.File, rel string) []string {
	var out []string
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Body == nil || fd.Name == nil {
			return
		}
		fn := fd.Name.Name
		EachInSubtree[ast.CompositeLit](fd.Body, func(cl *ast.CompositeLit) {
			if id := exprToIdent(cl.Type); id != nil && id.Name == codegenSealedSpecType &&
				fn != codegenSpecCtorFunc {
				out = append(out, rel+": "+fn+" constructs "+codegenSealedSpecType+
					" outside "+codegenSpecCtorFunc+" — the sealed spec has a single sanctioned constructor")
			}
		})
		EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
			id := exprToIdent(call.Fun)
			if id == nil || id.Name != codegenSpecCtorFunc {
				return
			}
			if fn == codegenSpecCtorFunc {
				return // self (no recursion in production, defensive)
			}
			if _, allow := codegenSpecCtorCallerAllowlist[fn]; allow {
				return
			}
			out = append(out, rel+": "+fn+" calls "+codegenSpecCtorFunc+
				" outside the funnel — only "+allowlistKeys(codegenSpecCtorCallerAllowlist)+" may call it")
		})
	})
	return out
}

// scanHandlerTmplLiteralLines returns the line numbers in f where a string
// literal equal to "handler.tmpl" appears.
func scanHandlerTmplLiteralLines(f *ast.File, fset *token.FileSet) []string {
	var lines []string
	EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
		if lit.Kind != token.STRING {
			return
		}
		if v, ok := StringLitValue(lit); ok && v == codegenHandlerTemplate {
			lines = append(lines, itoa(fset.Position(lit.Pos()).Line))
		}
	})
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

func hasFuncDeclNamed(f *ast.File, name string) bool {
	found := false
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Recv == nil && fd.Name != nil && fd.Name.Name == name {
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

// itoa avoids importing strconv solely for line numbers in diagnostics.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
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

func parseCodegenFixtureSrc(t *testing.T, src string) (*ast.File, *token.FileSet) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return f, fset
}

func TestCodegenFunnel_A1a_DetectsExportedEndpointType(t *testing.T) {
	t.Parallel()
	// RED fixture: Endpoint typed with an EXPORTED spec type.
	f, _ := parseCodegenFixtureSrc(t, "package contractgen\n"+
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
	g, _ := parseCodegenFixtureSrc(t, "package contractgen\n"+
		"type httpEndpointSpec struct{}\n"+
		"type ContractGenSpec struct { Endpoint *httpEndpointSpec }\n")
	gb, _ := contractGenSpecEndpointBaseType(g)
	if gb != codegenSealedSpecType {
		t.Fatalf("sealed fixture base = %q, want %q", gb, codegenSealedSpecType)
	}
}

func TestCodegenFunnel_A1a_DetectsExportedReExportAlias(t *testing.T) {
	t.Parallel()
	f, _ := parseCodegenFixtureSrc(t, "package contractgen\n"+
		"type httpEndpointSpec struct{}\n"+
		"type PublicEndpointSpec = httpEndpointSpec\n")
	if got := scanExportedSealedAlias(f); len(got) == 0 {
		t.Fatal("detector missed exported alias re-export of the sealed type")
	}
	// Negative: an unexported alias is harmless (not a cross-package re-export).
	g, _ := parseCodegenFixtureSrc(t, "package contractgen\n"+
		"type httpEndpointSpec struct{}\n"+
		"type localAlias = httpEndpointSpec\n")
	if got := scanExportedSealedAlias(g); len(got) != 0 {
		t.Fatalf("detector over-flagged unexported alias: %v", got)
	}
}

func TestCodegenFunnel_A1b_DetectsRogueConstructionAndCaller(t *testing.T) {
	t.Parallel()
	// RED: construction outside the constructor + a call from a non-funnel func.
	f, _ := parseCodegenFixtureSrc(t, "package contractgen\n"+
		"type httpEndpointSpec struct{}\n"+
		"func buildHTTPEndpointSpec() *httpEndpointSpec { return &httpEndpointSpec{} }\n"+
		"func rogue() *httpEndpointSpec { return &httpEndpointSpec{} }\n"+
		"func notTheFunnel() { _, _ = buildHTTPEndpointSpec(), 0 }\n")
	got := scanSealedSpecConstructionAndCaller(f, "fixture.go")
	if !containsSubstr(got, "rogue constructs httpEndpointSpec") {
		t.Errorf("missed rogue construction; got %v", got)
	}
	if !containsSubstr(got, "notTheFunnel calls buildHTTPEndpointSpec") {
		t.Errorf("missed rogue caller; got %v", got)
	}
	// GREEN: construction inside the constructor + call from the allowlisted funnel.
	g, _ := parseCodegenFixtureSrc(t, "package contractgen\n"+
		"type httpEndpointSpec struct{}\n"+
		"func buildHTTPEndpointSpec() *httpEndpointSpec { return &httpEndpointSpec{} }\n"+
		"func buildHTTPSpec() { _, _ = buildHTTPEndpointSpec(), 0 }\n")
	if got := scanSealedSpecConstructionAndCaller(g, "fixture.go"); len(got) != 0 {
		t.Errorf("over-flagged sanctioned construction/caller: %v", got)
	}
}

func TestCodegenFunnel_A2_MarkerDiscriminates(t *testing.T) {
	t.Parallel()
	// Positive: the canonical http.Handler emission (any param name).
	for _, s := range []string{
		"func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {",
		"func (h *Handler) ServeHTTP(rw http.ResponseWriter, req *http.Request) {",
	} {
		if !codegenHandlerEmitMarker.MatchString(s) {
			t.Errorf("marker missed http.Handler emission: %q", s)
		}
	}
	// Negative: prose / type references that are NOT a ServeHTTP method def.
	for _, s := range []string{
		"// Renders the http.Handler that decodes the request",
		"bootstrapAuth func(http.Handler) http.Handler",
		"Handler: http.HandlerFunc(h.handle),",
	} {
		if codegenHandlerEmitMarker.MatchString(s) {
			t.Errorf("marker over-matched non-emission text: %q", s)
		}
	}
}

func TestCodegenFunnel_A3_DetectsHandlerTmplLiteral(t *testing.T) {
	t.Parallel()
	f, fset := parseCodegenFixtureSrc(t, "package cellgen\n"+
		"func render() { _ = \"handler.tmpl\" }\n")
	if got := scanHandlerTmplLiteralLines(f, fset); len(got) != 1 {
		t.Fatalf("detector found %d handler.tmpl literals, want 1", len(got))
	}
	// Negative: a different template name is not flagged.
	g, gfset := parseCodegenFixtureSrc(t, "package cellgen\nfunc render() { _ = \"cell.tmpl\" }\n")
	if got := scanHandlerTmplLiteralLines(g, gfset); len(got) != 0 {
		t.Fatalf("detector over-flagged non-handler template: %v", got)
	}
}

func containsSubstr(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}
