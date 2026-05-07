package devicestatus

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	statuscontract "github.com/ghbvf/gocell/generated/contracts/http/device/status/v1"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/tests/contracttest"
)

// contractSpecID mirrors the generated contractSpec for test assertions.
var (
	contractSpecID     = "http.device.status.v1"
	contractSpecMethod = "GET"
	contractSpecPath   = "/api/v1/devices/{id}/status"
)

func newContractHandler() http.Handler {
	repo := mem.NewDeviceRepository()
	_ = repo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "sensor-01", Status: "online",
		LastSeen: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	svc := NewService(repo, slog.Default())
	handler := statuscontract.NewHandler(svc, auth.SelfOr("id", "admin"))
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/devices/{id}/status", handler)
	return mux
}

func TestDeviceStatusContractSpecMatchesContract(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "iotdevice")
	c := contracttest.LoadByID(t, root, contractSpecID)
	require.NotNil(t, c.HTTP)
	require.Equal(t, contractSpecID, c.ID)
	require.Equal(t, contractSpecMethod, c.HTTP.Method)
	require.Equal(t, contractSpecPath, c.HTTP.Path)
}

func TestHttpDeviceStatusV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "iotdevice")
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
	root := contracttest.ExampleContractsRoot(t, "iotdevice")
	c := contracttest.LoadByID(t, root, "http.device.status.v1")
	h := newContractHandler()

	path := strings.Replace(c.HTTP.Path, "{id}", "no-such-id", 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	h.ServeHTTP(rec, req)
	require.NotEqual(t, c.HTTP.SuccessStatus, rec.Code, "non-existent device must not return success")
}
