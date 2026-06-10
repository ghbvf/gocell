//go:build archtest

// invariants asserted in this file:
//   - INVARIANT: HTTP-HEADERS-FIELD-FROZEN-01
//   - INVARIANT: HTTP-REQUEST-HEADER-READ-FUNNEL-01
//
// Package archtest — inbound HTTP request-header single-source funnel (issue #1494).
//
// contract.yaml endpoints.http.headers is the single source for request-header
// consumption. contractgen merges each declared header into the generated Request
// DTO (json:"-", populate-only) and the generated handler reads it via
// r.Header.Get. These two invariants close the funnel in both directions:
//
//  1. DOWNSTREAM is locked by the contractgen golden (declared headers: ⟹ generated
//     accessor; byte-pinned by synth_http_full goldens) + this file's reflect lock
//     on HTTPTransportMeta.Headers (the field can't be renamed/retyped/retagged
//     without an explicit review checkpoint).
//  2. UPSTREAM is locked by HTTP-REQUEST-HEADER-READ-FUNNEL-01: business code
//     (cells/, examples/) may NOT read an inbound request header raw. Forcing every
//     inbound business header through the contract declaration is what stops the
//     next developer from re-opening the "two truths" hole this issue closed (a new
//     undeclared header read piped into a handler). The sole sanctioned reader is
//     the generated handler under generated/, which this scan does not cover.
//
// AI-robust ratings (per .claude/rules/gocell/ai-robust.md):
//
//   - HTTP-HEADERS-FIELD-FROZEN-01 — Hard (reflect schema freeze). The field name,
//     type identity (map[string]ParamSchema), and yaml/json tags are runtime-
//     enumerated structural facts; any drift fails the tuple compare, forcing the
//     change onto an explicit review checkpoint. No string anchor.
//   - HTTP-REQUEST-HEADER-READ-FUNNEL-01 — Funnel dual-lock:
//     下游 Hard: the banned read is resolved via go/types (the receiver selector
//     resolves to *net/http.Request.Header regardless of import alias), and the
//     read-vs-write distinction (.Get/.Values/index = read; .Set/.Add = write) is
//     structural, so an outbound req.Header.Set (e.g. configclient Authorization)
//     and a w.Header().Get response read are not false-positives.
//     上游 Medium — a GO-LANGUAGE CEILING (not a deferred TODO): Go cannot make
//     "only generated code may touch *http.Request.Header" a compile error — the
//     *http.Request is freely passed to every handler, and inter-procedural data
//     flow (passing the header to a helper) is not statically tracked. Same
//     permanent ceiling as CTXKEYS-REALIP-READ-CALLER-01 / SPAN-SETATTR-HOLDER-SEAL
//     (#851) / HEALTHZ-HOLDER-SEAL (#893) / outbox principal-write (#1282); the
//     generic Hard mechanism (codegen-derived typed header binding for all inbound
//     reads) is the contractgen funnel itself, tracked as the won't-do Go ceiling
//     at gh #1646.
//
// Tool blind spots (charter §"强制盲区自检", reverse self-check = the RED fixture):
//   - The detector flags `<x>.Header.Get/.Values` and `<x>.Header[...]` when x is a
//     *net/http.Request, AND the one-hop alias `h := r.Header; h.Get(..)` —
//     collectInboundHeaderAliases binds aliased locals by go/types *Object identity
//     (so a same-named var in another scope never folds in). REMAINING residue
//     (the Medium ceiling above, NOT a cheap bypass): multi-hop alias (`h2 := h`),
//     passing r.Header to a helper (inter-procedural data flow), and `r.Header.Clone()`
//     / `range r.Header` (not value reads). The contract-declared path is the norm.
//   - The production scan is scoped to cells/ + examples/ (business layers that own
//     contracts). Framework header reads in runtime/ + adapters/ (auth /
//     idempotency / readyz-token middleware) are transport concerns, deliberately
//     out of scope.
//   - The RED fixture (internal/headerreadfixture) proves the detector fires on the
//     three read forms and does not false-positive on the write / response-read
//     controls — this is the teeth check for a flat ban (no allowlist to anti-vacuity).
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestHTTPHeadersFieldFrozen01 locks the HTTPTransportMeta.Headers field shape:
// name, type identity (map[string]ParamSchema), and the yaml/json tags that make
// it the hand-written single source. A rename/retype/retag surfaces here as an
// explicit review checkpoint rather than a silent contract drift.
func TestHTTPHeadersFieldFrozen01(t *testing.T) {
	t.Parallel()
	ht := reflect.TypeOf(metadata.HTTPTransportMeta{})

	f, ok := ht.FieldByName("Headers")
	if !ok {
		t.Fatal("HTTP-HEADERS-FIELD-FROZEN-01: HTTPTransportMeta has no Headers field (rename?)")
	}
	wantType := reflect.TypeOf(map[string]metadata.ParamSchema{})
	if f.Type != wantType {
		t.Errorf("HTTP-HEADERS-FIELD-FROZEN-01: HTTPTransportMeta.Headers type = %s, want %s "+
			"(headers reuse ParamSchema, same as path/query params)", f.Type, wantType)
	}
	if got := f.Tag.Get("yaml"); got != "headers,omitempty" {
		t.Errorf("HTTP-HEADERS-FIELD-FROZEN-01: HTTPTransportMeta.Headers yaml tag = %q, want %q", got, "headers,omitempty")
	}
	if got := f.Tag.Get("json"); got != "headers,omitempty" {
		t.Errorf("HTTP-HEADERS-FIELD-FROZEN-01: HTTPTransportMeta.Headers json tag = %q, want %q", got, "headers,omitempty")
	}
}

