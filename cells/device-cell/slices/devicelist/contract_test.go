package devicelist

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/device-cell/internal/mem"
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
	h := NewHandler(svc)

	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/devices", auth.RequirePolicy(auth.Authenticated())(http.HandlerFunc(h.HandleList)))
	return mux
}

func TestHttpDeviceListV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.device.list.v1")
	h := newContractDeviceListHandler(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, nil)
	req = req.WithContext(auth.TestContext("user-1", nil))
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	// MustRejectResponse: wrong shape (missing required fields)
	c.MustRejectResponse(t, []byte(`{"data":{"wrong":"shape"}}`))
	// MustRejectResponse: data is not an array
	c.MustRejectResponse(t, []byte(`{"data":{"id":"dev-1"},"hasMore":false}`))
}
