package cellgen

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen"
)

// fmtLiteral wraps a CellMeta literal string in a minimal Go file, runs
// goimports+gofumpt, and returns the normalized literal extracted from the
// output. This mirrors what the cellgen template pipeline does to the
// renderCellMetaLiteral output, ensuring test assertions compare post-format
// output against the existing cell_gen.go golden.
func fmtLiteral(t *testing.T, lit string) string {
	t.Helper()
	src := fmt.Sprintf(`package testpkg

import "github.com/ghbvf/gocell/kernel/metadata"

var _ = %s
`, lit)
	formatted, err := codegen.FormatGoSource("", []byte(src))
	if err != nil {
		t.Fatalf("fmtLiteral: FormatGoSource error: %v\nraw source:\n%s", err, src)
	}
	// Extract the literal from `var _ = <literal>` by finding the var statement.
	s := string(formatted)
	const marker = "var _ = "
	idx := strings.Index(s, marker)
	if idx < 0 {
		t.Fatalf("fmtLiteral: marker %q not found in formatted output:\n%s", marker, s)
	}
	// The literal goes from after the marker to the final "}".
	rest := strings.TrimRight(s[idx+len(marker):], "\n")
	// Remove trailing newline after the closing brace.
	return strings.TrimSuffix(rest, "\n")
}

// accesscoreGolden is the gofmt-normalized literal for the accesscore cell.
// It must match the `&metadata.CellMeta{...}` block in
// cells/accesscore/cell_gen.go lines 24-35 after normalization.
var accesscoreGolden = strings.TrimSpace(`&metadata.CellMeta{
	ID:               "accesscore",
	Type:             "core",
	ConsistencyLevel: "L2",
	DurabilityMode:   "durable",
	Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
	Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
	Verify: metadata.CellVerifyMeta{Smoke: []string{
		"smoke.accesscore.startup",
	}},
	GoStructName: metadata.MustNewGoIdentifier("AccessCore"),
}`)

func TestRenderCellMetaLiteral_AccesscoreGreenBaseline(t *testing.T) {
	cell := &metadata.CellMeta{
		ID:               "accesscore",
		Type:             "core",
		ConsistencyLevel: "L2",
		DurabilityMode:   "durable",
		Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
		Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
		Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.accesscore.startup"}},
		GoStructName:     metadata.MustNewGoIdentifier("AccessCore"),
		// L0Dependencies: nil/empty — must be omitted (zero-value skip)
		// Dir and File have yaml:"-" — must be omitted
	}

	raw := RenderCellMetaLiteral(cell)
	got := fmtLiteral(t, raw)
	if got != accesscoreGolden {
		t.Errorf("RenderCellMetaLiteral() GREEN baseline mismatch")
		gotLines := strings.Split(got, "\n")
		wantLines := strings.Split(accesscoreGolden, "\n")
		for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
			g, w := "", ""
			if i < len(gotLines) {
				g = gotLines[i]
			}
			if i < len(wantLines) {
				w = wantLines[i]
			}
			if g != w {
				t.Errorf("  line %d: got  %q", i+1, g)
				t.Errorf("  line %d: want %q", i+1, w)
			}
		}
	}
}

