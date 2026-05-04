package cellgen

import (
	"embed"
	"fmt"
	"path/filepath"
	"sort"
	"text/template"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

// templates is parsed once at package init; the renderer reuses it for
// every cell to avoid re-parsing identical text on every call.
var templates = template.Must(template.ParseFS(templateFS, "templates/*.tmpl"))

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
		return res, fmt.Errorf("cellgen generate: root is empty")
	}
	if project == nil {
		return res, fmt.Errorf("cellgen generate: project is nil")
	}

	cellIDs := selectCellIDs(project, opts.OnlyCell)
	if opts.OnlyCell != "" && len(cellIDs) == 0 {
		return res, fmt.Errorf("cellgen generate: cell %q not found", opts.OnlyCell)
	}

	for _, id := range cellIDs {
		cell := project.Cells[id]
		// Skip cells without GoStructName — they have not opted into codegen
		// yet. The K#04 PR-1 enables only the whitelisted cells (currently
		// examples/todoorder/cells/ordercell). Other cells are skipped here
		// silently; archtest enforces that any cell matching the codegen
		// whitelist must declare GoStructName.
		if cell.GoStructName == "" {
			continue
		}

		spec, err := BuildCellSpec(project, id)
		if err != nil {
			return res, err
		}

		cellPath := cellGenPath(root, cell)
		content, err := codegen.Render(codegen.RenderOptions{
			TemplateName: "cell.tmpl",
			Templates:    templates,
			Data:         spec,
			Filename:     cellPath,
		})
		if err != nil {
			return res, fmt.Errorf("cellgen generate: render %s: %w", id, err)
		}
		writeRes, err := codegen.Write(codegen.WriteOptions{
			Path:     cellPath,
			Content:  content,
			RepoRoot: root,
			DryRun:   opts.DryRun,
			Verify:   opts.Verify,
		})
		if err != nil {
			return res, err
		}
		recordResult(&res, writeRes)

		// Slice gen
		sliceIDs := slicesForCellSorted(project, id)
		for _, sid := range sliceIDs {
			sliceSpec, err := BuildSliceSpec(project, id, sid)
			if err != nil {
				return res, err
			}
			if sliceSpec == nil {
				continue
			}
			slice := project.Slices[id+"/"+sid]
			slicePath := sliceGenPath(root, slice)
			content, err := codegen.Render(codegen.RenderOptions{
				TemplateName: "slice.tmpl",
				Templates:    templates,
				Data:         sliceSpec,
				Filename:     slicePath,
			})
			if err != nil {
				return res, fmt.Errorf("cellgen generate: render slice %s/%s: %w", id, sid, err)
			}
			writeRes, err := codegen.Write(codegen.WriteOptions{
				Path:     slicePath,
				Content:  content,
				RepoRoot: root,
				DryRun:   opts.DryRun,
				Verify:   opts.Verify,
			})
			if err != nil {
				return res, err
			}
			recordResult(&res, writeRes)
		}
	}
	return res, nil
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
