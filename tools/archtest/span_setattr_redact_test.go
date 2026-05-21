// invariants:
//   - INVARIANT: SPAN-SETATTR-REDACT-01
//
// SPAN-SETATTR-REDACT-01 — every string-valued span attribute in
// adapters/otel/ is funneled through pkg/redaction.RedactString +
// TruncateString(attrValueMaxLen). Hardcoded fail-closed redaction has no
// caller-side opt-out, mirroring the sibling SPAN-RECORD-ERROR-REDACT-01
// rule on span.RecordError.
//
// Four-assertion AI-Hard double-locked funnel (per .claude/rules/gocell/ai-collab.md
// 范本目录 "single sanctioned holder" + "typed marker funnel"):
//
//	A1 (Holder uniqueness — upstream Hard): any struct in adapters/otel/ that
//	   holds an oteltrace.Span field must be otelSpan. A rogue type holding
//	   the OTel span pointer could call s.inner.SetAttributes(...) directly,
//	   bypassing attrToKeyValue.
//
//	A2 (Callsite uniqueness — downstream Hard): every attribute.String(_, _)
//	   callsite in adapters/otel/span.go must appear inside the body of
//	   safeStringAttr or safeBytesAttr. attribute.String is the OTel SDK's
//	   sole constructor for string-typed attribute.KeyValue; locking its
//	   callsite set seals the funnel exit.
//
//	A3 (safeStringAttr form): safeStringAttr's body holds exactly one return
//	   statement structurally matching
//	   `attribute.String(<key>, redaction.TruncateString(redaction.RedactString(<raw>), attrValueMaxLen))`.
//	   Order (Redact then Truncate) is correctness-critical — if Truncate ran
//	   first, a sensitive value sitting past the cap would have its tail
//	   leaked unmasked.
//
//	A4 (safeBytesAttr form): safeBytesAttr's body holds exactly one return
//	   statement structurally matching
//	   `attribute.String(<key>, redactedBytesValue(<b>))`.
//	   Preserves the existing SHA256+length metadata path for binary payloads
//	   (debugging value the regex-based RedactString does not provide).
//
// Detection is pure AST (no go/types) — scope is one file + four fixture
// subdirectories. Import aliases for pkg/redaction and go.opentelemetry.io/otel/trace
// are resolved from each file's ImportSpec list.
//
// ref: tools/archtest/span_record_error_redact_test.go (sibling INVARIANT)
// ref: .claude/rules/gocell/observability.md "Span Attribute Redaction"
// ref: .claude/rules/gocell/ai-collab.md §"Hard 范本目录"
package archtest

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

const (
	otelTraceImportPath = `"go.opentelemetry.io/otel/trace"`

	// sanctioned helper names — single source for span.go and fixtures.
	safeStringHelper = "safeStringAttr"
	safeBytesHelper  = "safeBytesAttr"

	// attrValueMaxLen is the production constant name; archtest A3 asserts it
	// appears as the third argument to redaction.TruncateString.
	attrValueMaxLenIdent = "attrValueMaxLen"

	// redactedBytesValue is the existing SHA256+length helper in span.go;
	// archtest A4 asserts it remains the bytes-branch redactor.
	redactedBytesValueIdent = "redactedBytesValue"

	// attribute.X SDK constructor names used by AST matchers.
	attributeStringSel = "String"

	// pkg/redaction selector names used by AST matchers.
	redactStringSel   = "RedactString"
	truncateStringSel = "TruncateString"
)

const (
	violA1RogueHolder = "struct holds oteltrace.Span field but is not otelSpan" +
		" — bypass of SetAttributes funnel (SPAN-SETATTR-REDACT-01 A1)"
	violA2CallsiteEscape = "attribute.String(...) callsite outside safeStringAttr/safeBytesAttr" +
		" — bypass of redaction funnel (SPAN-SETATTR-REDACT-01 A2)"
	violA3StringForm = "safeStringAttr return expr must be" +
		" attribute.String(key, redaction.TruncateString(redaction.RedactString(raw), attrValueMaxLen))" +
		" — Redact MUST precede Truncate (SPAN-SETATTR-REDACT-01 A3)"
	violA4BytesForm = "safeBytesAttr return expr must be" +
		" attribute.String(key, redactedBytesValue(b)) (SPAN-SETATTR-REDACT-01 A4)"
)

// spanSetAttrScanDirs lists the directories scanned for SPAN-SETATTR-REDACT-01.
// The rule is intentionally narrow: only adapters/otel/ owns the span wrapper.
// New directories should be added here only if they themselves construct
// attribute.KeyValue from user-derived data (which they should not — span
// attribute construction is meant to be funneled through this single adapter).
var spanSetAttrScanDirs = []string{"adapters/otel"}

