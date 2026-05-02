package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// repoRoot returns the gocell repo root by walking up from the test's working
// directory until a go.mod is found. This makes the graph tests independent
// of the working directory that `go test` is invoked from.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := findRoot()
	if err != nil {
		t.Fatalf("repoRoot: %v", err)
	}
	return root
}

// TestRunGraphJSON exercises the same code path as `gocell graph
// --format=json` against the real gocell module (the package's own test
// runs in the worktree root). We assert on shape rather than counts to
// keep the test stable as new packages land.
func TestRunGraphJSON(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := executeGraph(graphOptions{
		Format:  graphFormatJSON,
		Pattern: "github.com/ghbvf/gocell/tools/depgraph/...",
		Root:    repoRoot(t),
		Out:     &buf,
	}); err != nil {
		t.Fatalf("executeGraph: %v", err)
	}

	var graph struct {
		Module   string `json:"module"`
		Packages []struct {
			ID    string `json:"id"`
			Layer string `json:"layer"`
		} `json:"packages"`
		Stats struct {
			Packages int `json:"packages"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(buf.Bytes(), &graph); err != nil {
		t.Fatalf("json.Unmarshal: %v\noutput=%s", err, buf.String())
	}
	if graph.Module != "github.com/ghbvf/gocell" {
		t.Errorf("Module = %q, want gocell", graph.Module)
	}
	if graph.Stats.Packages == 0 {
		t.Error("Stats.Packages = 0, want > 0")
	}
	foundDepgraph := false
	for _, p := range graph.Packages {
		if p.ID == "github.com/ghbvf/gocell/tools/depgraph" {
			foundDepgraph = true
			if p.Layer != "tools" {
				t.Errorf("depgraph.Layer = %q, want tools", p.Layer)
			}
		}
	}
	if !foundDepgraph {
		t.Errorf("depgraph package missing from output:\n%s", buf.String())
	}
}

func TestRunGraphDOT(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := executeGraph(graphOptions{
		Format:  graphFormatDOT,
		Pattern: "github.com/ghbvf/gocell/tools/depgraph/...",
		Root:    repoRoot(t),
		Out:     &buf,
	}); err != nil {
		t.Fatalf("executeGraph: %v", err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "digraph depgraph {") {
		t.Errorf("DOT output missing header: %s", out[:64])
	}
	if !strings.Contains(out, `label="tools";`) {
		t.Error("DOT missing tools layer cluster")
	}
}

// mustParseGraphArgs parses args and fails the test on error. Used by the
// TestParseGraphArgs_* family below to keep each scenario at cognitive
// complexity ≤ 4 — the previous table-driven form with per-row check
// closures landed at 26 (SonarCloud brain-overload).
func mustParseGraphArgs(t *testing.T, args []string) graphOptions {
	t.Helper()
	opts, err := parseGraphArgs(args)
	if err != nil {
		t.Fatalf("parseGraphArgs(%v): %v", args, err)
	}
	return opts
}

func TestParseGraphArgs_Defaults(t *testing.T) {
	t.Parallel()
	opts := mustParseGraphArgs(t, nil)
	if opts.Format != graphFormatJSON {
		t.Errorf("Format = %q, want json", opts.Format)
	}
	if opts.Pattern != "./..." {
		t.Errorf("Pattern = %q, want ./...", opts.Pattern)
	}
}

func TestParseGraphArgs_FormatIsCaseInsensitive(t *testing.T) {
	t.Parallel()
	opts := mustParseGraphArgs(t, []string{"--format=DOT"})
	if opts.Format != graphFormatDOT {
		t.Errorf("Format = %q, want dot", opts.Format)
	}
}

func TestParseGraphArgs_CustomPatternAndIncludeTests(t *testing.T) {
	t.Parallel()
	opts := mustParseGraphArgs(t, []string{"--pattern=./tools/...", "--include-tests"})
	if opts.Pattern != "./tools/..." {
		t.Errorf("Pattern = %q, want ./tools/...", opts.Pattern)
	}
	if !opts.IncludeTests {
		t.Error("IncludeTests = false, want true")
	}
}

func TestParseGraphArgs_RootFlag(t *testing.T) {
	t.Parallel()
	opts := mustParseGraphArgs(t, []string{"--root=/tmp/myproject"})
	if opts.Root != "/tmp/myproject" {
		t.Errorf("Root = %q, want /tmp/myproject", opts.Root)
	}
}

func TestParseGraphArgs_UnknownFormatRejects(t *testing.T) {
	t.Parallel()
	opts, err := parseGraphArgs([]string{"--format=xml"})
	if err == nil {
		t.Errorf("expected error for unknown format, got nil; opts=%+v", opts)
	}
}
