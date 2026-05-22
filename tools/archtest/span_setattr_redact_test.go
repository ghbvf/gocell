// INVARIANT: SPAN-SETATTR-REDACT-01
//
// SPAN-SETATTR-REDACT-01 — every string-valued span attribute in
// adapters/otel/ is funneled through pkg/redaction.RedactString +
// TruncateString(attrValueMaxLen). Hardcoded fail-closed redaction has no
// caller-side opt-out, mirroring the sibling SPAN-RECORD-ERROR-REDACT-01
// rule on span.RecordError.
//
// Five-assertion AI-Hard double-locked funnel (per .claude/rules/gocell/ai-collab.md
// 范本目录 "single sanctioned holder" + "typed marker funnel"):
//
//	A1 (Holder uniqueness — upstream Hard): any struct in adapters/otel/ that
//	   holds an oteltrace.Span field must be otelSpan. A rogue type holding
//	   the OTel span pointer could call s.inner.SetAttributes(...) directly,
//	   bypassing attrToKeyValue.
//
//	A2 (Callsite uniqueness — downstream Hard): every attribute.String(_, _)
//	   call AND every attribute.Key(_).<X>(...) chain shape in
//	   adapters/otel/span.go must appear inside the body of safeStringAttr or
//	   safeBytesAttr. attribute.String and the .Key(_).<X> chain are the OTel
//	   SDK's two interchangeable string-attribute constructors; locking both
//	   callsite sets seals the funnel exit.
//
//	A3a (safeStringAttr key-branch form): body must start with
//	   `if redaction.IsSensitiveKey(<paramKey>) { return attribute.String(<paramKey>, redaction.Mask) }`.
//	   No init clause, no else, body has exactly one ReturnStmt. This is the
//	   structured-key bypass guard — it closes wrapper.Attr{Key: "password",
//	   Value: "hunter2"} (value is bare, no `password=` anchor for RedactString).
//
//	A3b (safeStringAttr free-form return): trailing return must structurally match
//	   `attribute.String(<paramKey>, redaction.TruncateString(redaction.RedactString(<paramRaw>), attrValueMaxLen))`.
//	   Argument identifiers are bound to FuncDecl formal parameter names —
//	   a swap edit like `attribute.String(raw, …RedactString(key)…)` satisfies
//	   the loose `*ast.Ident` check but fails identity binding. Order (Redact
//	   then Truncate) is correctness-critical: if Truncate ran first, a
//	   sensitive value sitting past the cap would have its tail leaked unmasked.
//
//	A4 (safeBytesAttr form): safeBytesAttr's body holds exactly one return
//	   statement structurally matching
//	   `attribute.String(<key>, redactedBytesValue(<b>))`.
//	   Preserves the existing SHA256+length metadata path for binary payloads
//	   (debugging value the regex-based RedactString does not provide).
//
// Detection is pure AST (no go/types) — scope is one file + seven fixture
// subdirectories. Import aliases for pkg/redaction and go.opentelemetry.io/otel/trace
// are resolved from each file's ImportSpec list.
//
// BLIND SPOTS (AST forms outside the coverage of SPAN-SETATTR-REDACT-01):
//
//   - A1 only checks struct field types; an oteltrace.Span stored as a local
//     var or returned from a function is invisible to A1.
//   - A2 covers attribute.String(...) bare calls AND attribute.Key(_).<X>(...)
//     chain shape (X ∈ String / StringSlice / StringValue); attribute.StringValue /
//     attribute.StringSlice bare calls and attribute.KeyValue{...} composite
//     literals are not covered. Reverse self-check test
//     TestSpanSetAttrRedacted_NoBlindspotsInProduction asserts the StringValue /
//     StringSlice bare calls and the chain shape do not appear in the
//     production adapters/otel tree.
//   - A3a tolerates extra statements between the key-branch IfStmt and the
//     trailing free-form return (e.g. logging) — only first and last
//     top-level statements are checked. Adding intermediate statements is
//     possible but unusual; if it happens, A3b still binds the trailing
//     return shape.
//   - A4 requires a single-return body; multi-statement bodies (tmp var +
//     return) intentionally fail — that is a feature, not a blind spot.
//   - Symbol matching is syntactic; a custom attribute package alias with a
//     String symbol would shadow the real one.
//
// Upstream package-internal upgrade path: backlog issue #851
// (SPAN-SETATTR-HOLDER-SEAL-01 — seal via unexported interface).
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
	redactStringSel    = "RedactString"
	truncateStringSel  = "TruncateString"
	isSensitiveKeySel  = "IsSensitiveKey"
	redactionMaskIdent = "Mask"

	// attribute.Key chain shape: the receiver-method form
	// `attribute.Key("k").String("v")` is an SDK-supported alternative to
	// `attribute.String(...)`. archtest A2 + blindspot self-check cover both.
	attributeKeyCtor = "Key"
)

