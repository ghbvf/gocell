package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
	"github.com/ghbvf/gocell/tools/depgraph"
)

// defaultGraphPattern is the --pattern default. When unchanged AND --root is a
// workspace (has go.work), the graph spans every workspace member (one
// "<importPath>/..." pattern per module) rather than only the root module's
// "./..." — so a nested module is graphed, not silently dropped.
const defaultGraphPattern = "./..."

// defaultRootDir returns the current working directory, used as the default
// value for the --root flag when the caller does not supply one.
func defaultRootDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	return dir
}

// graphFormat is the rendered representation of a depgraph.Graph.
type graphFormat string

const (
	graphFormatJSON graphFormat = "json"
	graphFormatDOT  graphFormat = "dot"
)

// graphOptions configures runGraph. Held as a struct so graph_test.go
// can drive the same code path that main.go does.
type graphOptions struct {
	Format       graphFormat
	Pattern      string
	Root         string
	IncludeTests bool
	Out          io.Writer
}

func (o graphOptions) writer() io.Writer {
	if o.Out == nil {
		return os.Stdout
	}
	return o.Out
}

// runGraph implements `gocell graph [--format=json|dot] [--pattern=...]`.
// It loads the module's package graph via depgraph.Load and emits either
// JSON (default) or Graphviz DOT to stdout.
//
// ctx is part of the uniform commands-map signature; depgraph.Load wraps
// golang.org/x/tools/go/packages, which is not ctx-native, so there is no
// cancelable downstream to thread it into.
func runGraph(_ context.Context, args []string) error {
	opts, err := parseGraphArgs(args)
	if err != nil {
		return err
	}
	return executeGraph(opts)
}

func parseGraphArgs(args []string) (graphOptions, error) {
	fs := flag.NewFlagSet("graph", flag.ContinueOnError)
	// Default output (os.Stderr) is preserved so `-h` prints usage; do not
	// silence with io.Discard. Dispatch maps flag.ErrHelp → exit code 0.
	format := fs.String("format", "json", "output format: json|dot")
	pattern := fs.String("pattern", defaultGraphPattern,
		"package pattern passed to packages.Load; in a workspace (--root has go.work), "+
			"the default spans every member module — override to scope to one pattern")
	root := fs.String("root", defaultRootDir(), "project root directory passed as Dir to packages.Load")
	includeTests := fs.Bool("include-tests", false,
		"load test-variant packages so TestOnly markers are populated; "+
			"does NOT add or remove packages from the graph (test helper "+
			"packages always appear regardless of this flag)")
	if err := fs.Parse(args); err != nil {
		// flag.ErrHelp is the user asking for `-h`; let Dispatch see it raw
		// and translate into ExitOK. Other parse errors get the usual prefix.
		if errors.Is(err, flag.ErrHelp) {
			return graphOptions{}, err
		}
		return graphOptions{}, fmt.Errorf("graph: %w", err)
	}
	f := graphFormat(strings.ToLower(*format))
	if f != graphFormatJSON && f != graphFormatDOT {
		return graphOptions{}, fmt.Errorf("graph: unknown format %q (want json|dot)", *format)
	}
	return graphOptions{
		Format:       f,
		Pattern:      *pattern,
		Root:         *root,
		IncludeTests: *includeTests,
	}, nil
}

// loadGraph builds the dependency graph for opts.Root. The default --pattern
// delegates to loadPackageGraph (the shared workspace-aware loader: spans every
// go.work member via relative-dir "./<dir>/..." patterns, or a single standalone
// module when no go.work). A non-default --pattern is an explicit scope override
// (escape hatch) that is passed through verbatim — still go.work-aware (it loads
// in ModeWorkspace when a go.work is present, ModeModule otherwise).
func loadGraph(opts graphOptions) (*kerneldepgraph.Graph, error) {
	if opts.Pattern == defaultGraphPattern {
		return loadPackageGraph(opts.Root, opts.IncludeTests)
	}
	lo := depgraph.LoadOptions{IncludeTests: opts.IncludeTests, Dir: opts.Root}
	if _, err := os.Stat(filepath.Join(opts.Root, "go.work")); err != nil {
		// No go.work above the root → single standalone module.
		return depgraph.Load(lo, opts.Pattern)
	}
	return depgraph.LoadWorkspace(lo, opts.Pattern)
}

func executeGraph(opts graphOptions) error {
	g, err := loadGraph(opts)
	if err != nil {
		return fmt.Errorf("graph: load: %w", err)
	}
	w := opts.writer()
	switch opts.Format {
	case graphFormatJSON:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(g); err != nil {
			return fmt.Errorf("graph: encode json: %w", err)
		}
	case graphFormatDOT:
		if err := depgraph.WriteDOT(g, w); err != nil {
			return fmt.Errorf("graph: write dot: %w", err)
		}
	}
	return nil
}
