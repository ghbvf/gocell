package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ghbvf/gocell/tools/depgraph"
)

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
func runGraph(args []string) error {
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
	pattern := fs.String("pattern", "./...", "package pattern passed to packages.Load")
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

func executeGraph(opts graphOptions) error {
	g, err := depgraph.Load(depgraph.LoadOptions{
		IncludeTests: opts.IncludeTests,
		Dir:          opts.Root,
	}, opts.Pattern)
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
