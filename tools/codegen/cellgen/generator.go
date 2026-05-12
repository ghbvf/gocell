package cellgen

import (
	"embed"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/tools/codegen"
	"github.com/ghbvf/gocell/tools/codegen/markergen"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

// templates is parsed once at package init by cloning the shared header
// template set (codegen.SharedTemplates) and layering cellgen-local
// templates (cell.tmpl, slice.tmpl) on top. This gives cell.tmpl and
// slice.tmpl access to the "header" template definition from
// tools/codegen/templates/header.tmpl without embedding it in the
// cellgen subpackage (which would break future sibling subpackages
// contractgen / markergen that also need the shared header).
//
// CELLGEN-LITERAL-FUNNEL-02: no FuncMap. The CellMeta literal is pre-rendered
// into CellGenSpec.RenderedMetaLiteral at build time so cell.tmpl emits the
// string directly without invoking any template function (and therefore
// cannot reach the *metadata.CellMeta struct to hand-enumerate fields).
var templates = mustParseTemplates()

func mustParseTemplates() *template.Template {
	cloned := template.Must(codegen.SharedTemplates.Clone())
	return template.Must(cloned.ParseFS(templateFS, "templates/*.tmpl"))
}

// Options controls a Generate run.
type Options struct {
	// DryRun emits ActionWouldWrite without filesystem mutation.
	DryRun bool
	// Verify diffs the rendered content against disk and reports drift.
	// Mutually exclusive with DryRun at the CLI layer; combining them here
	// is harmless (Verify dominates — no write either way).
	Verify bool
	// OnlyCell, when non-empty, restricts generation to a single cell id.
	// Empty = generate for every cell in project.
	OnlyCell string
}

// Result aggregates per-call outcomes for CLI reporting.
type Result struct {
	// Generated lists files that were written, would-have-been-written
	// (DryRun), or remain unchanged (Unchanged).
	Generated []string
	// Drifted lists files whose disk content differs from the freshly
	// rendered content (Verify mode).
	Drifted []string
}

// GeneratedFiles satisfies the cmd/gocell/app.CodegenResult interface.
func (r Result) GeneratedFiles() []string { return r.Generated }

// DriftedFiles satisfies the cmd/gocell/app.CodegenResult interface.
func (r Result) DriftedFiles() []string { return r.Drifted }

// Generate runs the cellgen pipeline against the parsed project.
//
//   - Selects cells: OnlyCell or all in project.Cells (deterministically ordered).
//   - For each cell: BuildCellSpec → Render(cell.tmpl) → Write cell_gen.go.
//   - For each slice with Subscribes: BuildSliceSpec → Render(slice.tmpl) →
//     Write slice_gen.go. Slices without subscribes do not produce output.
//
// Returns a Result describing per-file actions. The error return is non-nil
// only for hard failures (spec invalid, template execution, write IO);
// drift in Verify mode populates Result.Drifted without an error.
func Generate(root string, project *metadata.ProjectMeta, opts Options) (Result, error) {
	var res Result
	if root == "" {
		return res, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen generate: root is empty")
	}
	if project == nil {
		return res, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen generate: project is nil")
	}

	cellIDs := selectCellIDs(project, opts.OnlyCell)
	if opts.OnlyCell != "" && len(cellIDs) == 0 {
		return res, errcode.New(errcode.KindNotFound, errcode.ErrCellNotFound,
			"cellgen generate: cell not found",
			errcode.WithDetails(slog.String("cellID", opts.OnlyCell)))
	}

	// K#05 W2: read wire declarations directly from cell.go marker comments
	// via markergen.Merge. Builder receives WireBundle per cell.
	//
	// K05-09: When OnlyCell is set, filter the project view to the target cell
	// so unrelated cells' marker errors don't block single-cell generation.
	mergeProject := project
	if opts.OnlyCell != "" {
		mergeProject = projectFilteredToCell(project, opts.OnlyCell)
	}
	bundles, err := markergen.Merge(root, mergeProject)
	if err != nil {
		return res, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"cellgen generate: markergen merge", err)
	}

	for _, id := range cellIDs {
		cell := project.Cells[id]
		// Skip cells without GoStructName — they have not opted into codegen
		// yet. The K#04 PR-1 enables only the whitelisted cells (currently
		// examples/todoorder/cells/ordercell). Other cells are skipped here;
		// archtest enforces that any cell matching the codegen whitelist must
		// declare GoStructName. Emit a warning to stderr (unless in verify
		// mode, which is silent on opt-out cells by design).
		if cell.GoStructName.IsZero() {
			if !opts.Verify {
				slog.Info("cellgen: skipping cell (no goStructName)", slog.String("cell_id", cell.ID))
			}
			continue
		}
		if err := generateOneCell(root, project, cell, bundles[id], opts, &res); err != nil {
			return res, err
		}
	}
	return res, nil
}