// isInboundRequestHeaderSelector reports whether expr is a `<x>.Header` field
// selector where x is (*)net/http.Request — i.e. the inbound request header map.
// Resolved via go/types so an import alias for net/http does not evade it.
func isInboundRequestHeaderSelector(info *types.Info, expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Header" {
		return false
	}
	t := info.TypeOf(sel.X)
	if t == nil {
		return false
	}
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == "net/http" && obj.Name() == "Request"
}

// collectInboundHeaderAliases returns the set of variable objects bound to an
// inbound `*http.Request.Header` within file, e.g. `h := r.Header` /
// `var h = r.Header` (#1494 review F5 — one-hop alias). Binding is by go/types
// *Object identity, so a same-named variable in another function/scope is a
// distinct object and never folds in (no cross-scope false positive). Multi-hop
// (`h2 := h`), passing the header to a helper, and other inter-procedural data
// flow remain a documented Medium residue (a genuine Go static-analysis ceiling).
func collectInboundHeaderAliases(info *types.Info, file *ast.File) map[types.Object]bool {
	aliases := map[types.Object]bool{}
	record := func(lhs ast.Expr, rhs ast.Expr) {
		if !isInboundRequestHeaderSelector(info, rhs) {
			return
		}
		if id, ok := lhs.(*ast.Ident); ok {
			if obj := info.ObjectOf(id); obj != nil {
				aliases[obj] = true
			}
		}
	}
	EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
		for i, rhs := range as.Rhs {
			if i < len(as.Lhs) {
				record(as.Lhs[i], rhs)
			}
		}
	})
	EachInSubtree[ast.ValueSpec](file, func(vs *ast.ValueSpec) {
		for i, rhs := range vs.Values {
			if i < len(vs.Names) {
				record(vs.Names[i], rhs)
			}
		}
	})
	return aliases
}

// isInboundHeaderExpr reports whether expr denotes the inbound request header
// map — either a direct `<req>.Header` selector or a local variable aliased to
// one (collectInboundHeaderAliases).
func isInboundHeaderExpr(info *types.Info, expr ast.Expr, aliases map[types.Object]bool) bool {
	if isInboundRequestHeaderSelector(info, expr) {
		return true
	}
	if id, ok := expr.(*ast.Ident); ok {
		if obj := info.ObjectOf(id); obj != nil {
			return aliases[obj]
		}
	}
	return false
}

