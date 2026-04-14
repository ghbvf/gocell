package devicestatus

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/device-cell/internal/mem"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/stretchr/testify/require"
)

func newContractHandler() http.Handler {
	repo := mem.NewDeviceRepository()
	_ = repo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "sensor-01", Status: "online",
		LastSeen: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	svc := NewService(repo, slog.Default())
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/devices/{id}/status", http.HandlerFunc(NewHandler(svc).HandleGetStatus))
	return mux
}

func TestHttpDeviceStatusV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.device.status.v1")
	h := newContractHandler()

	path := strings.Replace(c.HTTP.Path, "{id}", "dev-1", 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestHttpDeviceStatusV1Serve_NotFound(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.device.status.v1")
	h := newContractHandler()

	path := strings.Replace(c.HTTP.Path, "{id}", "no-such-id", 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	h.ServeHTTP(rec, req)
	require.NotEqual(t, c.HTTP.SuccessStatus, rec.Code, "non-existent device must not return success")
}