// spanSetAttrSpanGoRel is the single file enforced by A2/A3/A4 (callsite
// locality + helper form). A1 scans the entire directory.
const spanSetAttrSpanGoRel = "adapters/otel/span.go"

// otelTraceLocalName returns the local identifier used in file to refer to
// the go.opentelemetry.io/otel/trace package (default "trace" for an unnamed
// import; alias otherwise). Returns "" when the file does not import the
// package at all.
func otelTraceLocalName(file *ast.File) string {
	for _, imp := range file.Imports {
		if imp.Path == nil || imp.Path.Value != otelTraceImportPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "trace"
	}
	return ""
}

// fieldTypeIsOtelSpan reports whether fieldType is a SelectorExpr matching
// `<otelTraceLocal>.Span`. Pointer/non-pointer agnostic — both
// `oteltrace.Span` and `*oteltrace.Span` qualify since either form is a
// holder of the SDK span and can call s.inner.SetAttributes(...) directly.
func fieldTypeIsOtelSpan(fieldType ast.Expr, otelTraceLocal string) bool {
	if otelTraceLocal == "" {
		return false
	}
	switch t := fieldType.(type) {
	case *ast.SelectorExpr:
		return selectorMatches(t, otelTraceLocal, "Span")
	case *ast.StarExpr:
		sel, ok := t.X.(*ast.SelectorExpr)
		return ok && selectorMatches(sel, otelTraceLocal, "Span")
	default:
		return false
	}
}

// selectorMatches reports whether sel is `<xName>.<selName>` with X an
// *ast.Ident.
func selectorMatches(sel *ast.SelectorExpr, xName, selName string) bool {
	if sel == nil || sel.Sel == nil || sel.Sel.Name != selName {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return x.Name == xName
}

// collectFuncBodyRanges returns the body Pos/End pairs of every FuncDecl in
// file whose Name matches one of allowedNames. Pairs are flat: even indices
// are start positions, odd indices are end positions. Mirrors
// collectRecordErrorImplRanges in span_record_error_redact_test.go.
func collectFuncBodyRanges(file *ast.File, allowedNames ...string) []token.Pos {
	allowed := make(map[string]struct{}, len(allowedNames))
	for _, n := range allowedNames {
		allowed[n] = struct{}{}
	}
	var ranges []token.Pos
	EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Body == nil || fn.Name == nil {
			return
		}
		if _, ok := allowed[fn.Name.Name]; !ok {
			return
		}
		ranges = append(ranges, fn.Body.Lbrace, fn.Body.Rbrace)
	})
	return ranges
}

// onlyReturn returns the single *ast.ReturnStmt at depth-1 of fn.Body.
// Returns nil when the body has zero or more than one top-level return
// statement (we expect each sanctioned helper to be a one-liner) — nested
// returns in conditionals are not tolerated either, as they imply a
// redaction branch that A3/A4 cannot reason about.
//
// Uses EachInChildren (depth=1) per SCANNER-FRAMEWORK-USAGE-01: for-range
// over fn.Body.List + type assertion is the banned shape; EachInChildren
// is the typed-call replacement.
func onlyReturn(fn *ast.FuncDecl) *ast.ReturnStmt {
	if fn.Body == nil {
		return nil
	}
	var found *ast.ReturnStmt
	tooMany := false
	EachInChildren[ast.ReturnStmt](fn.Body, func(ret *ast.ReturnStmt) {
		if found != nil {
			tooMany = true
			return
		}
		found = ret
	})
	if tooMany {
		return nil
	}
	return found
}

