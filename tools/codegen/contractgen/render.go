package contractgen

import (
	"embed"
	"strconv"
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
		// hasMinLength reports whether a string param/field has a minLength constraint.
		"hasMinLength": func(p *int) bool {
			return p != nil
		},
		// hasMaxLength reports whether a string param/field has a maxLength constraint.
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

// Per-template rendering for production goes through Generate /
// RenderContractArtifacts (generator.go), which call codegen.Render directly.
// The per-artifact render wrappers used to live here but were only ever called
// from tests; they now live in render_test.go as test helpers (their sole
// remaining use), so render.go carries no test-only surface.