const (
	violA1RogueHolder = "struct holds oteltrace.Span field but is not otelSpan" +
		" — bypass of SetAttributes funnel (SPAN-SETATTR-REDACT-01 A1)"
	violA2CallsiteEscape = "attribute.String(...) callsite outside safeStringAttr/safeBytesAttr" +
		" — bypass of redaction funnel (SPAN-SETATTR-REDACT-01 A2)"
	violA2ChainCallsite = "attribute.Key(_).<X>(...) chain shape outside safeStringAttr/safeBytesAttr" +
		" — bypass of redaction funnel (SPAN-SETATTR-REDACT-01 A2)"
	violA3aKeyBranchMissing = "safeStringAttr must start with" +
		" `if redaction.IsSensitiveKey(key) { return attribute.String(key, redaction.Mask) }`" +
		" — structured-key bypass guard required (SPAN-SETATTR-REDACT-01 A3a)"
	violA3bFreeformForm = "safeStringAttr trailing return expr must be" +
		" attribute.String(key, redaction.TruncateString(redaction.RedactString(raw), attrValueMaxLen))" +
		" — Redact MUST precede Truncate; arg identities must match formal params" +
		" (SPAN-SETATTR-REDACT-01 A3b)"
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
// statement (we expect single-statement helpers like safeBytesAttr).
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

// trailingReturn returns the last top-level statement of fn.Body if it is a
// *ast.ReturnStmt. Used by A3b to locate the free-form return that follows
// the A3a key-branch IfStmt in safeStringAttr's two-statement body.
func trailingReturn(fn *ast.FuncDecl) *ast.ReturnStmt {
	if fn == nil || fn.Body == nil || len(fn.Body.List) == 0 {
		return nil
	}
	ret, _ := fn.Body.List[len(fn.Body.List)-1].(*ast.ReturnStmt)
	return ret
}

// twoStringParamNames returns the first two parameter identifier names of fn,
// expanding grouped declarations like (key, raw string) so each name counts
// individually. Used by A3a/A3b to bind argument identifiers to formal-param
// identities (per ai-collab.md "Hard 范本目录" — identity binding is strictly
// stronger than the loose `*ast.Ident` type assertion an arg-position swap
// would otherwise satisfy).
//
// ok=false when fn has fewer than two named parameters.
func twoStringParamNames(fn *ast.FuncDecl) (first, second string, ok bool) {
	if fn == nil || fn.Type == nil || fn.Type.Params == nil {
		return "", "", false
	}
	var names []string
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			if name == nil {
				continue
			}
			names = append(names, name.Name)
			if len(names) == 2 {
				return names[0], names[1], true
			}
		}
	}
	return "", "", false
}

// isAttributeKeyChainStringCall reports whether call is the chain shape
// `<otelAttrLocal>.Key(_).<X>(...)` for X in {"String", "StringSlice",
// "StringValue"} — the SDK-supported alternative form to
// `<otelAttrLocal>.String(...)`. The outer SelectorExpr's X is a CallExpr,
// not an Ident, so callMatches/A2's bare-Ident check would miss this shape.
//
// This shape is a known blind spot of the original A2 check and is closed
// by checkA2 (in span.go scope) plus the production blindspot reverse
// self-check (adapters/otel tree scope).
func isAttributeKeyChainStringCall(call *ast.CallExpr, otelAttrLocal string) bool {
	if call == nil || otelAttrLocal == "" {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return false
	}
	switch sel.Sel.Name {
	case attributeStringSel, "StringSlice", "StringValue":
		// fall through
	default:
		return false
	}
	inner, ok := sel.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	return callMatches(inner, otelAttrLocal, attributeKeyCtor)
}

