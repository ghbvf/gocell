package cellgen

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/codegen"
)

// TestRenderCell_HTTPResolverPresent verifies that cell.tmpl with non-empty
// HTTPMethodPermissions emits the runtime/auth import + the package-level
// cellHTTPResolver var built via auth.NewStaticMethodPolicyResolver, with each
// contractID→action entry rendered (#2205). This is the cell-level half of the
// contract-derived HTTP authz funnel — the sibling of the gRPC MethodPermissions map.
func TestRenderCell_HTTPResolverPresent(t *testing.T) {
	t.Parallel()
	spec := &CellGenSpec{
		Package:             "demo",
		StructName:          "Demo",
		CellID:              "demo",
		RenderedMetaLiteral: "&metadata.CellMeta{}",
		HTTPMethodPermissions: []MethodPermission{
			{FullMethod: "http.config.get.v1", Permission: "config:read"},
			{FullMethod: "http.config.write.v1", Permission: "config:write"},
		},
	}

	out, err := codegen.Render("github.com/ghbvf/gocell", codegen.RenderOptions{
		TemplateName: "cell.tmpl",
		Templates:    templates,
		Data:         spec,
		Filename:     "demo/cell_gen.go",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	got := string(out)

	mustContain(t, got, `"github.com/ghbvf/gocell/framework/runtime/auth"`)
	mustContain(t, got, `var cellHTTPResolver = auth.NewStaticMethodPolicyResolver(map[string]string{`)
	// Keys and values asserted separately so gofumpt colon-alignment whitespace
	// between them does not break the exact-substring match.
	mustContain(t, got, `"http.config.get.v1":`)
	mustContain(t, got, `"config:read"`)
	mustContain(t, got, `"http.config.write.v1":`)
	mustContain(t, got, `"config:write"`)
}

// TestRenderCell_HTTPResolverAbsent is the anti-vacuity / sparse-omit guard: a cell
// with no permission-bearing HTTP contracts renders NEITHER the resolver var NOR the
// runtime/auth import (the overlay is sparse — legacy hand-wired routes stay untouched).
func TestRenderCell_HTTPResolverAbsent(t *testing.T) {
	t.Parallel()
	spec := &CellGenSpec{
		Package:             "demo",
		StructName:          "Demo",
		CellID:              "demo",
		RenderedMetaLiteral: "&metadata.CellMeta{}",
		// HTTPMethodPermissions intentionally empty.
	}

	out, err := codegen.Render("github.com/ghbvf/gocell", codegen.RenderOptions{
		TemplateName: "cell.tmpl",
		Templates:    templates,
		Data:         spec,
		Filename:     "demo/cell_gen.go",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	got := string(out)

	if strings.Contains(got, "cellHTTPResolver") {
		t.Errorf("cell with no HTTP permissions must NOT render cellHTTPResolver, got:\n%s", got)
	}
	if strings.Contains(got, `"github.com/ghbvf/gocell/framework/runtime/auth"`) {
		t.Errorf("cell with no HTTP permissions must NOT import runtime/auth, got:\n%s", got)
	}
}
