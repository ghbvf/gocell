package codegen

import (
	"bytes"
	"fmt"
	"sync"
	"text/template"

	"golang.org/x/tools/imports"
	gofumpt "mvdan.cc/gofumpt/format"
)

// LangVersion is the producer-side gofumpt language version. It tracks the
// `go` directive in go.mod (go 1.25) so gofumpt formats to the same language
// level the CI gate enforces.
//
// SYNC: golangci-lint auto-injects LangVersion from the go.mod `go` directive;
// this literal is a second manual truth point — when the go.mod `go` directive
// changes, update "go1.25" here in lockstep (the .golangci.yml gofumpt comment
// carries the matching note).
//
// The other formatter knob — module-local import grouping — is NOT a constant:
// it is the target repo's module path, supplied per-call to FormatGoSource /
// Render so generated files group local imports the way the *consuming* repo's
// golangci-lint gate expects. In the framework repo the module path resolved
// from go.mod equals .golangci.yml's hardcoded github.com/ghbvf/gocell, so
// producer output is byte-identical to the old const-based behavior; external
// repos developing cells supply their own module path (#1083).
const LangVersion = "go1.25"

// importsMu serializes the imports.LocalPrefix global mutation in processImports.
// imports.LocalPrefix is a package global in golang.org/x/tools/imports with no
// per-call alternative; codegen runs single-threaded in production (uncontended),
// but tests may format concurrently — the lock keeps the set-then-use critical
// section race-free under -race. imports.Process itself performs single-threaded
// resolution (no internal goroutines reading LocalPrefix), so holding the lock
// across the set + Process call is sufficient to serialize concurrent callers.
var importsMu sync.Mutex

// FormatGoSource normalizes Go source bytes through goimports → gofumpt and
// returns the canonical formatted output. It is the single producer-side
// formatter outlet; every codegen / scaffold path must funnel its rendered
// bytes through here so generated and scaffolded files match what the CI
// `golangci-lint` gate (.golangci.yml formatters.enable: gofumpt) enforces.
//
// modulePath is the consuming repo's Go module path (from its go.mod); it
// drives both module-local import grouping (imports.LocalPrefix) and gofumpt's
// module-locality detection. It is a required positional argument — "format
// without a module path" is a compile error, not a silent framework-default
// fallback (#1083). Empty modulePath is rejected (fail-closed).
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
func FormatGoSource(modulePath, filename string, src []byte) ([]byte, error) {
	if modulePath == "" {
		return src, fmt.Errorf("codegen format: modulePath is empty")
	}
	imported, err := processImports(modulePath, filename, src)
	if err != nil {
		return src, fmt.Errorf("codegen format: goimports: %w", err)
	}
	formatted, err := gofumpt.Source(imported, gofumpt.Options{
		LangVersion: LangVersion,
		ModulePath:  modulePath,
	})
	if err != nil {
		return imported, fmt.Errorf("codegen format: gofumpt: %w", err)
	}
	return formatted, nil
}

// processImports sets imports.LocalPrefix to modulePath and runs goimports.
// The LocalPrefix global write — the one sanctioned assignment in the codebase,
// sealed by archtest CODEGEN-LOCALPREFIX-SEALED-01 — and the subsequent
// imports.Process read are held under importsMu so the per-call mutation never
// races a concurrent formatter call.
func processImports(modulePath, filename string, src []byte) ([]byte, error) {
	importsMu.Lock()
	defer importsMu.Unlock()
	imports.LocalPrefix = modulePath
	return imports.Process(filename, src, &imports.Options{
		TabIndent:  true,
		TabWidth:   8,
		Comments:   true,
		FormatOnly: false,
	})
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
// modulePath is the consuming repo's module path (required positional; see
// FormatGoSource) — it is threaded straight through to the formatter.
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
func Render(modulePath string, opts RenderOptions) ([]byte, error) {
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
	formatted, err := FormatGoSource(modulePath, opts.Filename, raw)
	if err != nil {
		return raw, fmt.Errorf("codegen render: template %q: %w", opts.TemplateName, err)
	}
	return formatted, nil
}