// keyBranchShape validates A3a: safeStringAttr body must start with
//
//	if <redactionLocal>.IsSensitiveKey(<paramKey>) {
//	    return <otelAttrLocal>.String(<paramKey>, <redactionLocal>.Mask)
//	}
//
// (no init clause, no else clause, body has exactly one ReturnStmt). This
// is the structured-key bypass guard that closes the leak where a caller
// passes wrapper.Attr{Key: "password", Value: "hunter2"} — value is bare,
// has no `password=` anchor, so RedactString alone would never fire.
func keyBranchShape(ifStmt *ast.IfStmt, otelAttrLocal, redactionLocal, paramKey string) bool {
	if ifStmt == nil || ifStmt.Init != nil || ifStmt.Else != nil || ifStmt.Body == nil {
		return false
	}
	cond, ok := ifStmt.Cond.(*ast.CallExpr)
	if !ok || !callMatches(cond, redactionLocal, isSensitiveKeySel) || len(cond.Args) != 1 {
		return false
	}
	keyArg, ok := cond.Args[0].(*ast.Ident)
	if !ok || keyArg.Name != paramKey {
		return false
	}
	if len(ifStmt.Body.List) != 1 {
		return false
	}
	ret, ok := ifStmt.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	outer, ok := ret.Results[0].(*ast.CallExpr)
	if !ok || !callMatches(outer, otelAttrLocal, attributeStringSel) || len(outer.Args) != 2 {
		return false
	}
	argKey, ok := outer.Args[0].(*ast.Ident)
	if !ok || argKey.Name != paramKey {
		return false
	}
	mask, ok := outer.Args[1].(*ast.SelectorExpr)
	if !ok || mask.Sel == nil || mask.Sel.Name != redactionMaskIdent {
		return false
	}
	x, ok := mask.X.(*ast.Ident)
	if !ok || x.Name != redactionLocal {
		return false
	}
	return true
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

// stringAttrReturnShape validates A3b: the trailing return expression of
// safeStringAttr must be
//
//	attribute.String(<paramKey>, <redactionLocal>.TruncateString(<redactionLocal>.RedactString(<paramRaw>), attrValueMaxLen))
//
// Argument identifiers are bound to the FuncDecl's formal parameter names
// (paramKey is the first param, paramRaw the second). A swap edit like
// `attribute.String(raw, redaction.TruncateString(redaction.RedactString(key), …))`
// satisfies the loose `*ast.Ident` check but fails identity binding —
// closes the A3 swap-args blindspot.
func stringAttrReturnShape(ret *ast.ReturnStmt, otelAttrLocal, redactionLocal, paramKey, paramRaw string) bool {
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
	// arg[0]: Ident bound to paramKey
	keyArg, ok := outer.Args[0].(*ast.Ident)
	if !ok || keyArg.Name != paramKey {
		return false
	}
	// arg[1]: redaction.TruncateString(redaction.RedactString(paramRaw), attrValueMaxLen)
	truncate, ok := outer.Args[1].(*ast.CallExpr)
	if !ok || !callMatches(truncate, redactionLocal, truncateStringSel) || len(truncate.Args) != 2 {
		return false
	}
	redact, ok := truncate.Args[0].(*ast.CallExpr)
	if !ok || !callMatches(redact, redactionLocal, redactStringSel) || len(redact.Args) != 1 {
		return false
	}
	rawArg, ok := redact.Args[0].(*ast.Ident)
	if !ok || rawArg.Name != paramRaw {
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

// checkA2 — callsite uniqueness. Every attribute.String(_, _) call AND every
// attribute.Key(_).<X>(...) chain shape (X ∈ String/StringSlice/StringValue)
// in span.go must sit inside the body of safeStringAttr or safeBytesAttr.
func checkA2(p *Pass, file *ast.File, otelAttrLocal string) []Diagnostic {
	if otelAttrLocal == "" {
		return nil
	}
	helperRanges := collectFuncBodyRanges(file, safeStringHelper, safeBytesHelper)
	var ds []Diagnostic
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		isBare := callMatches(call, otelAttrLocal, attributeStringSel)
		isChain := isAttributeKeyChainStringCall(call, otelAttrLocal)
		if !isBare && !isChain {
			return
		}
		if posInRanges(call.Pos(), helperRanges) {
			return
		}
		msg := violA2CallsiteEscape
		if isChain {
			msg = violA2ChainCallsite
		}
		pos := p.Fset.Position(call.Pos())
		ds = append(ds, Diagnostic{
			Rel:     filepath.ToSlash(p.Rel(file)),
			Line:    pos.Line,
			Message: msg,
		})
	})
	return ds
}

// checkA3 — safeStringAttr body shape. Splits the original A3 into:
//
//	A3a (key-branch IfStmt form): first top-level statement must be the
//	    `if redaction.IsSensitiveKey(key) { return attribute.String(key,
//	    redaction.Mask) }` structured-key bypass guard.
//	A3b (free-form trailing return): last top-level statement must be the
//	    `return attribute.String(key, redaction.TruncateString(
//	    redaction.RedactString(raw), attrValueMaxLen))` shape with arg
//	    identifiers bound to FuncDecl formal params (closes the swap-args
//	    blindspot).
func checkA3(p *Pass, file *ast.File, otelAttrLocal, redactionLocal string) []Diagnostic {
	fn, ok := findTopLevelFuncDecl(file, safeStringHelper)
	if !ok {
		// Missing helper itself fails both A3a and A3b.
		pos := p.Fset.Position(file.Package)
		rel := filepath.ToSlash(p.Rel(file))
		return []Diagnostic{
			{Rel: rel, Line: pos.Line, Message: violA3aKeyBranchMissing},
			{Rel: rel, Line: pos.Line, Message: violA3bFreeformForm},
		}
	}
	paramKey, paramRaw, paramOk := twoStringParamNames(fn)
	var ds []Diagnostic

	// A3a: first body statement must be the key-branch IfStmt.
	if fn.Body == nil || len(fn.Body.List) == 0 {
		line := p.Fset.Position(fn.Pos()).Line
		ds = append(ds, Diagnostic{
			Rel: filepath.ToSlash(p.Rel(file)), Line: line, Message: violA3aKeyBranchMissing,
		})
	} else if !paramOk {
		// No formal param to bind → can't validate key arg identity.
		line := p.Fset.Position(fn.Body.List[0].Pos()).Line
		ds = append(ds, Diagnostic{
			Rel: filepath.ToSlash(p.Rel(file)), Line: line, Message: violA3aKeyBranchMissing,
		})
	} else if ifStmt, isIf := fn.Body.List[0].(*ast.IfStmt); !isIf ||
		!keyBranchShape(ifStmt, otelAttrLocal, redactionLocal, paramKey) {
		line := p.Fset.Position(fn.Body.List[0].Pos()).Line
		ds = append(ds, Diagnostic{
			Rel: filepath.ToSlash(p.Rel(file)), Line: line, Message: violA3aKeyBranchMissing,
		})
	}

	// A3b: trailing top-level statement must be the free-form return.
	ret := trailingReturn(fn)
	if !paramOk || !stringAttrReturnShape(ret, otelAttrLocal, redactionLocal, paramKey, paramRaw) {
		line := p.Fset.Position(fn.Pos()).Line
		if ret != nil {
			line = p.Fset.Position(ret.Pos()).Line
		}
		ds = append(ds, Diagnostic{
			Rel: filepath.ToSlash(p.Rel(file)), Line: line, Message: violA3bFreeformForm,
		})
	}
	return ds
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

// TestSpanSetAttrRedacted_NoBlindspotsInProduction asserts the AST forms
// outside SPAN-SETATTR-REDACT-01's coverage do not appear in production
// adapters/otel code. If a future contributor introduces one, this reverse
// check makes the blind spot visible at archtest time. Per
// .claude/rules/gocell/ai-collab.md §"工具选定后强制盲区自检": reverse
// self-checks are prerequisite举证 for the Hard/Medium rating.
func TestSpanSetAttrRedacted_NoBlindspotsInProduction(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"adapters/otel"}, IncludeGenerated())
	var ds []Diagnostic
	Run(t, scope, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			otelAttrLocal := otelAttributeLocalName(file)
			if otelAttrLocal == "" {
				continue
			}
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				// (i) attribute.Key(_).<X>(...) chain shape — SDK-supported
				// alternative to attribute.String(...) that the original A2
				// bare-Ident check misses entirely.
				if isAttributeKeyChainStringCall(call, otelAttrLocal) {
					pos := p.Fset.Position(call.Pos())
					ds = append(ds, Diagnostic{
						Rel:  filepath.ToSlash(p.Rel(file)),
						Line: pos.Line,
						Message: "attribute.Key(_).<X>(...) chain shape is a blind spot of" +
							" SPAN-SETATTR-REDACT-01 — route through safeStringAttr or extend the funnel",
					})
					return
				}
				// (ii) attribute.StringValue / StringSlice — non-String
				// string-shaped constructors.
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil {
					return
				}
				if id, ok := sel.X.(*ast.Ident); !ok || id.Name != otelAttrLocal {
					return
				}
				switch sel.Sel.Name {
				case "StringValue", "StringSlice":
					pos := p.Fset.Position(call.Pos())
					msg := "attribute." + sel.Sel.Name +
						"(...) is a blind spot of SPAN-SETATTR-REDACT-01" +
						" — route through safeStringAttr or extend the funnel"
					ds = append(ds, Diagnostic{
						Rel:     filepath.ToSlash(p.Rel(file)),
						Line:    pos.Line,
						Message: msg,
					})
				}
			})
		}
		return ds
	})
	Report(t, "SPAN-SETATTR-REDACT-01-BLINDSPOT", ds)
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
		"violates_string_missing_key_branch",
		"violates_string_swap_args",
		"violates_key_chain_string",
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