// generateOneCell renders cell_gen.go and per-slice slice_gen.go for the
// given cell, appending outcomes to res.
func generateOneCell(
	root string,
	project *metadata.ProjectMeta,
	cell *metadata.CellMeta,
	bundle markergen.WireBundle,
	opts Options,
	res *Result,
) error {
	spec, err := BuildCellSpec(project, cell.ID, bundle)
	if err != nil {
		return err
	}
	// Enrich subscriptions with generated-package import paths derived from go.mod.
	if len(spec.Subscriptions) > 0 {
		modulePath, modErr := readModulePath(root)
		if modErr != nil {
			return modErr
		}
		EnrichSubscriptionsWithModulePath(spec, modulePath)
	}
	if err := renderAndWrite(root, "cell.tmpl", spec, cellGenPath(root, cell), opts, res, "cellgen generate: render "+cell.ID); err != nil {
		return err
	}
	for _, sid := range slicesForCellSorted(project, cell.ID) {
		sliceSpec, err := BuildSliceSpec(project, cell.ID, sid, bundle)
		if err != nil {
			return err
		}
		if sliceSpec == nil {
			continue
		}
		slice := project.Slices[cell.ID+"/"+sid]
		errPrefix := "cellgen generate: render slice " + cell.ID + "/" + sid
		if err := renderAndWrite(root, "slice.tmpl", sliceSpec, sliceGenPath(root, slice), opts, res, errPrefix); err != nil {
			return err
		}
	}
	return nil
}

// CellArtifact is a (RelPath, Kind, Content) triple describing one rendered
// cellgen output for a single cell. Returned by RenderCellArtifacts so other
// tools (notably tools/generatedverify) can build a project-derived expected
// manifest without going through the filesystem-mutating Generate path.
type CellArtifact struct {
	// Kind is "cell-gen" or "slice-gen".
	Kind string
	// RelPath is the project-relative path the file would be written to,
	// e.g. "examples/todoorder/cells/ordercell/cell_gen.go".
	RelPath string
	// Content is the rendered, formatted, goimports-processed bytes.
	Content []byte
}

// RenderCellArtifacts renders the cellgen output for a single cell to memory
// without touching disk. Returns one CellArtifact per produced file (one
// cell_gen.go plus one slice_gen.go per slice with subscribes). Cells
// without GoStructName return (nil, nil) — same opt-in semantics as Generate.
//
// Implementation note: render must be ordered cell→slices in a single pass
// because the cell template's imports are inferred from per-slice subscribes;
// extracting per-slice render into a helper duplicates the import accumulator.
//
//nolint:gocognit,funlen // render+slices in one pass; splitting duplicates import accumulator (see comment above)
func RenderCellArtifacts(root string, project *metadata.ProjectMeta, cellID string) ([]CellArtifact, error) {
	if project == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"cellgen render artifacts: project is nil")
	}
	cell, ok := project.Cells[cellID]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrCellNotFound,
			"cellgen render artifacts: cell not found",
			errcode.WithDetails(slog.String("cellID", cellID)))
	}
	if cell.GoStructName.IsZero() {
		return nil, nil
	}

	bundles, err := markergen.Merge(root, project)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"cellgen render artifacts: markergen merge", err)
	}
	bundle := bundles[cellID]

	var out []CellArtifact

	cellSpec, err := BuildCellSpec(project, cellID, bundle)
	if err != nil {
		return nil, err
	}
	// Enrich subscriptions with generated-package import paths derived from go.mod.
	if len(cellSpec.Subscriptions) > 0 {
		modulePath, modErr := readModulePath(root)
		if modErr != nil {
			return nil, modErr
		}
		EnrichSubscriptionsWithModulePath(cellSpec, modulePath)
	}
	cellAbs := cellGenPath(root, cell)
	cellContent, err := codegen.Render(codegen.RenderOptions{
		TemplateName: "cell.tmpl",
		Templates:    templates,
		Data:         cellSpec,
		Filename:     cellAbs,
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"cellgen render artifacts: cell render failed",
			err,
			errcode.WithDetails(slog.String("cellID", cellID)))
	}
	cellRel, err := relFromRoot(root, cellAbs)
	if err != nil {
		return nil, err
	}
	out = append(out, CellArtifact{Kind: "cell-gen", RelPath: cellRel, Content: cellContent})

	for _, sid := range slicesForCellSorted(project, cellID) {
		sliceSpec, err := BuildSliceSpec(project, cellID, sid, bundle)
		if err != nil {
			return nil, err
		}
		if sliceSpec == nil {
			continue
		}
		slice := project.Slices[cellID+"/"+sid]
		sliceAbs := sliceGenPath(root, slice)
		sliceContent, err := codegen.Render(codegen.RenderOptions{
			TemplateName: "slice.tmpl",
			Templates:    templates,
			Data:         sliceSpec,
			Filename:     sliceAbs,
		})
		if err != nil {
			return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
				"cellgen render artifacts: slice render failed",
				err,
				errcode.WithDetails(
					slog.String("cellID", cellID),
					slog.String("sliceID", sid),
				))
		}
		sliceRel, err := relFromRoot(root, sliceAbs)
		if err != nil {
			return nil, err
		}
		out = append(out, CellArtifact{Kind: "slice-gen", RelPath: sliceRel, Content: sliceContent})
	}
	return out, nil
}

