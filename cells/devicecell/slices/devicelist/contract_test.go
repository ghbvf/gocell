package devicelist

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

func newContractDeviceListHandler(t *testing.T) http.Handler {
	t.Helper()
	repo := mem.NewDeviceRepository()
	now := time.Now()
	_ = repo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "sensor-alpha", Status: "online", LastSeen: now,
	})
	_ = repo.Create(context.Background(), &domain.Device{
		ID: "dev-2", Name: "sensor-beta", Status: "offline", LastSeen: now,
	})

	codec, err := query.NewCursorCodec([]byte("gocell-demo-DEVICE-CELL-key-32!!"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(repo, codec, slog.Default(), query.RunModeDemo)
	if err != nil {
		t.Fatal(err)
	}

	inner := celltest.NewTestMux()
	NewHandler(svc).RegisterRoutes(inner)

	outer := http.NewServeMux()
	prefix := "/api/v1/devices"
	stripped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + strings.TrimPrefix(r.URL.Path, prefix)
		if r2.URL.Path == "/" || r2.URL.Path == "" {
			r2.URL.Path = "/"
		}
		inner.ServeHTTP(w, r2)
	})
	outer.Handle("/api/v1/devices", stripped)
	outer.Handle("/api/v1/devices/", stripped)
	return outer
}

func TestHttpDeviceListV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.device.list.v1")
	h := newContractDeviceListHandler(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, nil)
	req = req.WithContext(auth.TestContext("user-1", []string{"admin"}))
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	c.MustRejectResponse(t, []byte(`{"data":{"wrong":"shape"}}`))
	c.MustRejectResponse(t, []byte(`{"data":{"id":"dev-1"},"hasMore":false}`))
	c.MustRejectResponse(t, []byte(`{"data":[],"hasMore":"yes"}`))

	rec400 := httptest.NewRecorder()
	req400 := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path+"?limit=notanumber", nil)
	req400 = req400.WithContext(auth.TestContext("user-1", []string{"admin"}))
	h.ServeHTTP(rec400, req400)
	if rec400.Code != http.StatusBadRequest {
		t.Errorf("invalid limit: expected 400, got %d", rec400.Code)
	}
}
