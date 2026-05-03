package devtools_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/devtools/catalog"
	"github.com/ghbvf/gocell/runtime/http/devtools"
)

// minimalPkgGraph returns a small *kerneldepgraph.Graph for use in tests.
func minimalPkgGraph() *kerneldepgraph.Graph {
	return kerneldepgraph.FromNodes("github.com/ghbvf/gocell", []*kerneldepgraph.Node{
		{ID: "github.com/ghbvf/gocell/kernel/cell", Layer: "kernel", Imports: []string{}},
	})
}

// buildTestHandler constructs a Handler with synthetic ProjectMeta data and a
// non-nil pkgGraph (build-time path, always ready).
func buildTestHandler(t *testing.T) *devtools.Handler {
	t.Helper()
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "owner"},
				Schema:           metadata.SchemaMeta{Primary: "public.sessions"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.accesscore.health"}},
			},
			"auditcore": {
				ID:               "auditcore",
				Type:             "support",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "owner"},
				Schema:           metadata.SchemaMeta{Primary: "public.audit"},
			},
		},
		Slices:      map[string]*metadata.SliceMeta{},
		Contracts:   map[string]*metadata.ContractMeta{},
		Journeys:    map[string]*metadata.JourneyMeta{},
		Assemblies:  map[string]*metadata.AssemblyMeta{},
		StatusBoard: []metadata.StatusBoardEntry{},
		Actors:      []metadata.ActorMeta{},
	}
	cellGraph := &catalog.CellDepGraph{
		Nodes: []string{"accesscore", "auditcore"},
		Edges: []catalog.CellEdge{},
	}
	return devtools.NewHandler(project, cellGraph, minimalPkgGraph(), "/test-root", clock.Real())
}

// doAdminRequest fires a GET request with an admin auth context.
func doAdminRequest(t *testing.T, h *devtools.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// doUserRequest fires a GET request with a non-admin user context.
func doUserRequest(t *testing.T, h *devtools.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(auth.TestContext("regular-user", []string{"user"}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// doAnonRequest fires a GET request with no auth context.
func doAnonRequest(t *testing.T, h *devtools.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestCatalog_Default_AdminGetsFullSnapshot(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/devtools/catalog")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var doc catalog.Document
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if doc.SchemaVersion != catalog.SchemaVersionV1 {
		t.Errorf("schemaVersion = %q, want %q", doc.SchemaVersion, catalog.SchemaVersionV1)
	}
	if len(doc.Entities) == 0 {
		t.Error("expected non-empty Entities")
	}
}

// TestCatalog_Policy_EnforcedByRouter documents that the admin-only Policy
// (auth.AnyRole("admin")) is declared on auth.Route and enforced by the
// router middleware — not by the handler itself. When the handler is invoked
// directly via httptest (bypassing the router), non-admin and unauthenticated
// requests are not rejected at the handler level.
func TestCatalog_Policy_EnforcedByRouter_NotByHandler(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)

	// Non-admin user — handler does not enforce Policy directly; router does.
	rrNonAdmin := doUserRequest(t, h, "/devtools/catalog")
	if rrNonAdmin.Code != http.StatusOK {
		t.Errorf("expected handler to pass through non-admin (policy enforced by router), got %d: %s",
			rrNonAdmin.Code, rrNonAdmin.Body.String())
	}

	// No subject at all — same; handler passes through without enforcing Policy.
	rrAnon := doAnonRequest(t, h, "/devtools/catalog")
	if rrAnon.Code != http.StatusOK {
		t.Errorf("expected handler to pass through unauthenticated (policy enforced by router), got %d: %s",
			rrAnon.Code, rrAnon.Body.String())
	}
}

func TestCatalog_FilterKinds(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/devtools/catalog?kinds=Cell,Contract")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var doc catalog.Document
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, e := range doc.Entities {
		if e.Kind != "Cell" && e.Kind != "Contract" {
			t.Errorf("entity kind %q not in filter {Cell,Contract}", e.Kind)
		}
	}
}

func TestCatalog_FilterLayers(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/devtools/catalog?layers=cells")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCatalog_CellsFocus(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/devtools/catalog?cells=accesscore")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var doc catalog.Document
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var names []string
	for _, e := range doc.Entities {
		names = append(names, e.Kind+"/"+e.Metadata.Name)
	}
	if !containsString(names, "Cell/accesscore") {
		t.Fatalf("focused response missing accesscore cell: %v", names)
	}
	if containsString(names, "Cell/auditcore") {
		t.Fatalf("focused response leaked unrelated auditcore cell: %v", names)
	}
	if doc.Dependencies == nil || doc.Dependencies.Cells == nil {
		t.Fatalf("focused response missing cellDeps block")
	}
	if got := doc.Dependencies.Cells.Nodes; len(got) != 1 || got[0] != "accesscore" {
		t.Fatalf("focused cellDeps nodes = %v, want [accesscore]", got)
	}
}

func TestCatalog_IncludeMask_OnlyRelations(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/devtools/catalog?include=relations")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var doc catalog.Document
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// include=relations only — cellDeps/packageDeps/statusBoard absent.
	if doc.Dependencies != nil {
		if doc.Dependencies.Cells != nil {
			t.Error("cellDeps should be absent when include=relations")
		}
		if doc.Dependencies.Packages != nil {
			t.Error("packageDeps should be absent when include=relations")
		}
	}
	if len(doc.StatusBoard) > 0 {
		t.Error("statusBoard should be absent when include=relations")
	}
}

func TestCatalog_IncludeMask_NoFlags_DefaultsAll(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/devtools/catalog")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var doc catalog.Document
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Dependencies == nil {
		t.Error("expected Dependencies block when no include filter (AllIncluded)")
	}
}

func TestCatalog_FormatYAML(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/devtools/catalog?format=yaml", nil)
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if ct != "application/yaml" {
		t.Errorf("Content-Type = %q, want application/yaml", ct)
	}
}

func TestCatalog_BadKind(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/devtools/catalog?kinds=Frobnicator")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown kind, got %d: %s", rr.Code, rr.Body.String())
	}
	assertErrValidation(t, rr)
	if strings.Contains(rr.Body.String(), "Frobnicator") {
		t.Errorf("public error must not echo invalid kind token: %s", rr.Body.String())
	}
}

func TestCatalog_BadLayer(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/devtools/catalog?layers=nonexistent_layer")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown layer, got %d: %s", rr.Code, rr.Body.String())
	}
	assertErrValidation(t, rr)
	if strings.Contains(rr.Body.String(), "nonexistent_layer") {
		t.Errorf("public error must not echo invalid layer token: %s", rr.Body.String())
	}
}

