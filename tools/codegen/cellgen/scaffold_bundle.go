// scaffold_bundle.go implements K#09 ScaffoldCellBundle: a one-shot scaffold
// orchestrator that produces a compilable + testable cell skeleton in a
// single call. Composes ScaffoldCell (cell.yaml + cell.go) with
// ScaffoldExampleSlice (slice.yaml + service.go + service_test.go) and
// ScaffoldExampleContract (contract.yaml + JSON schemas).
//
// The resulting bundle layout (HTTP variant):
//
//	cells/{id}/cell.yaml
//	cells/{id}/cell.go
//	cells/{id}/slices/{id}example/{slice.yaml,service.go,service_test.go}
//	contracts/http/{id}/example/v1/{contract.yaml,request.schema.json,response.schema.json}
//
// The event variant swaps:
//
//	contracts/event/{id}/example/v1/{contract.yaml,payload.schema.json,headers.schema.json}
//
// K#09 funnel: scaffold output never writes the contract.yaml `codegen:` field
// (kernel/metadata parser defaults it to true when absent). See
// INVARIANT SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01.

package cellgen

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/ghbvf/gocell/tools/codegen"
)

// sliceBundleTemplate parses the multi-section scaffold-slice.tmpl which
// defines slice-yaml / service-go / service-test-go template blocks.
var sliceBundleTemplate = template.Must(template.New("scaffold-slice.tmpl").
	ParseFS(templateFS, "templates/scaffold-slice.tmpl"))

// contractBundleTemplate parses the multi-section scaffold-contract.tmpl which
// defines contract-yaml-http / contract-yaml-event / request-schema /
// response-schema / payload-schema / headers-schema template blocks.
var contractBundleTemplate = template.Must(template.New("scaffold-contract.tmpl").
	ParseFS(templateFS, "templates/scaffold-contract.tmpl"))

// bundleData is the shared template context for slice + contract bundle
// templates. Computed once in ScaffoldCellBundle from a ScaffoldSpec.
type bundleData struct {
	CellID       string
	SlicePackage string // SliceID with no dashes (Go package name)
	SliceID      string
	SliceRole    string // "serve" for HTTP, "publish" for event
	ContractID   string // e.g. http.{id}.example.v1
}

// ScaffoldCellBundle is the K#09 one-shot scaffold orchestrator. It composes
// ScaffoldCell + ScaffoldExampleSlice + ScaffoldExampleContract; on dry-run
// every template renders (catching errors) and conflict detection runs but
// no files are written.
//
// Defaults: when neither WithHTTP nor WithEvents is set, WithHTTP applies.
// WithBoth produces both an HTTP contract and an event contract.
//
// On any rendering or write failure the function returns immediately —
// callers should treat partial output as failed and rerun after resolving.
func ScaffoldCellBundle(root string, spec ScaffoldSpec) error {
	if err := validateScaffoldSpec(spec); err != nil {
		return err
	}

	// Step 1 — cell skeleton (cell.go + cell.yaml). Reuse existing ScaffoldCell.
	cellTarget := filepath.Join("cells", spec.CellID)
	if err := ScaffoldCell(root, cellTarget, spec); err != nil {
		return err
	}

	withHTTP, withEvents := resolveBundleVariants(spec)
	cellNoDash := strings.ReplaceAll(spec.CellID, "-", "")
	sliceID := cellNoDash + "example" // Go package + slice ID, dash-stripped

	if withHTTP {
		if err := scaffoldHTTPExampleArtifacts(root, spec, cellNoDash, sliceID); err != nil {
			return err
		}
	}
	if withEvents {
		if err := scaffoldEventExampleArtifacts(root, spec, cellNoDash, sliceID); err != nil {
			return err
		}
	}
	return nil
}

// resolveBundleVariants picks the contract variants to scaffold from the
// spec's WithHTTP / WithEvents / WithBoth flags. When all three are unset
// (default) the bundle includes HTTP only.
func resolveBundleVariants(spec ScaffoldSpec) (withHTTP, withEvents bool) {
	withHTTP = spec.WithHTTP || spec.WithBoth || (!spec.WithHTTP && !spec.WithEvents && !spec.WithBoth)
	withEvents = spec.WithEvents || spec.WithBoth
	return withHTTP, withEvents
}

// scaffoldHTTPExampleArtifacts produces the HTTP slice + contract pair.
func scaffoldHTTPExampleArtifacts(root string, spec ScaffoldSpec, cellNoDash, sliceID string) error {
	bd := bundleData{
		CellID:       spec.CellID,
		SlicePackage: sliceID,
		SliceID:      sliceID,
		SliceRole:    "serve",
		ContractID:   fmt.Sprintf("http.%s.example.v1", cellNoDash),
	}
	if err := scaffoldExampleSlice(root, bd, spec.DryRun); err != nil {
		return err
	}
	return scaffoldExampleContract(root, bd, "http", cellNoDash, spec.DryRun)
}

