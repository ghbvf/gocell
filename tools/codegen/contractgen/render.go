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
		// needsErrcode reports whether the generated handler requires the errcode
		// package. It is needed when: any query param exists (required check / parse
		// errors), or when any path param has a length constraint (B4 follow-up:
		// path string-length validation re-instated with generic "invalid" message
		// to avoid oracle leakage). Body schema validation is delegated to
		// schemavalidate.Validator and does not require errcode here.
		"needsErrcode": func(spec *ContractGenSpec) bool {
			if spec.Endpoint == nil {
				return false
			}
			ep := spec.Endpoint
			if len(ep.QueryParams) > 0 && !ep.IsPagination {
				return true
			}
			for _, p := range ep.PathParams {
				if p.MinLength != nil || p.MaxLength != nil {
					return true
				}
			}
			return false
		},
	}

	t := template.Must(codegen.SharedTemplates.Clone())
	t = t.Funcs(funcMap)
	return template.Must(t.ParseFS(templateFS, "templates/*.tmpl"))
}()

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