func TestRenderCellMetaLiteral_TableDriven(t *testing.T) {
	tests := []struct {
		name  string
		input *metadata.CellMeta
		// want is the gofmt-normalized expected output
		want string
	}{
		{
			name: "empty verify smoke slice omitted",
			input: &metadata.CellMeta{
				ID:               "testcell",
				Type:             "core",
				ConsistencyLevel: "L1",
				DurabilityMode:   "demo",
				Owner:            metadata.OwnerMeta{Team: "eng", Role: "owner"},
				Schema:           metadata.SchemaMeta{Primary: "test_schema"},
				// Verify.Smoke is nil — Verify struct is zero-value — must be omitted
				GoStructName: metadata.MustNewGoIdentifier("TestCell"),
			},
			want: strings.TrimSpace(`&metadata.CellMeta{
	ID:               "testcell",
	Type:             "core",
	ConsistencyLevel: "L1",
	DurabilityMode:   "demo",
	Owner:            metadata.OwnerMeta{Team: "eng", Role: "owner"},
	Schema:           metadata.SchemaMeta{Primary: "test_schema"},
	GoStructName: metadata.MustNewGoIdentifier("TestCell"),
}`),
		},
		{
			name: "multiple smoke entries",
			input: &metadata.CellMeta{
				ID:               "multicell",
				Type:             "edge",
				ConsistencyLevel: "L0",
				DurabilityMode:   "demo",
				Owner:            metadata.OwnerMeta{Team: "dev", Role: "viewer"},
				Schema:           metadata.SchemaMeta{Primary: "multi_schema"},
				Verify: metadata.CellVerifyMeta{Smoke: []string{
					"smoke.multicell.startup",
					"smoke.multicell.health",
				}},
				GoStructName: metadata.MustNewGoIdentifier("MultiCell"),
			},
			want: strings.TrimSpace(`&metadata.CellMeta{
	ID:               "multicell",
	Type:             "edge",
	ConsistencyLevel: "L0",
	DurabilityMode:   "demo",
	Owner:            metadata.OwnerMeta{Team: "dev", Role: "viewer"},
	Schema:           metadata.SchemaMeta{Primary: "multi_schema"},
	Verify: metadata.CellVerifyMeta{Smoke: []string{
		"smoke.multicell.startup",
		"smoke.multicell.health",
	}},
	GoStructName: metadata.MustNewGoIdentifier("MultiCell"),
}`),
		},
		{
			name: "L0Dependencies populated",
			input: &metadata.CellMeta{
				ID:               "l0cell",
				Type:             "support",
				ConsistencyLevel: "L0",
				DurabilityMode:   "demo",
				Owner:            metadata.OwnerMeta{Team: "infra", Role: "owner"},
				Schema:           metadata.SchemaMeta{Primary: "l0_schema"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.l0cell.startup"}},
				L0Dependencies: []metadata.L0DepMeta{
					{Cell: "cryptolib", Reason: "hashing"},
					{Cell: "validationlib", Reason: "input validation"},
				},
				GoStructName: metadata.MustNewGoIdentifier("L0Cell"),
			},
			want: strings.TrimSpace(`&metadata.CellMeta{
	ID:               "l0cell",
	Type:             "support",
	ConsistencyLevel: "L0",
	DurabilityMode:   "demo",
	Owner:            metadata.OwnerMeta{Team: "infra", Role: "owner"},
	Schema:           metadata.SchemaMeta{Primary: "l0_schema"},
	Verify: metadata.CellVerifyMeta{Smoke: []string{
		"smoke.l0cell.startup",
	}},
	L0Dependencies: []metadata.L0DepMeta{
		{Cell: "cryptolib", Reason: "hashing"},
		{Cell: "validationlib", Reason: "input validation"},
	},
	GoStructName: metadata.MustNewGoIdentifier("L0Cell"),
}`),
		},
		{
			name: "Dir and File yaml:- fields are skipped",
			input: &metadata.CellMeta{
				ID:               "dircell",
				Type:             "core",
				ConsistencyLevel: "L1",
				DurabilityMode:   "durable",
				Owner:            metadata.OwnerMeta{Team: "t", Role: "r"},
				Schema:           metadata.SchemaMeta{Primary: "s"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.dircell.startup"}},
				GoStructName:     metadata.MustNewGoIdentifier("DirCell"),
				Dir:              "cells/dircell",           // yaml:"-" — must be skipped
				File:             "cells/dircell/cell.yaml", // yaml:"-" — must be skipped
			},
			want: strings.TrimSpace(`&metadata.CellMeta{
	ID:               "dircell",
	Type:             "core",
	ConsistencyLevel: "L1",
	DurabilityMode:   "durable",
	Owner:            metadata.OwnerMeta{Team: "t", Role: "r"},
	Schema:           metadata.SchemaMeta{Primary: "s"},
	Verify: metadata.CellVerifyMeta{Smoke: []string{
		"smoke.dircell.startup",
	}},
	GoStructName: metadata.MustNewGoIdentifier("DirCell"),
}`),
		},
		{
			name: "zero GoStructName is skipped",
			input: &metadata.CellMeta{
				ID:               "nocell",
				Type:             "core",
				ConsistencyLevel: "L1",
				DurabilityMode:   "demo",
				Owner:            metadata.OwnerMeta{Team: "t", Role: "r"},
				Schema:           metadata.SchemaMeta{Primary: "s"},
				// GoStructName zero — should be omitted
			},
			want: strings.TrimSpace(`&metadata.CellMeta{
	ID:    "nocell",
	Type:  "core",
	ConsistencyLevel: "L1",
	DurabilityMode:   "demo",
	Owner:  metadata.OwnerMeta{Team: "t", Role: "r"},
	Schema: metadata.SchemaMeta{Primary: "s"},
}`),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := RenderCellMetaLiteral(tc.input)
			got := fmtLiteral(t, raw)
			// Normalize tc.want through fmtLiteral as well so test expectations
			// don't need hand-crafted alignment.
			want := fmtLiteral(t, tc.want)
			if got != want {
				t.Errorf("RenderCellMetaLiteral(%s) mismatch\ngot:\n%s\nwant:\n%s", tc.name, got, want)
				gotLines := strings.Split(got, "\n")
				wantLines := strings.Split(want, "\n")
				for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
					g, w := "", ""
					if i < len(gotLines) {
						g = gotLines[i]
					}
					if i < len(wantLines) {
						w = wantLines[i]
					}
					if g != w {
						t.Errorf("  line %d: got  %q", i+1, g)
						t.Errorf("  line %d: want %q", i+1, w)
					}
				}
			}
		})
	}
}
