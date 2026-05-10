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
//
//nolint:gocognit,cyclop // sequential bundle composition; complexity intrinsic
func ScaffoldCellBundle(root string, spec ScaffoldSpec) error {
	if err := validateScaffoldSpec(spec); err != nil {
		return err
	}

	// Resolve bundle defaults: if no variant chosen, default to HTTP.
	withHTTP := spec.WithHTTP || spec.WithBoth || (!spec.WithHTTP && !spec.WithEvents && !spec.WithBoth)
	withEvents := spec.WithEvents || spec.WithBoth

	// Step 1 — cell skeleton (cell.go + cell.yaml). Reuse existing ScaffoldCell.
	cellTarget := filepath.Join("cells", spec.CellID)
	if err := ScaffoldCell(root, cellTarget, spec); err != nil {
		return err
	}

	// Step 2 — example slice (slice.yaml + service.go + service_test.go).
	// Slice ID convention: {cellID-no-dash}example. Cell IDs may contain
	// dashes (e.g. "test-cell") but Go package names and slice IDs cannot,
	// so the dash-stripped form is used for both.
	cellNoDash := strings.ReplaceAll(spec.CellID, "-", "")
	sliceID := cellNoDash + "example"
	slicePackage := sliceID // Go package = slice ID

	if withHTTP {
		bd := bundleData{
			CellID:       spec.CellID,
			SlicePackage: slicePackage,
			SliceID:      sliceID,
			SliceRole:    "serve",
			ContractID:   fmt.Sprintf("http.%s.example.v1", cellNoDash),
		}
		if err := scaffoldExampleSlice(root, bd, spec.DryRun); err != nil {
			return err
		}
		if err := scaffoldExampleContract(root, bd, "http", cellNoDash, spec.DryRun); err != nil {
			return err
		}
	}
	if withEvents {
		// For event-only or both, slice & contract use event role.
		bd := bundleData{
			CellID:       spec.CellID,
			SlicePackage: slicePackage,
			SliceID:      sliceID,
			SliceRole:    "publish",
			ContractID:   fmt.Sprintf("event.%s.example.v1", cellNoDash),
		}
		// In WithBoth, slice already exists from HTTP step; re-render would
		// fail conflict detection. Skip slice in event branch when both set.
		if !spec.WithBoth {
			if err := scaffoldExampleSlice(root, bd, spec.DryRun); err != nil {
				return err
			}
		}
		if err := scaffoldExampleContract(root, bd, "event", cellNoDash, spec.DryRun); err != nil {
			return err
		}
	}
	return nil
}

// scaffoldExampleSlice renders the slice triple (slice.yaml + service.go +
// service_test.go) under cells/{cellID}/slices/{sliceID}/. Conflict detection
// matches ScaffoldCell's all-or-nothing semantics.
func scaffoldExampleSlice(root string, bd bundleData, dryRun bool) error {
	dir := filepath.Join(root, "cells", bd.CellID, "slices", bd.SliceID)
	files := []struct {
		name        string
		section     string
		isGoSource  bool
		description string
	}{
		{"slice.yaml", "slice-yaml", false, "slice metadata"},
		{"service.go", "service-go", true, "slice business logic"},
		{"service_test.go", "service-test-go", true, "slice business logic test"},
	}

	// Conflict detection — any pre-existing file aborts the whole step.
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("scaffold slice: file already exists: %s", path)
		}
	}

	rendered := make(map[string][]byte, len(files))
	for _, f := range files {
		var buf bytes.Buffer
		if err := sliceBundleTemplate.ExecuteTemplate(&buf, f.section, bd); err != nil {
			return fmt.Errorf("scaffold slice: render %s: %w", f.description, err)
		}
		out := buf.Bytes()
		if f.isGoSource {
			formatted, err := codegen.FormatGoSource("", out)
			if err != nil {
				return fmt.Errorf("scaffold slice: format %s: %w", f.name, err)
			}
			out = formatted
		}
		rendered[f.name] = out
	}

	if dryRun {
		return nil
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("scaffold slice: mkdir %s: %w", dir, err)
	}
	for name, content := range rendered {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			return fmt.Errorf("scaffold slice: write %s: %w", name, err)
		}
	}
	return nil
}

// scaffoldExampleContract renders the contract.yaml + 2 JSON schemas under
// contracts/{kind}/{cellPathSegment}/example/v1/. cellPathSegment is the
// dash-stripped cell ID (so contract.yaml IDs match Go package conventions).
// K#09 funnel: contract.yaml never embeds the `codegen:` field — kernel/
// metadata parser defaults it to true when absent.
func scaffoldExampleContract(root string, bd bundleData, kind, cellPathSegment string, dryRun bool) error {
	dir := filepath.Join(root, "contracts", kind, cellPathSegment, "example", "v1")

	type fileSpec struct {
		name    string
		section string
	}
	var files []fileSpec
	switch kind {
	case "http":
		files = []fileSpec{
			{"contract.yaml", "contract-yaml-http"},
			{"request.schema.json", "request-schema"},
			{"response.schema.json", "response-schema"},
		}
	case "event":
		files = []fileSpec{
			{"contract.yaml", "contract-yaml-event"},
			{"payload.schema.json", "payload-schema"},
			{"headers.schema.json", "headers-schema"},
		}
	default:
		return fmt.Errorf("scaffold contract: unsupported kind %q", kind)
	}

	for _, f := range files {
		path := filepath.Join(dir, f.name)
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("scaffold contract: file already exists: %s", path)
		}
	}

	rendered := make(map[string][]byte, len(files))
	for _, f := range files {
		var buf bytes.Buffer
		if err := contractBundleTemplate.ExecuteTemplate(&buf, f.section, bd); err != nil {
			return fmt.Errorf("scaffold contract: render %s: %w", f.name, err)
		}
		rendered[f.name] = buf.Bytes()
	}

	if dryRun {
		return nil
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("scaffold contract: mkdir %s: %w", dir, err)
	}
	for name, content := range rendered {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			return fmt.Errorf("scaffold contract: write %s: %w", name, err)
		}
	}
	return nil
}