// scaffoldEventExampleArtifacts produces the event slice + contract pair.
// When the spec also requested HTTP (WithBoth), the slice already exists
// from the HTTP path; only the event contract is added.
func scaffoldEventExampleArtifacts(root string, spec ScaffoldSpec, cellNoDash, sliceID string) error {
	bd := bundleData{
		CellID:       spec.CellID,
		SlicePackage: sliceID,
		SliceID:      sliceID,
		SliceRole:    "publish",
		ContractID:   fmt.Sprintf("event.%s.example.v1", cellNoDash),
	}
	if !spec.WithBoth {
		if err := scaffoldExampleSlice(root, bd, spec.DryRun); err != nil {
			return err
		}
	}
	return scaffoldExampleContract(root, bd, "event", cellNoDash, spec.DryRun)
}

// sliceBundleFiles returns the canonical set of files emitted under each
// example slice; section names match {{define ...}} blocks in
// scaffold-slice.tmpl.
func sliceBundleFiles() []bundleFileSpec {
	return []bundleFileSpec{
		{Name: "slice.yaml", Section: "slice-yaml", IsGoSource: false, Description: "slice metadata"},
		{Name: "service.go", Section: "service-go", IsGoSource: true, Description: "slice business logic"},
		{Name: "service_test.go", Section: "service-test-go", IsGoSource: true, Description: "slice business logic test"},
	}
}

// scaffoldExampleSlice renders the slice triple (slice.yaml + service.go +
// service_test.go) under cells/{cellID}/slices/{sliceID}/. Conflict detection
// matches ScaffoldCell's all-or-nothing semantics.
func scaffoldExampleSlice(root string, bd bundleData, dryRun bool) error {
	dir := filepath.Join(root, "cells", bd.CellID, "slices", bd.SliceID)
	files := sliceBundleFiles()
	return renderBundleFiles(dir, files, sliceBundleTemplate, bd, dryRun, "slice")
}

// renderBundleFiles is the shared render→conflict-check→format→write pipeline
// for slice and contract bundle outputs. The kindLabel ("slice" / "contract")
// is used in error messages so callers can identify the failing step.
func renderBundleFiles(dir string, files []bundleFileSpec, tpl *template.Template, data any, dryRun bool, kindLabel string) error {
	for _, f := range files {
		path := filepath.Join(dir, f.Name)
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("scaffold %s: file already exists: %s", kindLabel, path)
		}
	}

	rendered, err := renderBundleSections(tpl, files, data, kindLabel)
	if err != nil {
		return err
	}

	if dryRun {
		return nil
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("scaffold %s: mkdir %s: %w", kindLabel, dir, err)
	}
	for name, content := range rendered {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			return fmt.Errorf("scaffold %s: write %s: %w", kindLabel, name, err)
		}
	}
	return nil
}

// renderBundleSections runs each file spec's template section through
// (Execute → optional FormatGoSource) and returns a map keyed by file name.
func renderBundleSections(tpl *template.Template, files []bundleFileSpec, data any, kindLabel string) (map[string][]byte, error) {
	rendered := make(map[string][]byte, len(files))
	for _, f := range files {
		var buf bytes.Buffer
		if err := tpl.ExecuteTemplate(&buf, f.Section, data); err != nil {
			return nil, fmt.Errorf("scaffold %s: render %s: %w", kindLabel, f.Description, err)
		}
		out := buf.Bytes()
		if f.IsGoSource {
			formatted, err := codegen.FormatGoSource("", out)
			if err != nil {
				return nil, fmt.Errorf("scaffold %s: format %s: %w", kindLabel, f.Name, err)
			}
			out = formatted
		}
		rendered[f.Name] = out
	}
	return rendered, nil
}

// bundleFileSpec parameterizes a single output file in a multi-section
// scaffold bundle: which template section to invoke, where to write, and
// whether the rendered bytes go through FormatGoSource.
type bundleFileSpec struct {
	Name        string
	Section     string
	IsGoSource  bool
	Description string
}

// scaffoldExampleContract renders the contract.yaml + 2 JSON schemas under
// contracts/{kind}/{cellPathSegment}/example/v1/. cellPathSegment is the
// dash-stripped cell ID (so contract.yaml IDs match Go package conventions).
// K#09 funnel: contract.yaml never embeds the `codegen:` field — kernel/
// metadata parser defaults it to true when absent.
func scaffoldExampleContract(root string, bd bundleData, kind, cellPathSegment string, dryRun bool) error {
	dir := filepath.Join(root, "contracts", kind, cellPathSegment, "example", "v1")
	files, err := contractBundleFiles(kind)
	if err != nil {
		return err
	}
	return renderBundleFiles(dir, files, contractBundleTemplate, bd, dryRun, "contract")
}

// contractBundleFiles returns the canonical files emitted for an example
// contract — split per kind (http vs event) since the schema artifact set
// differs.
func contractBundleFiles(kind string) ([]bundleFileSpec, error) {
	switch kind {
	case "http":
		return []bundleFileSpec{
			{Name: "contract.yaml", Section: "contract-yaml-http", Description: "contract metadata"},
			{Name: "request.schema.json", Section: "request-schema", Description: "request schema"},
			{Name: "response.schema.json", Section: "response-schema", Description: "response schema"},
		}, nil
	case "event":
		return []bundleFileSpec{
			{Name: "contract.yaml", Section: "contract-yaml-event", Description: "contract metadata"},
			{Name: "payload.schema.json", Section: "payload-schema", Description: "payload schema"},
			{Name: "headers.schema.json", Section: "headers-schema", Description: "headers schema"},
		}, nil
	default:
		return nil, fmt.Errorf("scaffold contract: unsupported kind %q", kind)
	}
}
