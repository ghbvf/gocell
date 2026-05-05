package governance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

func TestValidateDOCNAME01_StrictScansActiveDocs(t *testing.T) {
	root := t.TempDir()
	writeDocNamingGuard(t, root)
	writeFile(t, root, "README.md", "# Demo\nUse sso-bff here.\n")
	writeFile(t, root, "docs/reviews/old.md", "Historical sso-bff is allowed here.\n")
	writeFile(t, root, "templates/adr.md", "Use ssobff here.\n")

	v := NewValidator(validProject(), root, clock.Real())
	results := v.validateDOCNAME01(true)

	require.Len(t, results, 1)
	got := results[0]
	assert.Equal(t, "DOC-NAME-01", got.Code)
	assert.Equal(t, SeverityError, got.Severity)
	assert.Equal(t, IssueForbidden, got.IssueType)
	assert.Equal(t, "README.md", got.File)
	assert.Equal(t, "content", got.Field)
	assert.Equal(t, 2, got.Line)
	assert.Positive(t, got.Column)
	assert.Contains(t, got.Message, `"sso-bff"`)
	assert.Contains(t, got.Message, `"ssobff"`)
}

func TestValidateDOCNAME01_NonStrictSilent(t *testing.T) {
	root := t.TempDir()
	writeDocNamingGuard(t, root)
	writeFile(t, root, "README.md", "Use sso-bff here.\n")

	v := NewValidator(validProject(), root, clock.Real())
	assert.Empty(t, v.validateDOCNAME01(false))
}

func TestValidateDOCNAME01_MissingGuardIsStrictError(t *testing.T) {
	root := t.TempDir()

	v := NewValidator(validProject(), root, clock.Real())
	results := v.validateDOCNAME01(true)

	require.Len(t, results, 1)
	assert.Equal(t, "DOC-NAME-01", results[0].Code)
	assert.Equal(t, SeverityError, results[0].Severity)
	assert.Equal(t, IssueRequired, results[0].IssueType)
	assert.Equal(t, docNamingGuardRelPath, results[0].File)
}

func TestValidateDOCNAME01_GlobIncludeAndWordBoundary(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "docs/architecture/naming-guard.yaml", `
include:
  - examples/*/README.md
exclude: []
replacements:
  - literal: todo-order
    replacement: todoorder
`)
	writeFile(t, root, "examples/todoorder/README.md", "todo-order should fail; todo-orderly should not.\n")
	writeFile(t, root, "examples/ssobff/README.md", "todoorder is already clean.\n")

	v := NewValidator(validProject(), root, clock.Real())
	results := v.validateDOCNAME01(true)

	require.Len(t, results, 1)
	assert.Equal(t, "examples/todoorder/README.md", results[0].File)
	assert.Equal(t, 1, results[0].Line)
	assert.Equal(t, 1, results[0].Column)
}

func TestValidateDOCNAME01_InvalidGuardConfig(t *testing.T) {
	tests := []struct {
		name        string
		config      string
		wantIssue   IssueType
		wantField   string
		wantMessage string
	}{
		{
			name:        "bad YAML",
			config:      "include: [\n",
			wantIssue:   IssueInvalid,
			wantField:   "",
			wantMessage: "cannot parse",
		},
		{
			name: "missing include",
			config: `
replacements:
  - literal: sso-bff
    replacement: ssobff
`,
			wantIssue:   IssueRequired,
			wantField:   "include",
			wantMessage: "include pattern",
		},
		{
			name: "missing replacements",
			config: `
include:
  - README.md
`,
			wantIssue:   IssueRequired,
			wantField:   "replacements",
			wantMessage: "replacement",
		},
		{
			name: "empty literal",
			config: `
include:
  - README.md
replacements:
  - literal: ""
    replacement: ssobff
`,
			wantIssue:   IssueRequired,
			wantField:   "replacements[0]",
			wantMessage: "literal and replacement",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "docs/architecture/naming-guard.yaml", tt.config)

			v := NewValidator(validProject(), root, clock.Real())
			results := v.validateDOCNAME01(true)

			require.NotEmpty(t, results)
			assert.Equal(t, "DOC-NAME-01", results[0].Code)
			assert.Equal(t, tt.wantIssue, results[0].IssueType)
			assert.Equal(t, tt.wantField, results[0].Field)
			assert.Contains(t, results[0].Message, tt.wantMessage)
		})
	}
}

