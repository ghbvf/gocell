package codegen

import (
	"embed"
	"text/template"
)

//go:embed templates/*.tmpl
var sharedTemplateFS embed.FS

// SharedTemplates is the parsed set of shared templates available to all
// codegen subpackages (cellgen, contractgen, markergen). It includes
// tools/codegen/templates/header.tmpl which defines the "header" template.
//
// Subpackages MUST NOT embed header.tmpl themselves. Instead, clone
// SharedTemplates and layer subpackage-local templates on top:
//
//	tmpl := template.Must(codegen.SharedTemplates.Clone())
//	template.Must(tmpl.ParseFS(localFS, "templates/*.tmpl"))
var SharedTemplates = template.Must(template.ParseFS(sharedTemplateFS, "templates/*.tmpl"))
