package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ghbvf/gocell/tools/depgraph"
)

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
	fs.SetOutput(io.Discard)
	format := fs.String("format", "json", "output format: json|dot")
	pattern := fs.String("pattern", "./...", "package pattern passed to packages.Load")
	includeTests := fs.Bool("include-tests", false, "load test variants (mark TestOnly nodes)")
	if err := fs.Parse(args); err != nil {
		return graphOptions{}, fmt.Errorf("graph: %w", err)
	}
	f := graphFormat(strings.ToLower(*format))
	if f != graphFormatJSON && f != graphFormatDOT {
		return graphOptions{}, fmt.Errorf("graph: unknown format %q (want json|dot)", *format)
	}
	return graphOptions{
		Format:       f,
		Pattern:      *pattern,
		IncludeTests: *includeTests,
	}, nil
}

func executeGraph(opts graphOptions) error {
	g, err := depgraph.Load(depgraph.LoadOptions{
		IncludeTests: opts.IncludeTests,
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
		if err := g.WriteDOT(w); err != nil {
			return fmt.Errorf("graph: write dot: %w", err)
		}
	}
	return nil
}
