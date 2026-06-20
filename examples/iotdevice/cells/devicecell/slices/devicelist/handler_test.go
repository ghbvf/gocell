package devicelist

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	listcontract "github.com/ghbvf/gocell/generated/contracts/http/device/list/v1"
)

// testListAuthorizer is a test-local PDP for devicelist handler unit tests.
// It mirrors the device:list baseline in cells/devicecell/authorizer.go;
// deviceAuthorizer is package-private to devicecell, so this package
// implements an equivalent locally.
type testListAuthorizer struct{}

func (testListAuthorizer) Authorize(ctx context.Context, subject, resource, action string) (authz.Decision, error) {
	p, ok := auth.FromContext(ctx)
	if (ok && p != nil) && action == authz.PermDeviceList().String() && p.HasRole("admin") {
		return authz.Allow(authz.Obligations{})
	}
	return authz.Deny("test-list-authz: denied"), nil
}

// withListTestAuth builds a context carrying both a Principal and the list test
// PDP. device:list is a coarse role gate (subject-independent), so the subject is
// a fixed placeholder — only the roles drive the decision.
func withListTestAuth(roles []string) context.Context {
	return auth.WithAuthorizer(auth.TestContext("user-1", roles), testListAuthorizer{})
}

func newHandlerForTest(t *testing.T) *listcontract.Handler {
	t.Helper()
	repo := mem.NewDeviceRepository()
	_ = repo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "alpha", Status: "online", LastSeen: time.Now(),
	})
	svc, err := NewService(repo, newTestCodec(t), slog.Default(), query.RunModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	return listcontract.NewHandler(svc, testResolver())
}

func TestHandleList_OK(t *testing.T) {
	h := newHandlerForTest(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(withListTestAuth([]string{"admin"}))

	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status=%d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if _, ok := body["data"]; !ok {
		t.Error("response missing 'data' field")
	}
	if _, ok := body["hasMore"]; !ok {
		t.Error("response missing 'hasMore' field")
	}
}

func TestHandleList_InvalidLimit(t *testing.T) {
	h := newHandlerForTest(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/?limit=abc", nil)
	r = r.WithContext(withListTestAuth([]string{"admin"}))

	h.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", w.Code)
	}
}

func TestHandleList_LimitExceedsMax(t *testing.T) {
	h := newHandlerForTest(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/?limit=9999", nil)
	r = r.WithContext(withListTestAuth([]string{"admin"}))

	h.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", w.Code)
	}
}
