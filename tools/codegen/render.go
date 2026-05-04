package codegen

import (
	"bytes"
	"fmt"
	"go/format"
	"text/template"

	"golang.org/x/tools/imports"
)

// RenderOptions configures a template-driven Go source render pass.
type RenderOptions struct {
	// TemplateName is the named template invoked from the parsed Templates set.
	TemplateName string
	// Templates is a parsed *template.Template containing TemplateName plus
	// any templates it references (e.g. via {{template "header" .}}).
	Templates *template.Template
	// Data is bound to the template's "." context.
	Data any
	// Filename is the absolute path of the file being rendered. goimports
	// uses it to resolve module-local imports. Empty filenames disable
	// path-aware import resolution.
	Filename string
}

// Render executes a template, runs go/format on the output, then goimports.
//
// Three-stage pipeline (each stage gates the next):
//  1. text/template.Execute renders raw source bytes
//  2. go/format.Source enforces canonical layout — failure here typically
//     means the template emitted invalid Go syntax; the raw bytes are
//     returned with the error so callers can pretty-print the offending
//     source for template debugging
//  3. golang.org/x/tools/imports.Process tidies and orders imports
//
// # Returns
//
// Returns rendered bytes on success. When go/format or goimports fails, the
// returned bytes contain the raw template output for debugging — callers
// MUST NOT write them to disk.
//
// ref: ent/ent entc/gen/template.go — same pipeline ordering.
func Render(opts RenderOptions) ([]byte, error) {
	if opts.Templates == nil {
		return nil, fmt.Errorf("codegen render: Templates is nil")
	}
	if opts.TemplateName == "" {
		return nil, fmt.Errorf("codegen render: TemplateName is empty")
	}

	var buf bytes.Buffer
	if err := opts.Templates.ExecuteTemplate(&buf, opts.TemplateName, opts.Data); err != nil {
		return nil, fmt.Errorf("codegen render: execute template %q: %w", opts.TemplateName, err)
	}

	raw := buf.Bytes()
	formatted, fmtErr := format.Source(raw)
	if fmtErr != nil {
		return raw, fmt.Errorf("codegen render: go/format failed for template %q: %w", opts.TemplateName, fmtErr)
	}

	final, impErr := imports.Process(opts.Filename, formatted, &imports.Options{
		TabIndent:  true,
		TabWidth:   8,
		Comments:   true,
		FormatOnly: false,
	})
	if impErr != nil {
		return formatted, fmt.Errorf("codegen render: goimports failed for %q: %w", opts.Filename, impErr)
	}
	return final, nil
}
