package app

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/csvparam"
	"github.com/ghbvf/gocell/runtime/devtools/catalog"
	"github.com/ghbvf/gocell/tools/depgraph"
)

// validIncludeTokens is the set of accepted --include= tokens.
var validIncludeTokens = []string{"cellDeps", "packageDeps", "relations", "statusBoard"}

// validKinds is the set of accepted --kinds= tokens.
// References catalog.AllKinds — single source of truth.
var validKinds = catalog.AllKinds

// validLayers is the set of accepted --layers= tokens.
// References catalog.AllLayers — single source of truth.
var validLayersList = catalog.AllLayers

// runExport dispatches `export <subcommand>` to its handler. catalog and
// metadata are byte-equal aliases sharing exportCatalog as the implementation.
func runExport(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gocell export <catalog|metadata> [flags]")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "catalog", "metadata":
		return exportCatalog(rest)
	default:
		return fmt.Errorf("unknown export subcommand %q (want catalog|metadata)", sub)
	}
}

func exportCatalog(args []string) error {
	fs := flag.NewFlagSet("export catalog", flag.ContinueOnError)
	format := fs.String("format", "json", "output format: json|yaml")
	out := fs.String("out", "", "output path; empty = stdout")
	include := fs.String("include", "cellDeps,packageDeps,statusBoard,relations", "comma-separated optional blocks (default all)")
	kinds := fs.String("kinds", "", "comma-separated entity kinds; empty = all")
	layers := fs.String("layers", "", "comma-separated layers; empty = all")
	cellsArg := fs.String("cells", "", "comma-separated cell IDs to focus on (with first-order neighbors); empty = all")
	root := fs.String("root", "", "project root directory; empty triggers go.mod auto-detection (walks up from cwd to find nearest go.mod)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *format != "json" && *format != "yaml" {
		return fmt.Errorf("export: unknown format %q (want json|yaml)", *format)
	}

	rootDir, err := resolveRoot(*root)
	if err != nil {
		return err
	}

	pm, err := loadProjectMeta(rootDir)
	if err != nil {
		return err
	}

	filter, err := buildFilter(*kinds, *layers, *cellsArg, *include)
	if err != nil {
		return err
	}

	opts := catalog.ExportOptions{
		Clock:  clock.Real(),
		Root:   rootDir,
		Filter: filter,
	}

	if err := attachCellDeps(&opts, filter, pm); err != nil {
		return err
	}

	if err := attachPackageDeps(&opts, filter, rootDir); err != nil {
		return err
	}

	doc, err := catalog.BuildDocument(pm, opts)
	if err != nil {
		return err
	}

	body, err := catalog.MarshalDocument(doc, *format)
	if err != nil {
		return err
	}

	return writeOut(*out, body)
}

// attachCellDeps populates opts.CellDeps when CellDeps is set in the filter.
func attachCellDeps(opts *catalog.ExportOptions, filter catalog.Filter, pm *metadata.ProjectMeta) error {
	if !filter.Include.CellDeps {
		return nil
	}
	cd, errs := governance.NewDependencyChecker(pm).Graph()
	if len(errs) > 0 {
		return fmt.Errorf("cell dep graph build had validation errors:\n%s", formatValidationErrors(errs))
	}
	opts.CellDeps = toMetadataCellDepGraph(cd)
	return nil
}

// attachPackageDeps populates opts.Packages when PackageDeps is set in the
// filter. Load failures are degraded gracefully: the export continues with
// a PackageDepsView{Error: "..."} so all other blocks remain intact.
func attachPackageDeps(opts *catalog.ExportOptions, filter catalog.Filter, rootDir string) error {
	if !filter.Include.PackageDeps {
		return nil
	}
	g, err := depgraph.Load(depgraph.LoadOptions{Dir: rootDir}, "./...")
	if err != nil {
		slog.Error("export: package dep graph load failed",
			slog.String("root", rootDir),
			slog.Any("error", err),
		)
		opts.Packages = &catalog.PackageDepsView{
			Error: "package dep load failed",
		}
		return nil
	}
	opts.Packages = &catalog.PackageDepsView{Graph: g}
	return nil
}