func TestValidateDOCNAME01_InvalidIncludeAndReadError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "docs/architecture/naming-guard.yaml", `
include:
  - "["
  - README.md
replacements:
  - literal: sso-bff
    replacement: ssobff
`)
	writeFile(t, root, "README.md", "Use sso-bff here.\n")
	v := NewValidator(validProject(), root, clock.Real())

	results := v.validateDOCNAME01(true)
	require.Len(t, results, 1)
	assert.Equal(t, IssueInvalid, results[0].IssueType)
	assert.Contains(t, results[0].Message, "invalid document naming include pattern")

	writeFile(t, root, "docs/architecture/naming-guard.yaml", `
include:
  - README.md
replacements:
  - literal: sso-bff
    replacement: ssobff
`)
	v = NewValidator(validProject(), root, clock.Real())
	v.readFile = func(path string) ([]byte, error) {
		if strings.HasSuffix(filepath.ToSlash(path), "/README.md") {
			return nil, errors.New("permission denied")
		}
		return os.ReadFile(filepath.Clean(path))
	}

	results = v.validateDOCNAME01(true)
	require.Len(t, results, 1)
	assert.Equal(t, "README.md", results[0].File)
	assert.Equal(t, IssueInvalid, results[0].IssueType)
	assert.Contains(t, results[0].Message, "cannot read active document")
}

func TestValidateDOCNAME01_EmptyRootSilent(t *testing.T) {
	v := NewValidator(validProject(), "", clock.Real())
	assert.Empty(t, v.validateDOCNAME01(true))
}

func TestDocNamingPatternMatch(t *testing.T) {
	assert.True(t, docNamingPatternMatch("docs/design/a.md", "docs/design/**"))
	assert.True(t, docNamingPatternMatch("examples/todoorder/README.md", "examples/*/README.md"))
	assert.True(t, docNamingPatternMatch("README.md", "README.md"))
	assert.False(t, docNamingPatternMatch("docs/plans/a.md", "docs/design/**"))
	assert.False(t, docNamingPatternMatch("docs/design/a.md", "["))
}

func TestValidateStrict_IncludesDOCNAME01(t *testing.T) {
	root := t.TempDir()
	writeDocNamingGuard(t, root)
	writeFile(t, root, "README.md", "Use sso-bff here.\n")

	v := NewValidator(emptyDocNamingProject(), root, clock.Real())
	results, err := v.ValidateStrict(t.Context(), true)
	require.NoError(t, err)
	assertDOCNAME01Present(t, results)
}

func TestValidateStrictFailFast_IncludesDOCNAME01(t *testing.T) {
	root := t.TempDir()
	writeDocNamingGuard(t, root)
	writeFile(t, root, "README.md", "Use sso-bff here.\n")

	v := NewValidator(emptyDocNamingProject(), root, clock.Real())
	results, err := v.ValidateStrictFailFast(t.Context())
	require.NoError(t, err)

	require.Len(t, results, 1)
	assertDOCNAME01Present(t, results)
}

func emptyDocNamingProject() *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

func assertDOCNAME01Present(t *testing.T, results []ValidationResult) {
	t.Helper()
	for _, r := range results {
		if r.Code == "DOC-NAME-01" {
			assert.Equal(t, SeverityError, r.Severity)
			return
		}
	}
	t.Fatalf("expected DOC-NAME-01 in %v", results)
}

func writeDocNamingGuard(t *testing.T, root string) {
	t.Helper()
	writeFile(t, root, "docs/architecture/naming-guard.yaml", `
include:
  - README.md
  - docs/reviews/**
  - templates/**
exclude:
  - docs/reviews/**
replacements:
  - literal: sso-bff
    replacement: ssobff
`)
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
