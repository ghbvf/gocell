package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

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

func TestParseGraphArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		wantErr bool
		check   func(t *testing.T, opts graphOptions)
	}{
		{
			name: "default_json",
			args: nil,
			check: func(t *testing.T, opts graphOptions) {
				if opts.Format != graphFormatJSON {
					t.Errorf("Format = %q, want json", opts.Format)
				}
				if opts.Pattern != "./..." {
					t.Errorf("Pattern = %q, want ./...", opts.Pattern)
				}
			},
		},
		{
			name: "explicit_dot_uppercase",
			args: []string{"--format=DOT"},
			check: func(t *testing.T, opts graphOptions) {
				if opts.Format != graphFormatDOT {
					t.Errorf("Format = %q, want dot", opts.Format)
				}
			},
		},
		{
			name: "custom_pattern_and_tests",
			args: []string{"--pattern=./tools/...", "--include-tests"},
			check: func(t *testing.T, opts graphOptions) {
				if opts.Pattern != "./tools/..." {
					t.Errorf("Pattern = %q, want ./tools/...", opts.Pattern)
				}
				if !opts.IncludeTests {
					t.Error("IncludeTests = false, want true")
				}
			},
		},
		{
			name:    "unknown_format",
			args:    []string{"--format=xml"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts, err := parseGraphArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil; opts=%+v", opts)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGraphArgs: %v", err)
			}
			if tt.check != nil {
				tt.check(t, opts)
			}
		})
	}
}
