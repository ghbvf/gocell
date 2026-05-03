package devtools_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/http/devtools"
)

const (
	catalogTestPollTimeout  = 5 * time.Second
	catalogTestPollInterval = 5 * time.Millisecond
)

// stubLoader returns a PackageDepLoader whose View immediately becomes the
// given fixed value.
func stubLoader(t *testing.T, view *metadata.PackageDepsView) *devtools.PackageDepLoader {
	t.Helper()
	loader := devtools.NewPackageDepLoader(
		context.Background(),
		"/stub-root",
		clock.Real(),
		devtools.LoadFunc(func(_ context.Context, _ string) *metadata.PackageDepsView {
			return view
		}),
	)
	t.Cleanup(func() {
		if err := loader.Close(); err != nil {
			t.Errorf("stubLoader.Close: %v", err)
		}
	})
	return loader
}

// buildTestHandler constructs a Handler with synthetic ProjectMeta data.
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
		},
		Slices:      map[string]*metadata.SliceMeta{},
		Contracts:   map[string]*metadata.ContractMeta{},
		Journeys:    map[string]*metadata.JourneyMeta{},
		Assemblies:  map[string]*metadata.AssemblyMeta{},
		StatusBoard: []metadata.StatusBoardEntry{},
		Actors:      []metadata.ActorMeta{},
	}
	cellGraph := &metadata.CellDepGraph{
		Nodes: []string{"accesscore"},
		Edges: []metadata.CellEdge{},
	}
	loader := stubLoader(t, &metadata.PackageDepsView{Status: "ready"})
	return devtools.NewHandler(project, cellGraph, loader, "/test-root", clock.Real())
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
	rr := doAdminRequest(t, h, "/api/v1/devtools/catalog")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var doc metadata.Document
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if doc.SchemaVersion != metadata.SchemaVersionV1 {
		t.Errorf("schemaVersion = %q, want %q", doc.SchemaVersion, metadata.SchemaVersionV1)
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
//
// End-to-end 401/403 enforcement is covered by bootstrap integration tests
// that exercise the full listener auth chain + router Policy pipeline.
func TestCatalog_Policy_EnforcedByRouter_NotByHandler(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)

	// Non-admin user — handler does not enforce Policy directly; router does.
	rrNonAdmin := doUserRequest(t, h, "/api/v1/devtools/catalog")
	if rrNonAdmin.Code != http.StatusOK {
		t.Errorf("expected handler to pass through non-admin (policy enforced by router), got %d: %s",
			rrNonAdmin.Code, rrNonAdmin.Body.String())
	}

	// No subject at all — same; handler passes through without enforcing Policy.
	rrAnon := doAnonRequest(t, h, "/api/v1/devtools/catalog")
	if rrAnon.Code != http.StatusOK {
		t.Errorf("expected handler to pass through unauthenticated (policy enforced by router), got %d: %s",
			rrAnon.Code, rrAnon.Body.String())
	}
}

func TestCatalog_FilterKinds(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/api/v1/devtools/catalog?kinds=Cell,Contract")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var doc metadata.Document
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
	rr := doAdminRequest(t, h, "/api/v1/devtools/catalog?layers=cells")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCatalog_CellsFocus(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/api/v1/devtools/catalog?cells=accesscore")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCatalog_IncludeMask_OnlyRelations(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/api/v1/devtools/catalog?include=relations")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var doc metadata.Document
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
	rr := doAdminRequest(t, h, "/api/v1/devtools/catalog")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var doc metadata.Document
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Dependencies == nil {
		t.Error("expected Dependencies block when no include filter (IncludeAll)")
	}
}

func TestCatalog_FormatYAML(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devtools/catalog?format=yaml", nil)
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/yaml") {
		t.Errorf("Content-Type = %q, want application/yaml", ct)
	}
}

func TestCatalog_BadKind(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/api/v1/devtools/catalog?kinds=Frobnicator")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown kind, got %d: %s", rr.Code, rr.Body.String())
	}
	assertErrValidation(t, rr)
}

func TestCatalog_BadLayer(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/api/v1/devtools/catalog?layers=nonexistent_layer")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown layer, got %d: %s", rr.Code, rr.Body.String())
	}
	assertErrValidation(t, rr)
}

func TestCatalog_BadInclude(t *testing.T) {
	t.Parallel()

	h := buildTestHandler(t)
	rr := doAdminRequest(t, h, "/api/v1/devtools/catalog?include=unknownFlag")

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown include flag, got %d: %s", rr.Code, rr.Body.String())
	}
	assertErrValidation(t, rr)
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

func TestCatalog_PackageDepsLoading(t *testing.T) {
	t.Parallel()

	// Create a loader that stays in "loading" state.
	ready := make(chan struct{})
	loader := devtools.NewPackageDepLoader(
		context.Background(),
		"/stub-root",
		clock.Real(),
		slowLoadFunc(ready, &metadata.PackageDepsView{Status: "ready"}),
	)
	t.Cleanup(func() {
		close(ready)
		if err := loader.Close(); err != nil {
			t.Errorf("loader.Close: %v", err)
		}
	})

	project := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
		Actors:     []metadata.ActorMeta{},
	}
	h := devtools.NewHandler(project, nil, loader, "/root", clock.Real())

	rr := doAdminRequest(t, h, "/api/v1/devtools/catalog")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var doc metadata.Document
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Dependencies == nil || doc.Dependencies.Packages == nil {
		t.Fatal("expected Dependencies.Packages in response")
	}
	if doc.Dependencies.Packages.Status != "loading" {
		t.Errorf("packages status = %q, want loading", doc.Dependencies.Packages.Status)
	}
}

func TestCatalog_PackageDepsReady(t *testing.T) {
	t.Parallel()

	loader := stubLoader(t, &metadata.PackageDepsView{Status: "ready"})

	// Wait for loader to reach ready state.
	deadline := time.Now().Add(catalogTestPollTimeout)
	for time.Now().Before(deadline) {
		if loader.View().Status == "ready" {
			break
		}
		time.Sleep(catalogTestPollInterval) //archtest:allow:test-sleep poll loop waiting for stub loader goroutine to publish initial view
	}

	project := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
		Actors:     []metadata.ActorMeta{},
	}
	h := devtools.NewHandler(project, nil, loader, "/root", clock.Real())

	rr := doAdminRequest(t, h, "/api/v1/devtools/catalog")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var doc metadata.Document
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Dependencies == nil || doc.Dependencies.Packages == nil {
		t.Fatal("expected Dependencies.Packages in response")
	}
	if doc.Dependencies.Packages.Status != "ready" {
		t.Errorf("packages status = %q, want ready", doc.Dependencies.Packages.Status)
	}
}
