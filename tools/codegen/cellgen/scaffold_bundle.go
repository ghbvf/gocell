// scaffold_bundle.go implements K#09 PlanCellBundleScaffold: a one-shot scaffold
// planner that returns a merged []pathsafe.PlannedFile covering a compilable +
// testable cell skeleton. Composes planCell (cell.yaml + cell.go) with
// planHTTPExampleArtifacts / planEventExampleArtifacts (slice.yaml + service.go +
// service_test.go + contract.yaml + JSON schemas). When SkipGenerate is false,
// appendDerivedCodegenStaged (stage_render.go) appends derived codegen files.
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

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/pathsafe"
	"github.com/ghbvf/gocell/tools/codegen"
)

// ListenerMarker is the K#05 cell:listener marker literal embedded in
// scaffolded cell.go output. Templates reference this typed constant via
// {{.ListenerMarker}} so the marker-string → cell.yaml drift guard
// (MARKERGEN-DRIFT-VERIFY-01) is fed by a single source of truth.
//
// Hand-typing the marker literal in templates is statically rejected by
// archtest SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01.
const ListenerMarker = "// +cell:listener:"

// sliceBundleTemplate parses the multi-section scaffold-slice.tmpl which
// defines slice-yaml / service-go / service-test-go template blocks.
var sliceBundleTemplate = template.Must(template.New("scaffold-slice.tmpl").
	ParseFS(templateFS, "templates/scaffold-slice.tmpl"))

// contractBundleTemplate parses the multi-section scaffold-contract.tmpl which
// defines contract-yaml-http / contract-yaml-event / request-schema /
// response-schema / payload-schema / headers-schema template blocks.
var contractBundleTemplate = template.Must(template.New("scaffold-contract.tmpl").
	ParseFS(templateFS, "templates/scaffold-contract.tmpl"))

// projectionBundleTemplate parses scaffold-projection.tmpl which defines the
// projection slice.yaml, service.go, service_test.go, projection contract.yaml,
// and payload.schema.json template blocks.
var projectionBundleTemplate = template.Must(template.New("scaffold-projection.tmpl").
	ParseFS(templateFS, "templates/scaffold-projection.tmpl"))

// bundleData is the shared template context for slice + contract bundle
// templates. Computed once in planCellBundle from a ScaffoldSpec.
type bundleData struct {
	CellID         string
	SlicePackage   string // SliceID with no dashes (Go package name)
	SliceID        string
	SliceRole      string // "serve" for HTTP, "publish" for event
	ContractID     string // e.g. http.{id}.example.v1
	ListenerMarker string // K#05 cell:listener marker; sourced from ListenerMarker const
}

// PlanCellBundleScaffold is the K#09 one-shot scaffold planner. It produces a
// merged []pathsafe.PlannedFile covering:
//   - skeleton files (cell.go, cell.yaml, slice artifacts, contract artifacts)
//     with ForceOverwrite=false — conflict detection rejects pre-existing files.
//   - when spec.SkipGenerate is false: derived codegen files (cell_gen.go,
//     generated/contracts/.../types_gen.go, iface_gen.go, handler_gen.go)
//     with ForceOverwrite=true — always regenerated without conflict.
//
// Defaults: when neither WithHTTP nor WithEvents is set, WithHTTP applies.
// WithBoth produces both an HTTP contract and an event contract.
//
// realRoot must be the output of pathsafe.ResolveRoot. The returned plan is
// written by the caller via a single pathsafe.WritePlannedFiles call, ensuring
// all-or-nothing atomicity across skeleton + derived files.
//
// Mirrors kernel/assembly.Generator.PlanAssemblyScaffold + appendGeneratedFiles.
func PlanCellBundleScaffold(realRoot string, spec ScaffoldSpec) ([]pathsafe.PlannedFile, error) {
	if err := validateScaffoldSpec(spec); err != nil {
		return nil, err
	}

	skeletonPlan, err := planCellBundle(realRoot, spec)
	if err != nil {
		return nil, err
	}

	if spec.SkipGenerate {
		return skeletonPlan, nil
	}

	// Ephemeral staging: write skeleton into a temp dir (via pathsafe funnel),
	// parse metadata, render derived artifacts in-memory, rebase to realRoot.
	// The staging lifecycle (MkdirTemp + RemoveAll) is encapsulated in
	// appendDerivedCodegenStaged (stage_render.go) — os calls are not in
	// scaffold_bundle.go which is in the depguard scaffold-os-ban list.
	return appendDerivedCodegenStaged(realRoot, spec.CellID.String(), spec.ModulePath, skeletonPlan)
}