// scanInboundHeaderReads reports every inbound request-header READ in a file:
// `<req>.Header.Get(..)` / `.Values(..)` (read APIs) and `<req>.Header[..]`
// (map index), including reads via a one-hop alias `h := r.Header` (review F5).
// Writes (.Set/.Add/.Del) and response reads (w.Header().Get) are structurally
// excluded — the receiver of the write methods is not in {Get,Values} and
// w.Header() is a call, not a *http.Request.Header field selector or its alias.
func scanInboundHeaderReads(info *types.Info, file *ast.File, rel string, position func(p ast.Node) (int, int)) []Diagnostic {
	var d []Diagnostic
	aliases := collectInboundHeaderAliases(info, file)
	report := func(n ast.Node, form string) {
		line, _ := position(n)
		d = append(d, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: fmt.Sprintf(
				"HTTP-REQUEST-HEADER-READ-FUNNEL-01: %s reads an inbound request header via %s. Business "+
					"code (cells/, examples/) must not read inbound headers raw — declare the header in "+
					"contract.yaml endpoints.http.headers and read the generated Request field instead "+
					"(the generated handler is the sole sanctioned reader). This keeps the header "+
					"single-sourced; a raw read re-opens the 'two truths' gap #1494 closed.",
				rel, form),
		})
	}
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return
		}
		if sel.Sel.Name != "Get" && sel.Sel.Name != "Values" {
			return
		}
		if isInboundHeaderExpr(info, sel.X, aliases) {
			report(call, "r.Header."+sel.Sel.Name)
		}
	})
	EachInSubtree[ast.IndexExpr](file, func(ix *ast.IndexExpr) {
		if isInboundHeaderExpr(info, ix.X, aliases) {
			report(ix, "r.Header[...] index")
		}
	})
	return d
}

// TestHTTPRequestHeaderReadFunnel01 asserts no production file under cells/ or
// examples/ reads an inbound request header raw. The sanctioned reader is the
// generated handler (under generated/, not scanned here).
func TestHTTPRequestHeaderReadFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			if !strings.HasPrefix(rel, "cells/") && !strings.HasPrefix(rel, "examples/") {
				continue
			}
			pos := func(n ast.Node) (int, int) {
				p := p.Fset.Position(n.Pos())
				return p.Line, p.Column
			}
			d = append(d, scanInboundHeaderReads(p.TypesInfo, file, rel, pos)...)
		}
		return d
	})
	Report(t, "HTTP-REQUEST-HEADER-READ-FUNNEL-01", diags)
}

// TestHTTPRequestHeaderReadFunnel01_FixtureFires is the reverse self-check (teeth
// proof for the flat ban): the RED fixture has 4 inbound reads — direct
// Get/Values/index PLUS the one-hop alias `h := r.Header; h.Get(...)` (#1494
// review F5, now closed) — and GREEN controls (outbound Set write, response
// w.Header().Get, and an inter-procedural read passed to a helper). The detector
// must flag exactly the 4 reads. The count of 4 confirms the one-hop alias is
// closed; the GREEN inter-procedural read confirms the documented Medium residue
// (a genuine Go data-flow ceiling) stays out of scope (no false positive).
func TestHTTPRequestHeaderReadFunnel01_FixtureFires(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")

	fixturePkg := modPath + "/tools/archtest/internal/headerreadfixture"
	pattern := "./tools/archtest/internal/headerreadfixture/..."
	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{pattern}), func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != fixturePkg {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			pos := func(n ast.Node) (int, int) {
				p := p.Fset.Position(n.Pos())
				return p.Line, p.Column
			}
			d = append(d, scanInboundHeaderReads(p.TypesInfo, file, rel, pos)...)
		}
		return d
	})
	for _, dg := range diags {
		t.Log(dg.Message)
	}
	require.Len(t, diags, 4,
		"fixture must yield exactly 4 inbound reads (Get/Values/index + one-hop alias h:=r.Header); "+
			"the outbound Set write, the w.Header().Get response read, and the inter-procedural read "+
			"(helper taking http.Header — the documented Medium residue) must NOT be flagged — 4 confirms "+
			"the one-hop alias is closed and the data-flow ceiling residue stays out of scope")
	for _, dg := range diags {
		assert.Contains(t, dg.Message, "HTTP-REQUEST-HEADER-READ-FUNNEL-01")
	}
}
