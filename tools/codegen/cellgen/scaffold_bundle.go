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
	"path/filepath"
	"strings"
	"text/template"

	"github.com/ghbvf/gocell/pkg/pathsafe"
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
// Writes are atomic: all files are planned first, then written in a single
// pathsafe.WritePlannedFiles call. On failure the entire bundle is rolled back.
func ScaffoldCellBundle(root string, spec ScaffoldSpec) error {
	if err := validateScaffoldSpec(spec); err != nil {
		return err
	}

	realRoot, err := pathsafe.ResolveRoot(root)
	if err != nil {
		return fmt.Errorf("scaffold bundle: %w", err)
	}

	plan, err := planCellBundle(realRoot, spec)
	if err != nil {
		return err
	}

	if err := pathsafe.WritePlannedFiles(realRoot, plan, spec.DryRun); err != nil {
		return fmt.Errorf("scaffold bundle: %w", err)
	}
	return nil
}

// PlanCellBundleForDryRun is the exported equivalent of planCellBundle,
// allowing callers (e.g. scaffoldCell dry-run in cmd/gocell/app) to enumerate
// the full file list without writing anything. realRoot must be the output of
// pathsafe.ResolveRoot.
func PlanCellBundleForDryRun(realRoot string, spec ScaffoldSpec) ([]pathsafe.PlannedFile, error) {
	return planCellBundle(realRoot, spec)
}

// planCellBundle builds the full []pathsafe.PlannedFile for a cell bundle
// (cell skeleton + example slice(s) + example contract(s)) without writing
// any files. All template rendering happens here.
func planCellBundle(realRoot string, spec ScaffoldSpec) ([]pathsafe.PlannedFile, error) {
	// Apply defaults.
	if spec.Type == "" {
		spec.Type = "core"
	}
	if spec.ConsistencyLevel == "" {
		spec.ConsistencyLevel = "L1"
	}

	var plan []pathsafe.PlannedFile

	// Cell skeleton (cell.go + cell.yaml).
	cellItems, err := planCell(realRoot, spec)
	if err != nil {
		return nil, err
	}
	plan = append(plan, cellItems...)

	withHTTP, withEvents := resolveBundleVariants(spec)
	cellNoDash := strings.ReplaceAll(spec.CellID, "-", "")
	sliceID := cellNoDash + "example"

	if withHTTP {
		items, err := planHTTPExampleArtifacts(realRoot, spec, cellNoDash, sliceID)
		if err != nil {
			return nil, err
		}
		plan = append(plan, items...)
	}
	if withEvents {
		items, err := planEventExampleArtifacts(realRoot, spec, cellNoDash, sliceID)
		if err != nil {
			return nil, err
		}
		plan = append(plan, items...)
	}

	return plan, nil
}

// planCell renders cell.go + cell.yaml and returns them as PlannedFiles.
func planCell(realRoot string, spec ScaffoldSpec) ([]pathsafe.PlannedFile, error) {
	cellGoContent, err := renderTemplate(cellGoTemplate, spec, true)
	if err != nil {
		return nil, fmt.Errorf("scaffold cell: render cell.go: %w", err)
	}
	cellYAMLContent, err := renderTemplate(cellYAMLTemplate, spec, false)
	if err != nil {
		return nil, fmt.Errorf("scaffold cell: render cell.yaml: %w", err)
	}

	targetDir := filepath.Join("cells", spec.CellID)
	absDir, err := pathsafe.ContainPath(realRoot, targetDir)
	if err != nil {
		return nil, fmt.Errorf("scaffold cell: %w", err)
	}

	return []pathsafe.PlannedFile{
		{AbsPath: filepath.Join(absDir, "cell.go"), Content: cellGoContent},
		{AbsPath: filepath.Join(absDir, "cell.yaml"), Content: cellYAMLContent},
	}, nil
}

// resolveBundleVariants picks the contract variants to scaffold from the
// spec's WithHTTP / WithEvents / WithBoth flags. When all three are unset
// (default) the bundle includes HTTP only.
func resolveBundleVariants(spec ScaffoldSpec) (withHTTP, withEvents bool) {
	withHTTP = spec.WithHTTP || spec.WithBoth || (!spec.WithHTTP && !spec.WithEvents && !spec.WithBoth)
	withEvents = spec.WithEvents || spec.WithBoth
	return withHTTP, withEvents
}