// minCellConsistencyLevel returns the minimum cell consistency level required
// for the given bundle variants. The mapping follows CLAUDE.md §一致性等级:
//
//   - withProjection (projection role) → L3 WorkflowEventual
//   - withEvents (publish role) → slice at least L2 → cell at least L2
//   - withHTTP only (serve role) → slice L1 → cell at least L1
//   - neither → L0
//
// Returns the minimum level as a string (e.g. "L2").
func minCellConsistencyLevel(withHTTP, withEvents, withProjection bool) string {
	if withProjection {
		return "L3"
	}
	if withEvents {
		return "L2"
	}
	if withHTTP {
		return "L1"
	}
	return "L0"
}

// consistencyLevelOrdinal maps a consistency level string to its ordinal for
// comparison. Higher ordinal = higher consistency requirement.
func consistencyLevelOrdinal(level string) int {
	switch level {
	case "L0":
		return 0
	case "L1":
		return 1
	case "L2":
		return 2
	case "L3":
		return 3
	case "L4":
		return 4
	default:
		return -1
	}
}

// validateBundleConsistencyLevel checks that spec.ConsistencyLevel is not
// lower than the minimum required by the bundle variants
// (withHTTP/withEvents/withProjection).
// Returns an error if the declared level is below the minimum.
func validateBundleConsistencyLevel(spec ScaffoldSpec) error {
	withHTTP, withEvents := resolveBundleVariants(spec)
	minLevel := minCellConsistencyLevel(withHTTP, withEvents, spec.WithProjection)
	declaredOrd := consistencyLevelOrdinal(spec.ConsistencyLevel)
	minOrd := consistencyLevelOrdinal(minLevel)
	if declaredOrd < minOrd {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"scaffold bundle: cell consistencyLevel is below minimum required by bundle variants",
			errcode.WithDetails(
				errcode.PublicString("declared", spec.ConsistencyLevel),
				errcode.PublicString("minimum", minLevel),
				errcode.PublicBool("withHTTP", withHTTP),
				errcode.PublicBool("withEvents", withEvents),
				errcode.PublicBool("withProjection", spec.WithProjection),
			))
	}
	return nil
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
		spec.ConsistencyLevel = "L2"
	}

	// Fail-fast: validate that cell.consistencyLevel >= minimum required by bundle variants.
	if err := validateBundleConsistencyLevel(spec); err != nil {
		return nil, err
	}

	var plan []pathsafe.PlannedFile

	// Cell skeleton (cell.go + cell.yaml).
	cellItems, err := planCell(realRoot, spec)
	if err != nil {
		return nil, err
	}
	plan = append(plan, cellItems...)

	withHTTP, withEvents := resolveBundleVariants(spec)
	// spec.CellID is scaffoldid.ScaffoldID — pattern ^[a-z][a-z0-9]+$ rules
	// out dashes at construction, so the legacy dash-stripping is now a no-op
	// kept as defense-in-depth in case the type ever loosens.
	cellNoDash := strings.ReplaceAll(spec.CellID.String(), "-", "")
	sliceID := cellNoDash + "example"

	if withHTTP {
		items, err := planHTTPExampleArtifacts(realRoot, spec, cellNoDash, sliceID)
		if err != nil {
			return nil, err
		}
		plan = append(plan, items...)
	}
	if withEvents {
		items, err := planEventExampleArtifacts(realRoot, spec, cellNoDash, sliceID, withHTTP)
		if err != nil {
			return nil, err
		}
		plan = append(plan, items...)
	}
	if spec.WithProjection {
		items, err := planProjectionExampleArtifacts(realRoot, spec, cellNoDash, withHTTP || withEvents)
		if err != nil {
			return nil, err
		}
		plan = append(plan, items...)
	}

	return plan, nil
}

// cellTemplateData wraps ScaffoldSpec with extra template-only fields so that
// scaffold-cell.tmpl can reference {{.ListenerMarker}} (SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01)
// without embedding the marker literal directly in the template.
type cellTemplateData struct {
	ScaffoldSpec
	ListenerMarker string
}

