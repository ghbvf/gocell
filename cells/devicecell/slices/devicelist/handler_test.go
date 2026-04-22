package devicelist

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

func newHandlerForTest(t *testing.T) *Handler {
	t.Helper()
	repo := mem.NewDeviceRepository()
	_ = repo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "alpha", Status: "online", LastSeen: time.Now(),
	})
	svc, err := NewService(repo, newTestCodec(t), slog.Default(), query.RunModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(svc)
}

func TestHandleList_OK(t *testing.T) {
	h := newHandlerForTest(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(auth.TestContext("user-1", []string{"admin"}))

	h.HandleList(w, r)

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
	r = r.WithContext(auth.TestContext("user-1", nil))

	h.HandleList(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", w.Code)
	}
}

func TestHandleList_LimitExceedsMax(t *testing.T) {
	h := newHandlerForTest(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/?limit=9999", nil)
	r = r.WithContext(auth.TestContext("user-1", nil))

	h.HandleList(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", w.Code)
	}
}
