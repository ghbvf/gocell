package assembly

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/pkg/pathsafe"
)

// TestScaffoldAssembly_YAMLScalarInjection asserts that user-provided
// free-text fields (OwnerTeam, OwnerRole) routed through the assembly
// scaffold template cannot inject adjacent YAML keys or break scalar
// structure. The fix lives in buildScaffoldContext which routes every
// user-input field through pkg/yamlsafe.Quote — without that funnel, raw
// strings containing `:` / `{` / `#` / leading whitespace destroy the
// emitted YAML structure.
//
// RED: scaffoldAssemblyContext still uses raw `string` fields; rendered
// assembly.yaml fails to round-trip back to the same OwnerTeam value.
//
// ref: kubernetes-sigs/yaml + gopkg.in/yaml.v3 plain→single→double quote
// strategy; gocell yamlsafe.Quote mirrors that choice with a typed Scalar
// newtype that prevents bypass.
func TestScaffoldAssembly_YAMLScalarInjection(t *testing.T) {
	t.Parallel()

	// Each fixture exercises a different YAML metacharacter class that
	// must round-trip through scalar quoting without leaking adjacent keys.
	// Newline / CR / NUL are already rejected by IsValidMetadataText
	// upstream, so they're covered in TestScaffoldAssembly_YAMLScalarInjection_ControlChars.
	cases := []struct {
		name string
		team string
		role string
	}{
		{
			name: "team_with_colon_and_braces",
			team: `evil: nested: {extra: pwned}`,
			role: "maintainer",
		},
		{
			name: "role_with_comment_marker",
			team: "platform",
			role: `evil#commented`,
		},
		{
			name: "team_with_leading_space",
			team: `  spaced-team`,
			role: "maintainer",
		},
		{
			name: "team_with_quotes",
			team: `O"quote'mixed`,
			role: `it's-role`,
		},
		{
			name: "team_with_yaml_indicators",
			team: `& *anchor !tag |literal >folded`,
			role: `%directive @reserved`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root, pm := scaffoldTestProject(t)
			gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

			spec := AssemblyScaffoldSpec{
				ID:        "myassembly",
				Cells:     []string{"examplecell"},
				OwnerTeam: tc.team,
				OwnerRole: tc.role,
				Deploy:    "k8s",
			}
			plan, err := gen.PlanAssemblyScaffold(spec)
			if err != nil {
				t.Fatalf("PlanAssemblyScaffold: %v", err)
			}
			realRoot, _ := pathsafe.ResolveRoot(root)
			if err := pathsafe.WritePlannedFiles(realRoot, plan, false); err != nil {
				t.Fatalf("WritePlannedFiles: %v", err)
			}

			asmYAML, err := os.ReadFile(filepath.Join(root, "assemblies", "myassembly", scaffoldAsmYAML)) //nolint:gosec // tempdir test fixture
			if err != nil {
				t.Fatalf("read assembly.yaml: %v", err)
			}

			var parsed struct {
				ID    string   `yaml:"id"`
				Cells []string `yaml:"cells"`
				Owner struct {
					Team string `yaml:"team"`
					Role string `yaml:"role"`
				} `yaml:"owner"`
			}
			if err := yaml.Unmarshal(asmYAML, &parsed); err != nil {
				t.Fatalf("rendered YAML failed to round-trip: %v\nrendered:\n%s", err, asmYAML)
			}

			if parsed.Owner.Team != tc.team {
				t.Errorf("owner.team round-trip mismatch:\n  want: %q\n  got:  %q\nrendered:\n%s",
					tc.team, parsed.Owner.Team, asmYAML)
			}
			if parsed.Owner.Role != tc.role {
				t.Errorf("owner.role round-trip mismatch:\n  want: %q\n  got:  %q\nrendered:\n%s",
					tc.role, parsed.Owner.Role, asmYAML)
			}

			// Top-level keys must not gain new entries from injected payloads.
			var topLevel map[string]any
			if err := yaml.Unmarshal(asmYAML, &topLevel); err != nil {
				t.Fatalf("top-level unmarshal: %v", err)
			}
			allowedKeys := map[string]struct{}{
				"id": {}, "cells": {}, "owner": {}, "build": {},
			}
			for k := range topLevel {
				if _, ok := allowedKeys[k]; !ok {
					t.Errorf("YAML injection created adjacent key %q (allowed: %v)", k, keysOf(allowedKeys))
				}
			}
			// owner sub-keys must not gain new entries from `nested:` injection.
			if ownerMap, ok := topLevel["owner"].(map[string]any); ok {
				for k := range ownerMap {
					if k != "team" && k != "role" {
						t.Errorf("YAML injection created adjacent owner key %q (allowed: team/role)", k)
					}
				}
			}
			// build sub-keys must not gain new entries from injection.
			if buildMap, ok := topLevel["build"].(map[string]any); ok {
				for k := range buildMap {
					if k != "entrypoint" && k != "binary" && k != "deployTemplate" {
						t.Errorf("YAML injection created adjacent build key %q (allowed: entrypoint/binary/deployTemplate)", k)
					}
				}
			}
		})
	}
}

func keysOf(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestScaffoldAssembly_YAMLScalarFile_ColonInOwner is a focused regression
// test: even a single ":" in OwnerTeam must NOT break the emitted YAML's
// line structure. This is the minimal repro of the "free-text field injects
// adjacent YAML key" class of bug.
func TestScaffoldAssembly_YAMLScalarFile_ColonInOwner(t *testing.T) {
	t.Parallel()

	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)

	const injectedTeam = "ops:malicious-key: pwned"

	spec := AssemblyScaffoldSpec{
		ID:        "myassembly",
		Cells:     []string{"examplecell"},
		OwnerTeam: injectedTeam,
		OwnerRole: "maintainer",
	}
	plan, err := gen.PlanAssemblyScaffold(spec)
	if err != nil {
		t.Fatalf("PlanAssemblyScaffold: %v", err)
	}
	realRoot, _ := pathsafe.ResolveRoot(root)
	if err := pathsafe.WritePlannedFiles(realRoot, plan, false); err != nil {
		t.Fatalf("WritePlannedFiles: %v", err)
	}
	asmYAML, _ := os.ReadFile(filepath.Join(root, "assemblies", "myassembly", scaffoldAsmYAML)) //nolint:gosec // tempdir fixture

	// Smell test: the injected literal must NOT appear inline as a bare YAML
	// key/value pair on the line under owner.
	if strings.Contains(string(asmYAML), "\nmalicious-key:") {
		t.Errorf("YAML injection succeeded: malicious-key appeared as adjacent YAML key\nrendered:\n%s", asmYAML)
	}

	// Verify round-trip integrity.
	var parsed struct {
		Owner struct {
			Team string `yaml:"team"`
		} `yaml:"owner"`
	}
	if err := yaml.Unmarshal(asmYAML, &parsed); err != nil {
		t.Fatalf("yaml.Unmarshal: %v\nrendered:\n%s", err, asmYAML)
	}
	if parsed.Owner.Team != injectedTeam {
		t.Errorf("owner.team round-trip mismatch:\n  want: %q\n  got:  %q", injectedTeam, parsed.Owner.Team)
	}
}