// callMatches reports whether call is `<xName>.<selName>(...)` with X an
// *ast.Ident. Used to identify attribute.String / redaction.RedactString /
// redaction.TruncateString.
func callMatches(call *ast.CallExpr, xName, selName string) bool {
	if call == nil {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return selectorMatches(sel, xName, selName)
}

// identCallMatches reports whether call is `<name>(...)` (unqualified
// identifier call). Used to identify the in-package redactedBytesValue
// helper.
func identCallMatches(call *ast.CallExpr, name string) bool {
	if call == nil {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == name
}

// stringAttrReturnShape validates A3: the return expression of safeStringAttr
// must be
//
//	attribute.String(<Ident>, <redactionLocal>.TruncateString(<redactionLocal>.RedactString(<Ident>), attrValueMaxLen))
//
// otelAttrLocal is the local name of go.opentelemetry.io/otel/attribute (for
// the outer call); redactionLocal is the local name of pkg/redaction (for
// the inner two). Returns true iff the shape matches exactly.
func stringAttrReturnShape(ret *ast.ReturnStmt, otelAttrLocal, redactionLocal string) bool {
	if ret == nil || len(ret.Results) != 1 {
		return false
	}
	outer, ok := ret.Results[0].(*ast.CallExpr)
	if !ok || len(outer.Args) != 2 {
		return false
	}
	if !callMatches(outer, otelAttrLocal, attributeStringSel) {
		return false
	}
	// arg[0]: bare Ident (key)
	if _, ok := outer.Args[0].(*ast.Ident); !ok {
		return false
	}
	// arg[1]: redaction.TruncateString(redaction.RedactString(<Ident>), attrValueMaxLen)
	truncate, ok := outer.Args[1].(*ast.CallExpr)
	if !ok || !callMatches(truncate, redactionLocal, truncateStringSel) || len(truncate.Args) != 2 {
		return false
	}
	redact, ok := truncate.Args[0].(*ast.CallExpr)
	if !ok || !callMatches(redact, redactionLocal, redactStringSel) || len(redact.Args) != 1 {
		return false
	}
	if _, ok := redact.Args[0].(*ast.Ident); !ok {
		return false
	}
	capIdent, ok := truncate.Args[1].(*ast.Ident)
	if !ok || capIdent.Name != attrValueMaxLenIdent {
		return false
	}
	return true
}

// bytesAttrReturnShape validates A4: the return expression of safeBytesAttr
// must be
//
//	attribute.String(<Ident>, redactedBytesValue(<Ident>))
//
// Single in-package call to redactedBytesValue (unqualified).
func bytesAttrReturnShape(ret *ast.ReturnStmt, otelAttrLocal string) bool {
	if ret == nil || len(ret.Results) != 1 {
		return false
	}
	outer, ok := ret.Results[0].(*ast.CallExpr)
	if !ok || len(outer.Args) != 2 {
		return false
	}
	if !callMatches(outer, otelAttrLocal, attributeStringSel) {
		return false
	}
	if _, ok := outer.Args[0].(*ast.Ident); !ok {
		return false
	}
	inner, ok := outer.Args[1].(*ast.CallExpr)
	if !ok || !identCallMatches(inner, redactedBytesValueIdent) || len(inner.Args) != 1 {
		return false
	}
	if _, ok := inner.Args[0].(*ast.Ident); !ok {
		return false
	}
	return true
}

// otelAttributeLocalName returns the local identifier for
// go.opentelemetry.io/otel/attribute (default "attribute" for unnamed
// imports; alias otherwise). Returns "" when the file does not import it.
func otelAttributeLocalName(file *ast.File) string {
	const importPath = `"go.opentelemetry.io/otel/attribute"`
	for _, imp := range file.Imports {
		if imp.Path == nil || imp.Path.Value != importPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "attribute"
	}
	return ""
}

// scanSpanSetAttrDirDiags emits SPAN-SETATTR-REDACT-01 diagnostics for the
// directory rooted at dir. Used both by production enforcement
// (TestSpanSetAttrRedacted scanning adapters/otel) and by fixture scans
// (TestSpanSetAttrRedactedFixtures via golden compare).
//
// IncludeTestdata is applied only when the dir path itself contains a
// "testdata" segment; the option requires that — applying it
// unconditionally rejects ModuleScope and non-testdata DirsScope at
// scope construction time.
func scanSpanSetAttrDirDiags(t *testing.T, root, dir string, spanGoBaseNames ...string) []Diagnostic {
	t.Helper()
	opts := []ScopeOption{IncludeGenerated()}
	if strings.Contains(filepath.ToSlash(dir), "/testdata/") {
		opts = append(opts, IncludeTestdata())
	}
	scope := DirsScope(root, []string{dir}, opts...)
	// spanGoBaseNames lets fixture mode point at the fixture file (e.g.
	// "span.go" basename inside the fixture dir) while production scan
	// uses the canonical "span.go" relative path.
	matchSpan := func(rel string) bool {
		if len(spanGoBaseNames) == 0 {
			return rel == spanSetAttrSpanGoRel
		}
		base := filepath.Base(rel)
		for _, want := range spanGoBaseNames {
			if base == want {
				return true
			}
		}
		return false
	}

	return Run(t, scope, func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, file := range p.Files {
			otelTraceLocal := otelTraceLocalName(file)
			otelAttrLocal := otelAttributeLocalName(file)
			redactionLocal := redactionLocalName(file)
			rel := filepath.ToSlash(p.Rel(file))
			isSpanGo := matchSpan(rel)

			ds = append(ds, checkA1(p, file, otelTraceLocal)...)

			if !isSpanGo {
				continue
			}
			ds = append(ds, checkA2(p, file, otelAttrLocal)...)
			ds = append(ds, checkA3(p, file, otelAttrLocal, redactionLocal)...)
			ds = append(ds, checkA4(p, file, otelAttrLocal)...)
		}
		return ds
	})
}

// checkA1 — holder uniqueness. Any struct field of type oteltrace.Span (or
// pointer thereto) must belong to the otelSpan struct.
func checkA1(p *Pass, file *ast.File, otelTraceLocal string) []Diagnostic {
	if otelTraceLocal == "" {
		return nil
	}
	var ds []Diagnostic
	EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return
		}
		for _, field := range st.Fields.List {
			if !fieldTypeIsOtelSpan(field.Type, otelTraceLocal) {
				continue
			}
			if ts.Name != nil && ts.Name.Name == "otelSpan" {
				continue
			}
			pos := p.Fset.Position(field.Pos())
			ds = append(ds, Diagnostic{
				Rel:     filepath.ToSlash(p.Rel(file)),
				Line:    pos.Line,
				Message: violA1RogueHolder,
			})
		}
	})
	return ds
}

