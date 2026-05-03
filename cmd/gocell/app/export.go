package app

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/depgraph"
)

// validIncludeTokens is the set of accepted --include= tokens.
var validIncludeTokens = []string{"cellDeps", "packageDeps", "relations", "statusBoard"}

// validKinds is the set of accepted --kinds= tokens.
// References metadata.AllKinds — single source of truth.
var validKinds = metadata.AllKinds

// validLayers is the set of accepted --layers= tokens.
// References metadata.AllLayers — single source of truth.
var validLayersList = metadata.AllLayers

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

	opts := metadata.ExportOptions{
		Now:    clock.Real().Now().UTC(),
		Root:   rootDir,
		Filter: filter,
	}

	if err := attachCellDeps(&opts, filter, pm); err != nil {
		return err
	}

	if err := attachPackageDeps(&opts, filter, rootDir); err != nil {
		return err
	}

	doc, err := metadata.BuildDocument(pm, opts)
	if err != nil {
		return err
	}

	body, err := metadata.MarshalDocument(doc, *format)
	if err != nil {
		return err
	}

	return writeOut(*out, body)
}

// attachCellDeps populates opts.CellDeps when IncludeCellDeps is set in the filter.
func attachCellDeps(opts *metadata.ExportOptions, filter metadata.Filter, pm *metadata.ProjectMeta) error {
	if filter.Include&metadata.IncludeCellDeps == 0 {
		return nil
	}
	cd, errs := governance.NewDependencyChecker(pm).Graph()
	if len(errs) > 0 {
		return fmt.Errorf("cell dep graph build had validation errors:\n%s", formatValidationErrors(errs))
	}
	opts.CellDeps = toMetadataCellDepGraph(cd)
	return nil
}

// attachPackageDeps populates opts.Packages when IncludePackageDeps is set in
// the filter. Load failures are degraded gracefully: the export continues with
// a PackageDepsView{Status: "error"} so all other blocks remain intact.
func attachPackageDeps(opts *metadata.ExportOptions, filter metadata.Filter, rootDir string) error {
	if filter.Include&metadata.IncludePackageDeps == 0 {
		return nil
	}
	g, err := depgraph.Load(depgraph.LoadOptions{Dir: rootDir}, "./...")
	if err != nil {
		slog.Error("export: package dep graph load failed",
			slog.String("root", rootDir),
			slog.Any("error", err),
		)
		opts.Packages = &metadata.PackageDepsView{
			Status: "error",
			Error:  "package dep load failed",
		}
		return nil
	}
	opts.Packages = &metadata.PackageDepsView{Status: "ready", Graph: g}
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

// buildFilter constructs a metadata.Filter from comma-separated CLI arguments.
// Returns an error if any token is unrecognized.
func buildFilter(kinds, layers, cells, include string) (metadata.Filter, error) {
	parsedKinds, err := parseTokens(kinds, validKinds, "kinds")
	if err != nil {
		return metadata.Filter{}, err
	}

	parsedLayers, err := parseTokens(layers, validLayersList, "layers")
	if err != nil {
		return metadata.Filter{}, err
	}
	parsedCells := parseTokensOpen(cells)

	mask, err := parseInclude(include)
	if err != nil {
		return metadata.Filter{}, err
	}

	return metadata.Filter{
		Kinds:   parsedKinds,
		Layers:  parsedLayers,
		Cells:   parsedCells,
		Include: mask,
	}, nil
}

// parseTokens splits a comma-separated string, trims whitespace, deduplicates,
// and validates each token against the allowed set. Returns nil (not empty slice)
// when s is empty (means "all"). Returns a sorted slice.
func parseTokens(s string, allowed []string, flagName string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, v := range allowed {
		allowedSet[v] = true
	}
	seen := make(map[string]bool)
	var result []string
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if !allowedSet[tok] {
			return nil, fmt.Errorf("export: unknown %s %q (valid: %s)", flagName, tok, strings.Join(allowed, ", "))
		}
		if !seen[tok] {
			seen[tok] = true
			result = append(result, tok)
		}
	}
	sort.Strings(result)
	return result, nil
}

// parseTokensOpen splits a comma-separated string without whitelist validation.
// Used for --layers and --cells which are open-ended sets.
func parseTokensOpen(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	seen := make(map[string]bool)
	var result []string
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if !seen[tok] {
			seen[tok] = true
			result = append(result, tok)
		}
	}
	sort.Strings(result)
	return result
}

// includeTokenToMask maps the wire token name to its IncludeMask bit.
var includeTokenToMask = map[string]metadata.IncludeMask{
	"cellDeps":    metadata.IncludeCellDeps,
	"packageDeps": metadata.IncludePackageDeps,
	"relations":   metadata.IncludeRelations,
	"statusBoard": metadata.IncludeStatusBoard,
}

// parseInclude converts a comma-separated include token string to an IncludeMask.
// An empty string returns zero (nothing included). Unknown tokens return an error.
func parseInclude(s string) (metadata.IncludeMask, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	var mask metadata.IncludeMask
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		bit, ok := includeTokenToMask[tok]
		if !ok {
			return 0, fmt.Errorf("export: unknown include token %q (valid: %s)",
				tok, strings.Join(validIncludeTokens, ", "))
		}
		mask |= bit
	}
	return mask, nil
}

// toMetadataCellDepGraph converts a governance.Graph to *metadata.CellDepGraph.
func toMetadataCellDepGraph(g governance.Graph) *metadata.CellDepGraph {
	edges := make([]metadata.CellEdge, 0, len(g.Edges))
	for _, e := range g.Edges {
		edges = append(edges, metadata.CellEdge{From: e.From, To: e.To})
	}
	return &metadata.CellDepGraph{
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
