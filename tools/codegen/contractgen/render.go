package contractgen

import (
	"embed"
	"fmt"
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
		// derefInt dereferences a *int pointer for use in templates.
		"derefInt": func(p *int) int {
			if p == nil {
				return 0
			}
			return *p
		},
		// derefInt64 dereferences a *int64 pointer for use in templates.
		"derefInt64": func(p *int64) int64 {
			if p == nil {
				return 0
			}
			return *p
		},
		// hasMinLength reports whether the param has a positive minimum length.
		"hasMinLength": func(p *int) bool {
			return p != nil && *p > 0
		},
		// hasMaxLength reports whether the param has a declared maximum length.
		"hasMaxLength": func(p *int) bool {
			return p != nil
		},
		// hasMinimum reports whether the param has a declared minimum value.
		"hasMinimum": func(p *int64) bool {
			return p != nil
		},
		// hasMaximum reports whether the param has a declared maximum value.
		"hasMaximum": func(p *int64) bool {
			return p != nil
		},
		// isBodyField reports whether a DTOField originates from the request body
		// (Source == "body" or empty — legacy default). Used to gate body validation.
		"isBodyField": func(f DTOField) bool {
			return f.Source == "" || f.Source == "body"
		},
		// hasBodyValidation reports whether any DTO field in the Request struct
		// requires body-level schema validation (minLength/maxLength/minimum/maximum).
		"hasBodyValidation": func(dtos []DTOSpec) bool {
			for _, dto := range dtos {
				if dto.Name != "Request" {
					continue
				}
				for _, f := range dto.Fields {
					if f.Source != "" && f.Source != "body" {
						continue
					}
					if f.MinLength != nil || f.MaxLength != nil || f.Minimum != nil || f.Maximum != nil {
						return true
					}
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
