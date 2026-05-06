package catalog_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/runtime/devtools/catalog"
)

// TestAssemblySpec_OwnerAndMaxConsistencyLevelRoundTrip exercises the
// metadata→AssemblySpec mapping for the K#10 Owner and MaxConsistencyLevel
// fields. The test guards against silent regressions where the metadata
// fields are populated but the wire DTO still emits zero values.
func TestAssemblySpec_OwnerAndMaxConsistencyLevelRoundTrip(t *testing.T) {
	t.Parallel()
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"alpha": {ID: "alpha", Type: "core", ConsistencyLevel: "L2"},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"mainbundle": {
				ID:                  "mainbundle",
				Cells:               []string{"alpha"},
				Owner:               metadata.OwnerMeta{Team: "platform", Role: "bundle-owner"},
				MaxConsistencyLevel: "L2",
				Build: metadata.BuildMeta{
					Entrypoint:     "cmd/mainbundle/main.go",
					Binary:         "mainbundle",
					DeployTemplate: "k8s",
				},
			},
		},
	}
	doc, err := catalog.BuildDocument(pm, catalog.ExportOptions{
		Clock: clockmock.New(time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)),
	})
	require.NoError(t, err)

	var asmSpec catalog.AssemblySpec
	for _, e := range doc.Entities {
		if e.Kind == "Assembly" && e.Metadata.Name == "mainbundle" {
			s, ok := e.Spec.(catalog.AssemblySpec)
			require.True(t, ok)
			asmSpec = s
			break
		}
	}
	assert.Equal(t, "platform", asmSpec.Owner.Team)
	assert.Equal(t, "bundle-owner", asmSpec.Owner.Role)
	assert.Equal(t, "L2", asmSpec.MaxConsistencyLevel)
	assert.Equal(t, []string{"alpha"}, asmSpec.Cells)
	assert.Equal(t, "k8s", asmSpec.Build.DeployTemplate)
}

// TestAssemblySpecCoversAssemblyMeta enforces that every yaml-bearing
// exported field on metadata.AssemblyMeta is either:
//
//   - mapped onto runtime/devtools/catalog.AssemblySpec, or
//   - listed in catalogExcludedAssemblyFields with a documented reason.
//
// Adding a new metadata.AssemblyMeta field without wire DTO sync triggers
// this test, preventing the "metadata extends but catalog stays stale"
// drift class identified in PR #404 review §F4. The check uses reflection
// rather than AST scanning to keep the guard a few lines.
func TestAssemblySpecCoversAssemblyMeta(t *testing.T) {
	t.Parallel()
	metaFields := exportedYAMLNames(reflect.TypeOf(metadata.AssemblyMeta{}))
	specFields := exportedJSONNames(reflect.TypeOf(catalog.AssemblySpec{}))

	for fname := range metaFields {
		if catalogExcludedAssemblyFields[fname] != "" {
			continue
		}
		// Match on lowercase field name; AssemblySpec uses camelCase JSON tags
		// while AssemblyMeta uses camelCase YAML tags — same wire-name.
		if _, ok := specFields[fname]; !ok {
			t.Errorf("metadata.AssemblyMeta field %q has no AssemblySpec mapping; "+
				"add it to AssemblySpec, update buildAssemblyEntity, or list it in "+
				"catalogExcludedAssemblyFields with a reason", fname)
		}
	}
}

// catalogExcludedAssemblyFields lists AssemblyMeta exported fields that are
// intentionally not surfaced via AssemblySpec. Each entry maps the yaml
// field name to a reason string.
var catalogExcludedAssemblyFields = map[string]string{
	// id is the resource identifier; it is surfaced via Entity.Metadata.Name
	// + UID at the catalog envelope layer (build.go buildAssemblyEntity sets
	// Name: a.ID), not inside Spec which is reserved for resource shape.
	"id": "surfaced via EntityMetadata.Name, not Spec",
}

// exportedYAMLNames returns the lowercased yaml field names for the
// exported, yaml-tag-bearing fields of t. Fields with yaml:"-" or no
// yaml tag are skipped (parser-internal or non-serialized).
func exportedYAMLNames(t reflect.Type) map[string]struct{} {
	out := map[string]struct{}{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("yaml")
		if tag == "" || tag == "-" || strings.HasPrefix(tag, "-,") {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		out[name] = struct{}{}
	}
	return out
}

// exportedJSONNames returns the json field names for the exported, json-tag-
// bearing fields of t.
func exportedJSONNames(t reflect.Type) map[string]struct{} {
	out := map[string]struct{}{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" || strings.HasPrefix(tag, "-,") {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		out[name] = struct{}{}
	}
	return out
}