// checkA2 — callsite uniqueness. Every attribute.String(_, _) call in span.go
// must sit inside the body of safeStringAttr or safeBytesAttr.
func checkA2(p *Pass, file *ast.File, otelAttrLocal string) []Diagnostic {
	if otelAttrLocal == "" {
		return nil
	}
	helperRanges := collectFuncBodyRanges(file, safeStringHelper, safeBytesHelper)
	var ds []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !callMatches(call, otelAttrLocal, attributeStringSel) {
			return
		}
		if posInRanges(call.Pos(), helperRanges) {
			return
		}
		pos := p.Fset.Position(call.Pos())
		ds = append(ds, Diagnostic{
			Rel:     filepath.ToSlash(p.Rel(file)),
			Line:    pos.Line,
			Message: violA2CallsiteEscape,
		})
	})
	return ds
}

// checkA3 — safeStringAttr return-expr form.
func checkA3(p *Pass, file *ast.File, otelAttrLocal, redactionLocal string) []Diagnostic {
	fn, ok := findTopLevelFuncDecl(file, safeStringHelper)
	if !ok {
		// Missing helper is itself a violation — flag once at the file's
		// package position so the diagnostic is attributable.
		pos := p.Fset.Position(file.Package)
		return []Diagnostic{{
			Rel:     filepath.ToSlash(p.Rel(file)),
			Line:    pos.Line,
			Message: violA3StringForm,
		}}
	}
	ret := onlyReturn(fn)
	if !stringAttrReturnShape(ret, otelAttrLocal, redactionLocal) {
		line := p.Fset.Position(fn.Pos()).Line
		if ret != nil {
			line = p.Fset.Position(ret.Pos()).Line
		}
		return []Diagnostic{{
			Rel:     filepath.ToSlash(p.Rel(file)),
			Line:    line,
			Message: violA3StringForm,
		}}
	}
	return nil
}

// checkA4 — safeBytesAttr return-expr form.
func checkA4(p *Pass, file *ast.File, otelAttrLocal string) []Diagnostic {
	fn, ok := findTopLevelFuncDecl(file, safeBytesHelper)
	if !ok {
		pos := p.Fset.Position(file.Package)
		return []Diagnostic{{
			Rel:     filepath.ToSlash(p.Rel(file)),
			Line:    pos.Line,
			Message: violA4BytesForm,
		}}
	}
	ret := onlyReturn(fn)
	if !bytesAttrReturnShape(ret, otelAttrLocal) {
		line := p.Fset.Position(fn.Pos()).Line
		if ret != nil {
			line = p.Fset.Position(ret.Pos()).Line
		}
		return []Diagnostic{{
			Rel:     filepath.ToSlash(p.Rel(file)),
			Line:    line,
			Message: violA4BytesForm,
		}}
	}
	return nil
}

// TestSpanSetAttrRedacted enforces SPAN-SETATTR-REDACT-01 across adapters/otel.
func TestSpanSetAttrRedacted(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	var all []Diagnostic
	for _, dir := range spanSetAttrScanDirs {
		all = append(all, scanSpanSetAttrDirDiags(t, root, dir)...)
	}
	Report(t, "SPAN-SETATTR-REDACT-01", all)
}

// TestSpanSetAttrRedactedFixtures verifies the AST scanner via static
// regression fixtures. Each fixture dir owns a diag.golden capturing the
// rule's real output; GREEN fixtures have empty golden, REDs capture each
// assertion's specific diagnostic.
func TestSpanSetAttrRedactedFixtures(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	baseRel := "tools/archtest/testdata/span_setattr_redact_fixtures"
	base := filepath.Join(root, "tools", "archtest", "testdata", "span_setattr_redact_fixtures")

	dirs := []string{
		"compliant",
		"violates_string_order",
		"violates_default_attribute_string",
		"violates_rogue_holder",
		"violates_bytes_form",
	}

	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			got := scanSpanSetAttrDirDiags(t, root, baseRel+"/"+dir, "span.go")
			AssertGolden(t, filepath.Join(base, dir, "diag.golden"), got)
		})
	}
}
