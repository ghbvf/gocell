package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot returns the gocell repo (workspace) root by walking up from the
// test's working directory until the directory holding go.work is found.
//
// cmd/gocell is its own go.work module (#1557), so the production findRoot()
// ("nearest go.mod wins") now stops at cmd/gocell/go.mod. These self-referential
// CLI tests scan the OUTER gocell repo (cells/, journeys/, assemblies/,
// tools/archtest/testdata, …), which lives at the workspace root — the only
// directory holding go.work (cmd/gocell/ holds go.mod but not go.work). Walking
// up to go.work therefore skips the cmd/gocell module boundary and resolves the
// real repo root, independent of the working directory `go test` runs from.
// Production findRoot() keeps its "nearest module" contract (correct for the CLI
// invoked from the repo root at runtime).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("repoRoot: getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.work")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repoRoot: no go.work found walking up from %s", dir)
		}
		dir = parent
	}
}

// TestRunGraphJSON exercises the same code path as `gocell graph
// --format=json` against the real gocell module (the package's own test
// runs in the worktree root). We assert on shape rather than counts to
// keep the test stable as new packages land.
func TestRunGraphJSON(t *testing.T) {
	// Not parallel (t.Setenv): `gocell graph` loads the real-repo package graph in
	// ModeWorkspace, which fail-closes under GOWORK=off. Post-#1557 cmd/gocell is a
	// go.work satellite whose tests run via hack/verify-workspace-test.sh under
	// GOWORK=off; point GOWORK at the repo's own go.work so the workspace load works
	// regardless of the ambient GOWORK (same pattern as useWorkspaceMultiModuleFixture).
	root := repoRoot(t)
	t.Setenv("GOWORK", filepath.Join(root, "go.work"))
	var buf bytes.Buffer
	if err := executeGraph(graphOptions{
		Format:  graphFormatJSON,
		Pattern: "github.com/ghbvf/gocell/tools/depgraph/...",
		Root:    root,
		Out:     &buf,
	}); err != nil {
		t.Fatalf("executeGraph: %v", err)
	}

	var graph struct {
		Modules  []string `json:"modules"`
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
	if len(graph.Modules) != 1 || graph.Modules[0] != "github.com/ghbvf/gocell/tools" {
		t.Errorf("Modules = %v, want [github.com/ghbvf/gocell/tools]", graph.Modules)
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
	// Not parallel (t.Setenv): see TestRunGraphJSON — graph uses ModeWorkspace which
	// fail-closes under hack/verify-workspace-test.sh's GOWORK=off (#1557 satellite).
	root := repoRoot(t)
	t.Setenv("GOWORK", filepath.Join(root, "go.work"))
	var buf bytes.Buffer
	if err := executeGraph(graphOptions{
		Format:  graphFormatDOT,
		Pattern: "github.com/ghbvf/gocell/tools/depgraph/...",
		Root:    root,
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
