package contractgen

import (
	"embed"
	"strconv"
	"strings"
	"text/template"

	"github.com/ghbvf/gocell/tools/codegen"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

// templates is the parsed template set, sharing the header template from
// codegen.SharedTemplates and layering contractgen-local templates on top.
// Template functions are registered for conditional rendering in handler.tmpl.
var templates = func() *template.Template {
	funcMap := template.FuncMap{
		// not negates a boolean value.
		"not": func(b bool) bool { return !b },
		// quoteGoString returns a Go double-quoted string literal for s.
		// Used to embed the request schema JSON constant safely.
		"quoteGoString": strconv.Quote,
		// hasPathParams reports whether the endpoint has path parameters.
		"hasPathParams": func(ep *httpEndpointSpec) bool {
			return ep != nil && len(ep.PathParams) > 0
		},
		// hasQueryNumeric reports whether any query param requires strconv parsing.
		"hasQueryNumeric": func(params []ParamSpec) bool {
			for _, p := range params {
				if p.GoType == "int64" || p.GoType == "float64" || p.GoType == "bool" {
					return true
				}
			}
			return false
		},
		// needsStrconv reports whether the generated handler imports the strconv
		// package. Drives the import block under the typed-response-envelope
		// template: when Pagination is set, cursor/limit go through
		// httputil.ParsePageParams (no strconv), and only the extra filter
		// params still need per-param parsing; when Pagination is nil, the
		// full QueryParams slice is parsed inline.
		"needsStrconv": needsStrconv,
		// derefInt64 dereferences a *int64 pointer for use in templates.
		"derefInt64": func(p *int64) int64 {
			if p == nil {
				return 0
			}
			return *p
		},
		// derefInt dereferences an *int pointer (used for MinLength/MaxLength).
		"derefInt": func(p *int) int {
			if p == nil {
				return 0
			}
			return *p
		},
		// hasMinimum reports whether the param has a declared minimum value.
		"hasMinimum": func(p *int64) bool {
			return p != nil
		},
		// hasMaximum reports whether the param has a declared maximum value.
		"hasMaximum": func(p *int64) bool {
			return p != nil
		},
		// needsMinLengthCheck / needsGuardedMinLengthCheck gate the minLength
		// lower-bound branch per render context (see the functions below).
		// Extracted to top-level functions (like needsStrconv) so the
		// dead-code-suppression logic is unit-tested directly. Issue #1914.
		"needsMinLengthCheck":        needsMinLengthCheck,
		"needsGuardedMinLengthCheck": needsGuardedMinLengthCheck,
		// hasMaxLength reports whether a string param/field has a maxLength
		// constraint. Unlike minLength, maxLength:0 stays a real constraint —
		// `len(x) > 0` rejects every non-empty value — so this predicate
		// intentionally does not mirror the minLength gating.
		"hasMaxLength": func(p *int) bool {
			return p != nil
		},
		// needsErrcode reports whether the generated handler imports the errcode
		// package. With the typed-response envelope every HTTP handler emits at
		// minimum the post-service nil-response guard
		// (`errcode.New(KindInternal, ErrInternal, ...)`), so the predicate is
		// effectively `spec.Endpoint != nil`. The historical per-feature checks
		// (request schema, path-param length constraints, per-param query parse)
		// still apply for sanity but no longer narrow the import.
		"needsErrcode": func(spec *ContractGenSpec) bool {
			return spec.Endpoint != nil
		},
		// omitEmptyCheck returns the Go boolean expression that evaluates to true
		// when the field value is non-zero, to be used as the condition in the
		// generated ToMap conditional entry. The expression uses the receiver
		// variable name "i". This makes the projection path wire-equivalent to the
		// struct json.Marshal serialization path (both omit zero-value optional fields).
		//
		// Type dispatch:
		//   string           → i.X != ""
		//   int64/float64    → i.X != 0
		//   bool / *bool     → not used for optional fields (optional bool → *bool)
		//   *T (pointer)     → i.X != nil
		//   []T (slice)      → len(i.X) > 0
		//   any              → i.X != nil
		"omitEmptyCheck": omitEmptyCheck,
	}

	t := template.Must(codegen.SharedTemplates.Clone())
	t = t.Funcs(funcMap)
	return template.Must(t.ParseFS(templateFS, "templates/*.tmpl"))
}()

// needsStrconv is the package-private implementation of the funcMap helper
// of the same name. Extracted to a top-level function so unit tests can
// invoke it directly without round-tripping through template.FuncMap value
// resolution (which fixes the funcMap value to interface{}). The helper is
// covered by render_test.TestNeedsStrconv.
func needsStrconv(spec *ContractGenSpec) bool {
	if spec == nil || spec.Endpoint == nil {
		return false
	}
	ep := spec.Endpoint
	if ep.Pagination != nil {
		for _, p := range ep.Pagination.ExtraQueryParams {
			if p.GoType == "int64" || p.GoType == "float64" || p.GoType == "bool" {
				return true
			}
		}
		return false
	}
	for _, p := range ep.QueryParams {
		if p.GoType == "int64" || p.GoType == "float64" || p.GoType == "bool" {
			return true
		}
	}
	return false
}

// needsMinLengthCheck gates the UNGUARDED minLength lower-bound branch emitted
// for path params (`len(v) < N`, no `!= ""` prefix). minLength:0 would render
// `len(v) < 0`, which is always false because a string length is never negative
// — dead code — so only a positive minLength yields a real check. Covered by
// render_test.TestNeedsMinLengthCheck. Issue #1914.
func needsMinLengthCheck(p *int) bool {
	return p != nil && *p > 0
}

// needsGuardedMinLengthCheck gates the GUARDED minLength lower-bound branch
// emitted for query params (`req.X != "" && len(req.X) < N`). The `!= ""` guard
// already enforces len(req.X) >= 1 for any value that reaches the comparison, so
// N <= 1 (including 0) makes the check always false — dead code — and only
// N >= 2 can ever reject a non-empty value. Covered by
// render_test.TestNeedsGuardedMinLengthCheck. Issue #1914.
func needsGuardedMinLengthCheck(p *int) bool {
	return p != nil && *p > 1
}

// omitEmptyCheck returns the Go boolean expression that is true when a DTOField
// value is non-zero, for use in the generated ToMap conditional guard. The
// receiver variable is always "i" (the ToMap method receiver).
//
// Type dispatch follows json encoding/omitempty semantics:
//   - string             → i.FieldName != ""
//   - int64 / float64   → i.FieldName != 0
//   - bool               → i.FieldName (truthy, mirrors json omitempty: false is omitted)
//   - []T (slice)        → len(i.FieldName) > 0
//   - *T (pointer)       → i.FieldName != nil
//   - any               → i.FieldName != nil
//
// Note: optional bool fields in generated DTOs are always *bool (the builder
// converts them in collectDTOs), so the plain "bool" case does not arise for
// OmitEmpty fields in practice; pointer dispatch covers it. The bool branch
// is present as a defensive fallback only.
func omitEmptyCheck(f DTOField) string {
	switch {
	case f.GoType == "string":
		return `i.` + f.Name + ` != ""`
	case f.GoType == "int64" || f.GoType == "float64":
		return `i.` + f.Name + ` != 0`
	case f.GoType == "bool":
		// plain bool omitempty: false is omitted, true is included.
		// In EmitToMap DTOs, optional bools are always *bool (builder converts
		// them in collectDTOs), so this branch is not reachable in production
		// generated code; it exists as a defensive fallback.
		return `i.` + f.Name
	case strings.HasPrefix(f.GoType, "[]"):
		return `len(i.` + f.Name + `) > 0`
	default:
		// pointer (*T), any, or any other reference type
		return `i.` + f.Name + ` != nil`
	}
}

// Per-template rendering for production goes through Generate /
// RenderContractArtifacts (generator.go), which call codegen.Render directly.
// The per-artifact render wrappers used to live here but were only ever called
// from tests; they now live in render_test.go as test helpers (their sole
// remaining use), so render.go carries no test-only surface.