func TestCatalog_BadInclude(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/devtools/catalog?include=unknownFlag")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown include flag, got %d: %s", rr.Code, rr.Body.String())
	}
	assertErrValidation(t, rr)
	if strings.Contains(rr.Body.String(), "unknownFlag") {
		t.Errorf("public error must not echo invalid include token: %s", rr.Body.String())
	}
}

// assertErrValidation checks that the response body contains an ERR_VALIDATION_*
// error code.
func assertErrValidation(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("response missing 'error' field: %v", body)
	}
	code, _ := errObj["code"].(string)
	if !strings.HasPrefix(code, "ERR_VALIDATION_") {
		t.Errorf("error code = %q, want ERR_VALIDATION_* prefix", code)
	}
}

// TestCatalog_PackageDeps_NilGraph verifies that when pkgGraph is nil, the
// packageDeps block is absent from the response (build-time graph not generated).
func TestCatalog_PackageDeps_NilGraph(t *testing.T) {
	t.Parallel()

	project := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
		Actors:     []metadata.ActorMeta{},
	}
	// nil pkgGraph — packageDeps block must be absent.
	h := devtools.NewHandler(project, nil, nil, "/root", clock.Real())

	rr := doAdminRequest(t, h, "/devtools/catalog")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var doc catalog.Document
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Dependencies != nil && doc.Dependencies.Packages != nil {
		t.Error("expected packageDeps block absent when pkgGraph is nil")
	}
}

// TestCatalog_PackageDeps_NonNilGraph verifies that when pkgGraph is non-nil,
// the packageDeps block is present with Graph != nil.
func TestCatalog_PackageDeps_NonNilGraph(t *testing.T) {
	t.Parallel()

	project := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
		Actors:     []metadata.ActorMeta{},
	}
	h := devtools.NewHandler(project, nil, minimalPkgGraph(), "/root", clock.Real())

	rr := doAdminRequest(t, h, "/devtools/catalog")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var doc catalog.Document
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Dependencies == nil || doc.Dependencies.Packages == nil {
		t.Fatal("expected Dependencies.Packages block when pkgGraph is non-nil")
	}
	if doc.Dependencies.Packages.Graph == nil {
		t.Error("packages Graph must be non-nil when pkgGraph is provided")
	}
}

// TestCatalog_IncludeAbsent_DefaultsAll verifies that omitting ?include= returns
// AllIncluded (dependencies block present).
func TestCatalog_IncludeAbsent_DefaultsAll(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/devtools/catalog")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var doc catalog.Document
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Dependencies == nil {
		t.Error("expected Dependencies block when include param is absent (AllIncluded default)")
	}
}

// TestCatalog_IncludeExplicitEmpty_ZeroBlocks verifies that ?include= (present
// but empty) returns zero optional blocks.
func TestCatalog_IncludeExplicitEmpty_ZeroBlocks(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/devtools/catalog?include=")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var doc catalog.Document
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Dependencies != nil {
		t.Error("expected no Dependencies block when include= is explicitly empty")
	}
	if len(doc.StatusBoard) > 0 {
		t.Error("expected no StatusBoard when include= is explicitly empty")
	}
}

// TestCatalog_FormatXML_BadRequest verifies that ?format=xml returns 400.
func TestCatalog_FormatXML_BadRequest(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/devtools/catalog?format=xml")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown format, got %d: %s", rr.Code, rr.Body.String())
	}
	assertErrValidation(t, rr)
	if strings.Contains(rr.Body.String(), "xml") {
		t.Errorf("public error must not echo invalid format token: %s", rr.Body.String())
	}
}

// TestCatalog_Root_RelativePath verifies that HTTP handler sets doc.Root to "."
// (relative path) so absolute server paths are not exposed to clients.
func TestCatalog_Root_RelativePath(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/devtools/catalog?include=")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var doc catalog.Document
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Root != "." {
		t.Errorf("doc.Root = %q, want \".\" (HTTP must not expose absolute paths)", doc.Root)
	}
}

func containsString(vals []string, want string) bool {
	for _, v := range vals {
		if v == want {
			return true
		}
	}
	return false
}
