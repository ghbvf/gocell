package rbaccheck

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/tests/contracttest"
)

// newContractRBACHandler builds a full-path mux matching the contract-declared
// routes (/api/v1/access/roles/...) so the contract test covers the complete
// routing chain, not just the relative handler paths.
func newContractRBACHandler() http.Handler {
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	roleRepo.SeedRole(&domain.Role{
		ID: "admin", Name: "admin",
		Permissions: []domain.Permission{
			{Resource: "users", Action: "read"},
		},
	})
	roleRepo.SeedRole(&domain.Role{
		ID: "operator", Name: "operator",
		Permissions: []domain.Permission{
			{Resource: "devices", Action: "write"},
		},
	})
	roleRepo.SeedRole(&domain.Role{
		ID: "viewer", Name: "viewer",
		Permissions: []domain.Permission{
			{Resource: "devices", Action: "read"},
		},
	})
	_, _ = roleRepo.AssignToUser(context.Background(), testutil.TestID("user-1"), "admin")
	_, _ = roleRepo.AssignToUser(context.Background(), testutil.TestID("user-1"), "operator")
	_, _ = roleRepo.AssignToUser(context.Background(), testutil.TestID("user-1"), "viewer")
	codec, err := query.NewCursorCodec([]byte("gocell-demo-ACCESS-CORE-key-32!!"))
	if err != nil {
		panic(err)
	}
	svc, err := NewService(roleRepo, codec, slog.Default(), query.RunModeProd)
	if err != nil {
		panic(err)
	}

	mux := celltest.NewTestMux()
	h := NewHandler(svc)
	mux.Route("/api/v1/access/roles", func(s cell.RouteMux) {
		if err := h.RegisterRoutes(s); err != nil {
			panic("newContractRBACHandler: RegisterRoutes: " + err.Error())
		}
	})
	return mux
}

type roleListPage struct {
	Data       []RoleResponse `json:"data"`
	NextCursor string         `json:"nextCursor"`
	HasMore    bool           `json:"hasMore"`
}

func decodeRoleListPage(t *testing.T, rec *httptest.ResponseRecorder) roleListPage {
	t.Helper()
	var page roleListPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode role list response: %v", err)
	}
	return page
}

// TestHttpAuthRoleListV1_ParamConstraints asserts that every declared
// path/query param has at least one reject case
// (CONTRACT-PATH-QUERY-COVERAGE-01 per-param granularity):
//   - userID path (format: uuid) rejects non-UUID strings;
//   - limit query rejects values out of range (minimum: 1, maximum: 500);
//   - cursor query rejects values exceeding maxLength: 4096.
func TestHttpAuthRoleListV1_ParamConstraints(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.role.list.v1")
	c.ValidatePathParam(t, "userID", "550e8400-e29b-41d4-a716-446655440000")
	c.MustRejectPathParam(t, "userID", "not-a-uuid") // violates format: uuid
	c.ValidateQueryParam(t, "limit", "1")
	c.MustRejectQueryParam(t, "limit", "0")                         // violates minimum: 1
	c.MustRejectQueryParam(t, "limit", "501")                       // violates maximum: 500
	c.MustRejectQueryParam(t, "cursor", string(make([]byte, 4097))) // violates maxLength: 4096
}

// TestHttpAuthRoleCheckV1_PathParamConstraints asserts that the roleName path
// param schema rejects empty string (violates minLength: 1), and the userID
// path param rejects non-UUID strings.
func TestHttpAuthRoleCheckV1_PathParamConstraints(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.role.check.v1")
	c.ValidatePathParam(t, "userID", "550e8400-e29b-41d4-a716-446655440000")
	c.MustRejectPathParam(t, "userID", "not-a-uuid") // violates format: uuid
	c.ValidatePathParam(t, "roleName", "admin")
	c.MustRejectPathParam(t, "roleName", "") // violates minLength: 1
}

func TestHttpAuthRoleListV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.role.list.v1")
	h := newContractRBACHandler()

	rec := httptest.NewRecorder()
	path := strings.Replace(c.HTTP.Path, "{userID}", testutil.TestID("user-1"), 1)
	req := httptest.NewRequest(c.HTTP.Method, path+"?limit=2", nil)
	req = req.WithContext(auth.TestContext(testutil.TestID("user-1"), nil))
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
	page1 := decodeRoleListPage(t, rec)
	if len(page1.Data) != 2 {
		t.Fatalf("page 1: expected 2 roles, got %d", len(page1.Data))
	}
	if !page1.HasMore {
		t.Fatal("page 1: expected hasMore=true")
	}
	if page1.NextCursor == "" {
		t.Fatal("page 1: expected non-empty nextCursor")
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(c.HTTP.Method, path+"?limit=2&cursor="+url.QueryEscape(page1.NextCursor), nil)
	req2 = req2.WithContext(auth.TestContext(testutil.TestID("user-1"), nil))
	h.ServeHTTP(rec2, req2)
	c.ValidateHTTPResponseRecorder(t, rec2)
	page2 := decodeRoleListPage(t, rec2)
	if len(page2.Data) != 1 {
		t.Fatalf("page 2: expected 1 role, got %d", len(page2.Data))
	}
	if page2.HasMore {
		t.Fatal("page 2: expected hasMore=false")
	}
	if page2.NextCursor != "" {
		t.Fatalf("page 2: expected empty nextCursor, got %q", page2.NextCursor)
	}

	c.MustRejectResponse(t, []byte(`{"data":[]}`))
	c.MustRejectResponse(t, []byte(`{"data":{"wrong":"shape"},"hasMore":false}`))

	rec400 := httptest.NewRecorder()
	req400 := httptest.NewRequest(c.HTTP.Method, path+"?limit=notanumber", nil)
	req400 = req400.WithContext(auth.TestContext(testutil.TestID("user-1"), nil))
	h.ServeHTTP(rec400, req400)
	if rec400.Code != http.StatusBadRequest {
		t.Errorf("invalid limit: expected 400, got %d", rec400.Code)
	}
	c.ValidateErrorResponse(t, http.StatusBadRequest, rec400.Body.Bytes())

	recBadCursor := httptest.NewRecorder()
	reqBadCursor := httptest.NewRequest(c.HTTP.Method, path+"?cursor=not-a-valid-cursor", nil)
	reqBadCursor = reqBadCursor.WithContext(auth.TestContext(testutil.TestID("user-1"), nil))
	h.ServeHTTP(recBadCursor, reqBadCursor)
	if recBadCursor.Code != http.StatusBadRequest {
		t.Errorf("invalid cursor: expected 400, got %d", recBadCursor.Code)
	}
	c.ValidateErrorResponse(t, http.StatusBadRequest, recBadCursor.Body.Bytes())
}

func TestHttpAuthRoleCheckV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.role.check.v1")
	h := newContractRBACHandler()

	rec := httptest.NewRecorder()
	path := strings.Replace(c.HTTP.Path, "{userID}", testutil.TestID("user-1"), 1)
	path = strings.Replace(path, "{roleName}", "admin", 1)
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	req = req.WithContext(auth.TestContext(testutil.TestID("user-1"), nil))
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	c.MustRejectResponse(t, []byte(`{"data":{"wrong":"shape"}}`))
}
