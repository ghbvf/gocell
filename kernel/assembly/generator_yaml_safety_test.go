package assembly

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/kernel/scaffoldid"
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
		{
			name: "team_leading_dash_space",
			team: "- oncall",
			role: "maintainer",
		},
		{
			name: "role_trailing_space",
			team: "platform",
			role: "maintainer ",
		},
		{
			name: "team_doc_marker",
			team: "---",
			role: "maintainer",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			asmYAML := renderInjectionAssemblyYAML(t, tc.team, tc.role)
			assertOwnerRoundTrip(t, asmYAML, tc.team, tc.role)
			assertNoInjectedAdjacentKeys(t, asmYAML)
		})
	}
}

// renderInjectionAssemblyYAML drives a single scaffold-assembly invocation
// with the injection payload supplied via team / role and returns the
// rendered assembly.yaml bytes. The helper isolates fixture setup, plan
// generation, and disk read so the injection fixtures themselves stay
// declarative — keeps TestScaffoldAssembly_YAMLScalarInjection cognitive
// complexity inside the project budget.
func renderInjectionAssemblyYAML(t *testing.T, team, role string) []byte {
	t.Helper()
	root, pm := scaffoldTestProject(t)
	gen := NewGenerator(pm, "github.com/ghbvf/gocell", root)
	spec := AssemblyScaffoldSpec{
		ID:        mustID(t, "myassembly"),
		Cells:     []scaffoldid.ScaffoldID{"examplecell"},
		OwnerTeam: team,
		OwnerRole: role,
		Deploy:    "k8s",
	}
	plan, err := gen.PlanAssemblyScaffold(spec)
	if err != nil {
		t.Fatalf("PlanAssemblyScaffold: %v", err)
	}
	realRoot, _ := pathsafe.ResolveRoot(root)
	if err := pathsafe.WritePlannedFiles(realRoot, mustPlanSet(t, plan), false); err != nil {
		t.Fatalf("WritePlannedFiles: %v", err)
	}
	asmYAML, err := os.ReadFile(filepath.Join(root, "assemblies", "myassembly", scaffoldAsmYAML)) //nolint:gosec // tempdir test fixture
	if err != nil {
		t.Fatalf("read assembly.yaml: %v", err)
	}
	return asmYAML
}

// assertOwnerRoundTrip unmarshals asmYAML into a typed struct and asserts
// the round-tripped owner.team / owner.role byte-equal the originals.
// Failure here means yamlsafe.Quote dropped or corrupted the scalar.
func assertOwnerRoundTrip(t *testing.T, asmYAML []byte, wantTeam, wantRole string) {
	t.Helper()
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
	if parsed.Owner.Team != wantTeam {
		t.Errorf("owner.team round-trip mismatch:\n  want: %q\n  got:  %q\nrendered:\n%s",
			wantTeam, parsed.Owner.Team, asmYAML)
	}
	if parsed.Owner.Role != wantRole {
		t.Errorf("owner.role round-trip mismatch:\n  want: %q\n  got:  %q\nrendered:\n%s",
			wantRole, parsed.Owner.Role, asmYAML)
	}
}

// assertNoInjectedAdjacentKeys asserts that top-level / owner / build map
// key sets contain no entries beyond their declared shape. A YAML injection
// payload like `team: evil\nrootRole: pwned` would surface here as an
// unexpected top-level key or owner sub-key.
func assertNoInjectedAdjacentKeys(t *testing.T, asmYAML []byte) {
	t.Helper()
	var topLevel map[string]any
	if err := yaml.Unmarshal(asmYAML, &topLevel); err != nil {
		t.Fatalf("top-level unmarshal: %v", err)
	}
	assertKeysAllowed(t, "top-level", topLevel, "id", "cells", "owner", "build")
	if ownerMap, ok := topLevel["owner"].(map[string]any); ok {
		assertKeysAllowed(t, "owner", ownerMap, "team", "role")
	}
	if buildMap, ok := topLevel["build"].(map[string]any); ok {
		assertKeysAllowed(t, "build", buildMap, "entrypoint", "binary", "deployTemplate")
	}
}

// assertKeysAllowed reports any key in m that is not in the allowed list.
// The scope label distinguishes nested map levels in the error message.
func assertKeysAllowed(t *testing.T, scope string, m map[string]any, allowed ...string) {
	t.Helper()
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, k := range allowed {
		allowedSet[k] = struct{}{}
	}
	for k := range m {
		if _, ok := allowedSet[k]; !ok {
			t.Errorf("YAML injection created adjacent %s key %q (allowed: %v)", scope, k, allowed)
		}
	}
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
		ID:        mustID(t, "myassembly"),
		Cells:     []scaffoldid.ScaffoldID{"examplecell"},
		OwnerTeam: injectedTeam,
		OwnerRole: "maintainer",
	}
	plan, err := gen.PlanAssemblyScaffold(spec)
	if err != nil {
		t.Fatalf("PlanAssemblyScaffold: %v", err)
	}
	realRoot, _ := pathsafe.ResolveRoot(root)
	if err := pathsafe.WritePlannedFiles(realRoot, mustPlanSet(t, plan), false); err != nil {
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