// planCell renders cell.go + cell.yaml and returns them as PlannedFiles.
func planCell(realRoot string, spec ScaffoldSpec) ([]pathsafe.PlannedFile, error) {
	cellData := cellTemplateData{
		ScaffoldSpec:   spec,
		ListenerMarker: ListenerMarker,
	}
	cellGoContent, err := renderTemplate(spec.ModulePath, cellGoTemplate, cellData, true)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "scaffold cell: render cell.go failed", err)
	}
	cellYAMLContent, err := renderTemplate(spec.ModulePath, cellYAMLTemplate, spec, false)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "scaffold cell: render cell.yaml failed", err)
	}

	targetDir := filepath.Join("cells", spec.CellID.String())
	absDir, err := pathsafe.ContainPath(realRoot, targetDir)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "scaffold cell: bundle plan failed", err)
	}

	plan := []pathsafe.PlannedFile{
		{AbsPath: filepath.Join(absDir, "cell.go"), Content: cellGoContent},
		{AbsPath: filepath.Join(absDir, "cell.yaml"), Content: cellYAMLContent},
	}

	archLayers, err := planInternalArchLayers(realRoot, spec.CellID.String(), spec.ModulePath)
	if err != nil {
		return nil, err
	}
	return append(plan, archLayers...), nil
}

// mandatoryInternalLayers are the two internal/ architecture layers every
// scaffolded cell starts with: ports (inward-facing interfaces) and mem
// (in-memory implementations for demo/test). Optional layers (domain / dto /
// events / adapters / testutil) are NOT scaffolded — they grow on demand (see
// .claude/rules/gocell/cell-patterns.md "internal/ 子包布局"). Seeding one
// doc.go starter per layer mirrors go-kratos/kratos-layout (one starter file
// per architecture layer); empty dirs are avoided because git does not track
// them and go-zero/goctl likewise never emits empty directories.
//
// ref: go-kratos/kratos-layout internal/{biz,data}.go; zeromicro/go-zero
// tools/goctl/rpc/generator/mkdir.go (every emitted dir holds a file).
var mandatoryInternalLayers = []struct{ pkg, summary string }{
	{"ports", "defines the repository and service interfaces for the %s cell.\n//\n" +
		"// Adapters (mem here, plus on-demand postgres/etc.) depend inward on these\n" +
		"// interfaces; the cell wires a concrete implementation at construction time."},
	{"mem", "provides in-memory implementations of the %s cell's ports interfaces,\n" +
		"// used by demo mode and tests. Production adapters are added on demand under\n" +
		"// internal/adapters/."},
}

// planInternalArchLayers renders the doc.go stub for each mandatory internal/
// architecture layer and returns them as PlannedFiles (written via the same
// pathsafe.WritePlannedFiles funnel as the rest of the bundle, per
// SCAFFOLD-WRITE-FUNNEL-01).
func planInternalArchLayers(realRoot, cellID, modulePath string) ([]pathsafe.PlannedFile, error) {
	items := make([]pathsafe.PlannedFile, 0, len(mandatoryInternalLayers))
	for _, l := range mandatoryInternalLayers {
		targetDir := filepath.Join("cells", cellID, "internal", l.pkg)
		absDir, err := pathsafe.ContainPath(realRoot, targetDir)
		if err != nil {
			return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
				"scaffold cell: internal layer path failed", err)
		}
		src := fmt.Sprintf("// Package %s %s\npackage %s\n", l.pkg, fmt.Sprintf(l.summary, cellID), l.pkg)
		formatted, err := codegen.FormatGoSource(modulePath, "", []byte(src))
		if err != nil {
			return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
				"scaffold cell: format internal layer doc.go failed", err)
		}
		items = append(items, pathsafe.PlannedFile{
			AbsPath: filepath.Join(absDir, "doc.go"),
			Content: formatted,
		})
	}
	return items, nil
}

// resolveBundleVariants picks the contract variants to scaffold from the
// spec's WithHTTP / WithEvents / WithBoth flags. When all three are unset
// AND WithProjection is also false (default) the bundle includes HTTP only.
// When WithProjection is the only flag set the default HTTP gate does NOT
// fire, keeping the projection-only output clean.
func resolveBundleVariants(spec ScaffoldSpec) (withHTTP, withEvents bool) {
	noneSet := !spec.WithHTTP && !spec.WithEvents && !spec.WithBoth
	withHTTP = spec.WithHTTP || spec.WithBoth || (noneSet && !spec.WithProjection)
	withEvents = spec.WithEvents || spec.WithBoth
	return withHTTP, withEvents
}

// planHTTPExampleArtifacts renders the HTTP slice + contract pair and returns
// them as PlannedFiles.
func planHTTPExampleArtifacts(realRoot string, spec ScaffoldSpec, cellNoDash, sliceID string) ([]pathsafe.PlannedFile, error) {
	bd := bundleData{
		CellID:         spec.CellID.String(),
		SlicePackage:   sliceID,
		SliceID:        sliceID,
		SliceRole:      "serve",
		ContractID:     fmt.Sprintf("http.%s.example.v1", cellNoDash),
		ListenerMarker: ListenerMarker,
	}
	sliceItems, err := planExampleSlice(realRoot, spec.ModulePath, bd)
	if err != nil {
		return nil, err
	}
	contractItems, err := planExampleContract(realRoot, spec.ModulePath, bd, "http", cellNoDash)
	if err != nil {
		return nil, err
	}
	return append(sliceItems, contractItems...), nil
}

