package codegen

import (
	"bytes"
	"fmt"
	"text/template"

	"golang.org/x/tools/imports"
	gofumpt "mvdan.cc/gofumpt/format"
)

// localPrefix is this module's import path. Sourced once and used by both
// the goimports grouping (`imports.LocalPrefix`) and gofumpt's module
// locality detection (`GofumptOptions.ModulePath`) so the producer mirrors
// the CI consumer gate (`.golangci.yml goimports.local-prefixes`) from a
// single authoritative literal.
const localPrefix = "github.com/ghbvf/gocell"

// init aligns goimports' package-level LocalPrefix with the CI formatter
// configuration. Without this, producer output would not group local
// imports separately from third-party imports — `golangci-lint fmt`
// would then re-shuffle the bytes the producer emitted, defeating
// "produced output is CI-canonical". imports.LocalPrefix is a package
// global; setting it in init keeps it stable for the lifetime of the
// codegen process (single-threaded build phase).
func init() {
	imports.LocalPrefix = localPrefix
}

// GofumptOptions are the producer-side gofumpt config. LangVersion tracks
// the go directive in go.mod (go 1.25); ModulePath matches the module
// declaration so gofumpt can group imports by module locality.
//
// These values must stay aligned with the CI formatter gate
// (.golangci.yml formatters.enable: gofumpt + golangci-lint v2.11.4).
// Exported so tests across tools/codegen, tools/codegen/cellgen, and
// tools/generatedcatalog can reference a single authoritative copy.
var GofumptOptions = gofumpt.Options{
	LangVersion: "go1.25",
	ModulePath:  localPrefix,
}

// FormatGoSource normalizes Go source bytes through goimports → gofumpt and
// returns the canonical formatted output. It is the single producer-side
// formatter outlet; every codegen / scaffold path must funnel its rendered
// bytes through here so generated and scaffolded files match what the CI
// `golangci-lint` gate (.golangci.yml formatters.enable: gofumpt) enforces.
//
// filename is the path goimports uses to resolve module-local imports —
// pass empty string when the source is not a file on disk.
//
// Pipeline order is goimports → gofumpt: gofumpt requires its input to be
// canonical gofmt-shaped, and goimports.Process produces exactly that while
// also resolving and ordering the import block.
//
// On failure, FormatGoSource returns the latest intermediate bytes (raw
// input on goimports failure, goimports output on gofumpt failure) so
// callers can pretty-print the offending source for debugging — these
// bytes MUST NOT be written to disk.
//
// ref: mvdan.cc/gofumpt format/format.go — gopls and golangci-lint adopt
// the same goimports → gofumpt ordering.
func FormatGoSource(filename string, src []byte) ([]byte, error) {
	imported, err := imports.Process(filename, src, &imports.Options{
		TabIndent:  true,
		TabWidth:   8,
		Comments:   true,
		FormatOnly: false,
	})
	if err != nil {
		return src, fmt.Errorf("codegen format: goimports: %w", err)
	}
	formatted, err := gofumpt.Source(imported, GofumptOptions)
	if err != nil {
		return imported, fmt.Errorf("codegen format: gofumpt: %w", err)
	}
	return formatted, nil
}

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

// Render executes a template and runs the producer formatter pipeline
// (goimports → gofumpt) over the output.
//
// Two-stage pipeline (each stage gates the next):
//  1. text/template.Execute renders raw source bytes
//  2. FormatGoSource applies goimports + gofumpt — failure here typically
//     means the template emitted invalid Go syntax; the raw bytes are
//     returned with the error so callers can pretty-print the offending
//     source for template debugging.
//
// # Returns
//
// Returns rendered bytes on success. When the formatter pipeline fails,
// the returned bytes contain raw template output for debugging — callers
// MUST NOT write them to disk.
//
// ref: ent/ent entc/gen/template.go — same staged-pipeline ordering.
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
	formatted, err := FormatGoSource(opts.Filename, raw)
	if err != nil {
		return raw, fmt.Errorf("codegen render: template %q: %w", opts.TemplateName, err)
	}
	return formatted, nil
}