// planHTTPExampleArtifacts renders the HTTP slice + contract pair and returns
// them as PlannedFiles.
func planHTTPExampleArtifacts(realRoot string, spec ScaffoldSpec, cellNoDash, sliceID string) ([]pathsafe.PlannedFile, error) {
	bd := bundleData{
		CellID:       spec.CellID,
		SlicePackage: sliceID,
		SliceID:      sliceID,
		SliceRole:    "serve",
		ContractID:   fmt.Sprintf("http.%s.example.v1", cellNoDash),
	}
	sliceItems, err := planExampleSlice(realRoot, bd)
	if err != nil {
		return nil, err
	}
	contractItems, err := planExampleContract(realRoot, bd, "http", cellNoDash)
	if err != nil {
		return nil, err
	}
	return append(sliceItems, contractItems...), nil
}

// planEventExampleArtifacts renders the event slice + contract pair and returns
// them as PlannedFiles. When spec.WithBoth, uses a separate event slice ID.
func planEventExampleArtifacts(realRoot string, spec ScaffoldSpec, cellNoDash, sliceID string) ([]pathsafe.PlannedFile, error) {
	eventSliceID := sliceID
	if spec.WithBoth {
		eventSliceID = cellNoDash + "eventexample"
	}
	bd := bundleData{
		CellID:       spec.CellID,
		SlicePackage: eventSliceID,
		SliceID:      eventSliceID,
		SliceRole:    "publish",
		ContractID:   fmt.Sprintf("event.%s.example.v1", cellNoDash),
	}
	sliceItems, err := planExampleSlice(realRoot, bd)
	if err != nil {
		return nil, err
	}
	contractItems, err := planExampleContract(realRoot, bd, "event", cellNoDash)
	if err != nil {
		return nil, err
	}
	return append(sliceItems, contractItems...), nil
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

// planExampleSlice renders the slice triple (slice.yaml + service.go +
// service_test.go) under cells/{cellID}/slices/{sliceID}/ and returns them
// as PlannedFiles. No filesystem writes occur here.
func planExampleSlice(realRoot string, bd bundleData) ([]pathsafe.PlannedFile, error) {
	targetDir := filepath.Join("cells", bd.CellID, "slices", bd.SliceID)
	files := sliceBundleFiles()
	return planBundleFiles(realRoot, targetDir, files, sliceBundleTemplate, bd, "slice")
}

// planBundleFiles is the shared render→format→plan pipeline for slice and
// contract bundle outputs. The kindLabel ("slice" / "contract") is used in
// error messages. Returns PlannedFiles without touching the filesystem.
func planBundleFiles(realRoot, targetDir string, files []bundleFileSpec, tpl *template.Template, data any, kindLabel string) ([]pathsafe.PlannedFile, error) {
	absDir, err := pathsafe.ContainPath(realRoot, targetDir)
	if err != nil {
		return nil, fmt.Errorf("scaffold %s: %w", kindLabel, err)
	}

	rendered, err := renderBundleSections(tpl, files, data, kindLabel)
	if err != nil {
		return nil, err
	}

	items := make([]pathsafe.PlannedFile, 0, len(files))
	for _, f := range files {
		items = append(items, pathsafe.PlannedFile{
			AbsPath: filepath.Join(absDir, f.Name),
			Content: rendered[f.Name],
		})
	}
	return items, nil
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

// planExampleContract renders contract.yaml + JSON schemas under
// contracts/{kind}/{cellPathSegment}/example/v1/ and returns them as
// PlannedFiles. K#09 funnel: contract.yaml never embeds the `codegen:` field.
func planExampleContract(realRoot string, bd bundleData, kind, cellPathSegment string) ([]pathsafe.PlannedFile, error) {
	targetDir := filepath.Join("contracts", kind, cellPathSegment, "example", "v1")
	files, err := contractBundleFiles(kind)
	if err != nil {
		return nil, err
	}
	return planBundleFiles(realRoot, targetDir, files, contractBundleTemplate, bd, "contract")
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