// planEventExampleArtifacts renders the event slice + contract pair and returns
// them as PlannedFiles. When withHTTP is true (an HTTP slice is also present),
// uses a distinct event sliceID (cellNoDash+"eventexample") to avoid duplicate
// AbsPath collisions — gating on withHTTP rather than spec.WithBoth unifies the
// WithBoth path and the WithHTTP&&WithEvents path under one rule.
func planEventExampleArtifacts(
	realRoot string, spec ScaffoldSpec, cellNoDash, sliceID string, withHTTP bool,
) ([]pathsafe.PlannedFile, error) {
	eventSliceID := sliceID
	if withHTTP {
		eventSliceID = cellNoDash + "eventexample"
	}
	bd := bundleData{
		CellID:         spec.CellID.String(),
		SlicePackage:   eventSliceID,
		SliceID:        eventSliceID,
		SliceRole:      "publish",
		ContractID:     fmt.Sprintf("event.%s.example.v1", cellNoDash),
		ListenerMarker: ListenerMarker,
	}
	sliceItems, err := planExampleSlice(realRoot, spec.ModulePath, bd)
	if err != nil {
		return nil, err
	}
	contractItems, err := planExampleContract(realRoot, spec.ModulePath, bd, "event", cellNoDash)
	if err != nil {
		return nil, err
	}
	return append(sliceItems, contractItems...), nil
}

// projectionBundleData is the template context for projection slice + contract
// bundle templates. It extends bundleData with projection-specific fields.
type projectionBundleData struct {
	CellID       string
	CellNoDash   string // CellID with dashes removed — used in contract IDs / handler names
	SliceID      string
	SlicePackage string
	ProjectionID string // snake_case projection store key, e.g. "myprojcell_summary"
}

// planProjectionExampleArtifacts renders the projection slice + kind:projection
// contract pair, the event source contract (so the subscribe CU target exists),
// and the internal/projection/doc.go starter. Returns them as PlannedFiles.
//
// otherSlicePresent should be true when another slice variant (HTTP or event)
// is also being scaffolded in the same bundle, causing the event source slice
// to use a distinct sliceID (cellNoDash+"eventexample") to avoid AbsPath
// collisions — mirrors the same gate in planEventExampleArtifacts.
//
// Produced paths (relative to realRoot):
//
//	cells/{id}/slices/{id}projection/{slice.yaml,service.go,service_test.go}
//	contracts/projection/{id}/summary/v1/{contract.yaml,payload.schema.json}
//	contracts/event/{id}/example/v1/{contract.yaml,payload.schema.json,headers.schema.json}
//	cells/{id}/internal/projection/doc.go
func planProjectionExampleArtifacts(realRoot string, spec ScaffoldSpec, cellNoDash string, otherSlicePresent bool) ([]pathsafe.PlannedFile, error) {
	projSliceID := cellNoDash + "projection"
	projectionID := cellNoDash + "_summary"

	pd := projectionBundleData{
		CellID:       spec.CellID.String(),
		CellNoDash:   cellNoDash,
		SliceID:      projSliceID,
		SlicePackage: projSliceID,
		ProjectionID: projectionID,
	}

	// 1. Projection slice (slice.yaml + service.go + service_test.go).
	projSliceFiles := []bundleFileSpec{
		{Name: "slice.yaml", Section: "projection-slice-yaml", IsGoSource: false, Description: "projection slice metadata"},
		{Name: "service.go", Section: "projection-service-go", IsGoSource: true, Description: "projection service stub"},
		{Name: "service_test.go", Section: "projection-service-test-go", IsGoSource: true, Description: "projection service test"},
	}
	sliceItems, err := planBundleFiles(
		realRoot, spec.ModulePath,
		filepath.Join("cells", spec.CellID.String(), "slices", projSliceID),
		projSliceFiles, projectionBundleTemplate, pd, "projection-slice",
	)
	if err != nil {
		return nil, err
	}

	// 2. Projection contract (contract.yaml + payload.schema.json).
	projContractFiles := []bundleFileSpec{
		{Name: "contract.yaml", Section: "projection-contract-yaml", Description: "projection contract metadata"},
		{Name: "payload.schema.json", Section: "projection-payload-schema", Description: "projection payload schema"},
	}
	projContractItems, err := planBundleFiles(
		realRoot, spec.ModulePath,
		filepath.Join("contracts", "projection", cellNoDash, "summary", "v1"),
		projContractFiles, projectionBundleTemplate, pd, "projection-contract",
	)
	if err != nil {
		return nil, err
	}

	// 3. Event source contract so the subscribe CU target exists.
	// Pass otherSlicePresent as withHTTP to planEventExampleArtifacts so the
	// event slice gets a distinct sliceID when another slice variant (HTTP or
	// event) is also present in the same bundle — mirrors the same gate used
	// inside planCellBundle for the pure --with-events case.
	eventItems, err := planEventExampleArtifacts(realRoot, spec, cellNoDash, cellNoDash+"example", otherSlicePresent)
	if err != nil {
		return nil, err
	}

	// 4. internal/projection/doc.go starter.
	const projPkg = "projection"
	const projSummary = "is the read-model store layer for the L3 CQRS projection of the %s cell.\n" +
		"// It holds the materialised view built from consumed events and exposes\n" +
		"// typed queries for the projection contract provider."
	targetDir := filepath.Join("cells", spec.CellID.String(), "internal", projPkg)
	absDir, err := pathsafe.ContainPath(realRoot, targetDir)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"scaffold projection: internal layer path failed", err)
	}
	src := fmt.Sprintf("// Package %s %s\npackage %s\n",
		projPkg, fmt.Sprintf(projSummary, spec.CellID.String()), projPkg)
	formatted, err := codegen.FormatGoSource(spec.ModulePath, "", []byte(src))
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"scaffold projection: format internal/projection/doc.go failed", err)
	}

	var plan []pathsafe.PlannedFile
	plan = append(plan, sliceItems...)
	plan = append(plan, projContractItems...)
	plan = append(plan, eventItems...)
	plan = append(plan, pathsafe.PlannedFile{
		AbsPath: filepath.Join(absDir, "doc.go"),
		Content: formatted,
	})
	return plan, nil
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
func planExampleSlice(realRoot, modulePath string, bd bundleData) ([]pathsafe.PlannedFile, error) {
	targetDir := filepath.Join("cells", bd.CellID, "slices", bd.SliceID)
	files := sliceBundleFiles()
	return planBundleFiles(realRoot, modulePath, targetDir, files, sliceBundleTemplate, bd, "slice")
}