// relFromRoot converts an absolute path under root into a slash-separated
// relative path. Returns an error if the path escapes root.
func relFromRoot(root, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"cellgen: compute relative path failed",
			err,
			errcode.WithInternal(fmt.Sprintf("abs=%s root=%s", abs, root)))
	}
	return filepath.ToSlash(rel), nil
}

// renderAndWrite is the shared (render → write → record) tail used by both
// the cell and slice render paths.
func renderAndWrite(root, tmpl string, data any, path string, opts Options, res *Result, errPrefix string) error {
	content, err := codegen.Render(codegen.RenderOptions{
		TemplateName: tmpl,
		Templates:    templates,
		Data:         data,
		Filename:     path,
	})
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"cellgen: render failed",
			err,
			errcode.WithInternal(errPrefix))
	}
	writeRes, err := codegen.Write(codegen.WriteOptions{
		Path:     path,
		Content:  content,
		RepoRoot: root,
		DryRun:   opts.DryRun,
		Verify:   opts.Verify,
	})
	if err != nil {
		return err
	}
	recordResult(res, writeRes)
	return nil
}

// selectCellIDs returns the deterministic ordered list of cell ids to process.
func selectCellIDs(p *metadata.ProjectMeta, only string) []string {
	if only != "" {
		if _, ok := p.Cells[only]; ok {
			return []string{only}
		}
		return nil
	}
	ids := make([]string, 0, len(p.Cells))
	for id := range p.Cells {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// slicesForCellSorted returns sliceIDs belonging to cellID in deterministic order.
func slicesForCellSorted(p *metadata.ProjectMeta, cellID string) []string {
	var out []string
	for _, s := range p.Slices {
		if s.BelongsToCell == cellID {
			out = append(out, s.ID)
		}
	}
	sort.Strings(out)
	return out
}

// cellGenPath converts a CellMeta.File ("examples/X/cells/Y/cell.yaml") to
// the absolute cell_gen.go path under root.
func cellGenPath(root string, cell *metadata.CellMeta) string {
	dir := filepath.Dir(cell.File)
	return filepath.Join(root, dir, "cell_gen.go")
}

// sliceGenPath converts a SliceMeta.File to the absolute slice_gen.go path.
func sliceGenPath(root string, slice *metadata.SliceMeta) string {
	dir := filepath.Dir(slice.File)
	return filepath.Join(root, dir, "slice_gen.go")
}

func recordResult(res *Result, w codegen.WriteResult) {
	switch w.Action {
	case codegen.ActionDrifted:
		res.Drifted = append(res.Drifted, w.Path)
	default:
		res.Generated = append(res.Generated, w.Path)
	}
}

// projectFilteredToCell returns a shallow copy of project containing only the
// target cell and its slices. This is used by Generate when OnlyCell is set so
// that markergen.Merge only parses the target cell's cell.go, preventing
// unrelated cells' marker errors from blocking single-cell generation.
//
// The returned ProjectMeta shares the same Contracts map (read-only in this
// context) and shallow-copies Cells and Slices to the filtered set.
func projectFilteredToCell(project *metadata.ProjectMeta, cellID string) *metadata.ProjectMeta {
	filtered := &metadata.ProjectMeta{
		Cells:     make(map[string]*metadata.CellMeta, 1),
		Slices:    make(map[string]*metadata.SliceMeta),
		Contracts: project.Contracts,
	}
	if cell, ok := project.Cells[cellID]; ok {
		filtered.Cells[cellID] = cell
	}
	prefix := cellID + "/"
	for key, s := range project.Slices {
		if strings.HasPrefix(key, prefix) {
			filtered.Slices[key] = s
		}
	}
	return filtered
}