// resolveRoot returns the absolute project root directory. If arg is non-empty
// it is resolved to an absolute path; otherwise findRoot() walks up from cwd.
func resolveRoot(arg string) (string, error) {
	if arg != "" {
		abs, err := filepath.Abs(arg)
		if err != nil {
			return "", fmt.Errorf("resolve root: %w", err)
		}
		if _, err := os.Stat(abs); err != nil {
			return "", fmt.Errorf("resolve root: %w", err)
		}
		return abs, nil
	}
	r, err := findRoot()
	if err != nil {
		return "", fmt.Errorf("cannot find project root: %w", err)
	}
	return r, nil
}

// loadProjectMeta parses the GoCell metadata under root.
func loadProjectMeta(root string) (*metadata.ProjectMeta, error) {
	pm, err := metadata.NewParser(root).Parse()
	if err != nil {
		return nil, fmt.Errorf("metadata parse: %w", err)
	}
	return pm, nil
}

// buildFilter constructs a catalog.Filter from comma-separated CLI arguments.
// Returns an error if any token is unrecognized.
func buildFilter(kinds, layers, cells, include string) (catalog.Filter, error) {
	parsedKinds, err := csvparam.ParseAllowed(kinds, validKinds, "kinds")
	if err != nil {
		return catalog.Filter{}, fmt.Errorf("export: %w", err)
	}

	parsedLayers, err := csvparam.ParseAllowed(layers, validLayersList, "layers")
	if err != nil {
		return catalog.Filter{}, fmt.Errorf("export: %w", err)
	}
	parsedCells := csvparam.Parse(cells)

	inc, err := parseInclude(include)
	if err != nil {
		return catalog.Filter{}, err
	}

	return catalog.Filter{
		Kinds:   parsedKinds,
		Layers:  parsedLayers,
		Cells:   parsedCells,
		Include: inc,
	}, nil
}

// parseInclude converts a comma-separated include token string to an IncludeOptions.
// An empty string returns zero (nothing included). Unknown tokens return an error.
func parseInclude(s string) (catalog.IncludeOptions, error) {
	tokens, err := csvparam.ParseAllowed(s, validIncludeTokens, "include")
	if err != nil {
		return catalog.IncludeOptions{}, fmt.Errorf("export: %w", err)
	}
	var inc catalog.IncludeOptions
	for _, tok := range tokens {
		switch tok {
		case "cellDeps":
			inc.CellDeps = true
		case "packageDeps":
			inc.PackageDeps = true
		case "relations":
			inc.Relations = true
		case "statusBoard":
			inc.StatusBoard = true
		default:
			return catalog.IncludeOptions{}, fmt.Errorf("export: %w", csvparam.UnknownTokenError{
				Param:   "include",
				Allowed: validIncludeTokens,
			})
		}
	}
	return inc, nil
}

// toMetadataCellDepGraph converts a governance.Graph to *catalog.CellDepGraph.
func toMetadataCellDepGraph(g governance.Graph) *catalog.CellDepGraph {
	edges := make([]catalog.CellEdge, 0, len(g.Edges))
	for _, e := range g.Edges {
		edges = append(edges, catalog.CellEdge{From: e.From, To: e.To})
	}
	return &catalog.CellDepGraph{
		Nodes: g.Nodes,
		Edges: edges,
	}
}

// formatValidationErrors formats a slice of governance.ValidationResult as a
// multi-line string suitable for error messages.
func formatValidationErrors(errs []governance.ValidationResult) string {
	var sb strings.Builder
	for _, e := range errs {
		fmt.Fprintf(&sb, "  [%s] %s: %s\n", e.Code, e.Field, e.Message)
	}
	return sb.String()
}

// writeOut writes body to path (creating the file if needed), or to stdout if
// path is empty.
func writeOut(path string, body []byte) error {
	if path == "" {
		_, err := os.Stdout.Write(body)
		return err
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("export: write output: %w", err)
	}
	return nil
}