// planBundleFiles is the shared render→format→plan pipeline for slice and
// contract bundle outputs. The kindLabel ("slice" / "contract") is used in
// error messages. Returns PlannedFiles without touching the filesystem.
func planBundleFiles(
	realRoot, modulePath, targetDir string,
	files []bundleFileSpec,
	tpl *template.Template,
	data any,
	kindLabel string,
) ([]pathsafe.PlannedFile, error) {
	absDir, err := pathsafe.ContainPath(realRoot, targetDir)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"scaffold bundle: plan path failed", err,
			errcode.WithDetails(errcode.PublicString("kind", kindLabel)))
	}

	rendered, err := renderBundleSections(modulePath, tpl, files, data, kindLabel)
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
func renderBundleSections(
	modulePath string, tpl *template.Template, files []bundleFileSpec, data any, kindLabel string,
) (map[string][]byte, error) {
	rendered := make(map[string][]byte, len(files))
	for _, f := range files {
		var buf bytes.Buffer
		if err := tpl.ExecuteTemplate(&buf, f.Section, data); err != nil {
			return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
				"scaffold bundle: render artifact failed", err,
				errcode.WithDetails(
					errcode.PublicString("kind", kindLabel),
					errcode.PublicString("artifact", f.Description),
				))
		}
		out := buf.Bytes()
		if f.IsGoSource {
			formatted, err := codegen.FormatGoSource(modulePath, "", out)
			if err != nil {
				return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
					"scaffold bundle: format artifact failed", err,
					errcode.WithDetails(
						errcode.PublicString("kind", kindLabel),
						errcode.PublicString("artifact", f.Name),
					))
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
func planExampleContract(realRoot, modulePath string, bd bundleData, kind, cellPathSegment string) ([]pathsafe.PlannedFile, error) {
	targetDir := filepath.Join("contracts", kind, cellPathSegment, "example", "v1")
	files, err := contractBundleFiles(kind)
	if err != nil {
		return nil, err
	}
	return planBundleFiles(realRoot, modulePath, targetDir, files, contractBundleTemplate, bd, "contract")
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
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"scaffold contract: unsupported kind",
			errcode.WithDetails(errcode.PublicString("kind", kind)))
	}
}
