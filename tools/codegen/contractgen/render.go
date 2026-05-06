package contractgen

import (
	"embed"
	"fmt"
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
		"hasPathParams": func(ep *HTTPEndpointSpec) bool {
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

// RenderTypes renders types_gen.go content for the given spec.
// Returns formatted, goimports-processed bytes.
func RenderTypes(spec *ContractGenSpec, filename string) ([]byte, error) {
	b, err := codegen.Render(codegen.RenderOptions{
		TemplateName: "types.tmpl",
		Templates:    templates,
		Data:         spec,
		Filename:     filename,
	})
	if err != nil {
		return b, fmt.Errorf("contractgen render types: %w", err)
	}
	return b, nil
}

// RenderIface renders iface_gen.go content for the given spec.
// Returns formatted, goimports-processed bytes.
func RenderIface(spec *ContractGenSpec, filename string) ([]byte, error) {
	b, err := codegen.Render(codegen.RenderOptions{
		TemplateName: "iface.tmpl",
		Templates:    templates,
		Data:         spec,
		Filename:     filename,
	})
	if err != nil {
		return b, fmt.Errorf("contractgen render iface: %w", err)
	}
	return b, nil
}

// RenderHandler renders handler_gen.go content for the given spec.
// Only valid for kind=http metadata. Returns an error for event metadata.
func RenderHandler(spec *ContractGenSpec, filename string) ([]byte, error) {
	if spec.Kind != "http" {
		return nil, fmt.Errorf("contractgen render handler: contract %q is kind=%q, not http", spec.ContractID, spec.Kind)
	}
	b, err := codegen.Render(codegen.RenderOptions{
		TemplateName: "handler.tmpl",
		Templates:    templates,
		Data:         spec,
		Filename:     filename,
	})
	if err != nil {
		return b, fmt.Errorf("contractgen render handler: %w", err)
	}
	return b, nil
}

// RenderSpec renders spec_gen.go content for the given spec.
// Only valid for kind=event contracts. Returns an error for non-event contracts.
func RenderSpec(spec *ContractGenSpec, filename string) ([]byte, error) {
	if spec.Kind != "event" {
		return nil, fmt.Errorf("contractgen render spec: contract %q is kind=%q, not event", spec.ContractID, spec.Kind)
	}
	b, err := codegen.Render(codegen.RenderOptions{
		TemplateName: "spec.tmpl",
		Templates:    templates,
		Data:         spec,
		Filename:     filename,
	})
	if err != nil {
		return b, fmt.Errorf("contractgen render spec: %w", err)
	}
	return b, nil
}

// RenderSubscription renders subscription_gen.go content for the given spec.
// Only valid for kind=event contracts. Returns an error for non-event contracts.
func RenderSubscription(spec *ContractGenSpec, filename string) ([]byte, error) {
	if spec.Kind != "event" {
		return nil, fmt.Errorf("contractgen render subscription: contract %q is kind=%q, not event", spec.ContractID, spec.Kind)
	}
	b, err := codegen.Render(codegen.RenderOptions{
		TemplateName: "subscription.tmpl",
		Templates:    templates,
		Data:         spec,
		Filename:     filename,
	})
	if err != nil {
		return b, fmt.Errorf("contractgen render subscription: %w", err)
	}
	return b, nil
}
