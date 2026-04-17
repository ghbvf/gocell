package devicecommand

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/device-cell/internal/mem"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

func newContractCommandHandler() (http.Handler, *mem.DeviceRepository, *mem.CommandRepository) {
	devRepo := mem.NewDeviceRepository()
	cmdRepo := mem.NewCommandRepository()
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	svc := NewService(cmdRepo, devRepo, codec, slog.Default(), query.RunModeProd)
	h := NewHandler(svc)
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/devices/{id}/commands", http.HandlerFunc(h.HandleEnqueue))
	mux.Handle("GET /api/v1/devices/{id}/commands", http.HandlerFunc(h.HandleListPending))
	mux.Handle("POST /api/v1/devices/{id}/commands/{cmdId}/ack", http.HandlerFunc(h.HandleAck))
	return mux, devRepo, cmdRepo
}

// --- HTTP contract tests (real handler) ---

func TestHttpDeviceCommandEnqueueV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.device.command.enqueue.v1")

	handler, devRepo, _ := newContractCommandHandler()
	_ = devRepo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "sensor-a", Status: "online",
	})

	c.ValidateRequest(t, []byte(`{"payload":"reboot"}`))
	c.MustRejectRequest(t, []byte(`{"payload":"x","extra":"bad"}`))

	rec := httptest.NewRecorder()
	path := strings.Replace(c.HTTP.Path, "{id}", "dev-1", 1)
	req := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(`{"payload":"reboot"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("operator-1", []string{"operator"}))
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

func TestHttpDeviceCommandListV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.device.command.list.v1")

	handler, devRepo, cmdRepo := newContractCommandHandler()
	_ = devRepo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "sensor-a", Status: "online",
	})
	_ = cmdRepo.Create(context.Background(), &domain.Command{
		ID: "cmd-1", DeviceID: "dev-1", Payload: "reboot", Status: "pending",
	})

	rec := httptest.NewRecorder()
	path := strings.Replace(c.HTTP.Path, "{id}", "dev-1", 1)
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	req = req.WithContext(auth.TestContext("dev-1", nil))
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

func TestHttpDeviceCommandAckV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.device.command.ack.v1")

	handler, devRepo, cmdRepo := newContractCommandHandler()
	_ = devRepo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "sensor-a", Status: "online",
	})
	_ = cmdRepo.Create(context.Background(), &domain.Command{
		ID: "cmd-1", DeviceID: "dev-1", Payload: "reboot", Status: "pending",
	})

	rec := httptest.NewRecorder()
	path := strings.Replace(c.HTTP.Path, "{id}", "dev-1", 1)
	path = strings.Replace(path, "{cmdId}", "cmd-1", 1)
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	req = req.WithContext(auth.TestContext("dev-1", nil))
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

// --- Command-kind contract tests (schema validation) ---

func TestCommandDeviceCommandEnqueueV1Handle(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "command.device-command.enqueue.v1")

	c.ValidateRequest(t, []byte(`{"payload":"reboot"}`))
	c.ValidateResponse(t, []byte(`{"data":{"id":"cmd-1","deviceId":"d-1","payload":"reboot","status":"pending","createdAt":"2026-01-01T00:00:00Z"}}`))
	c.MustRejectRequest(t, []byte(`{"payload":"x","extra":"bad"}`))
}

func TestCommandDeviceCommandListV1Handle(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "command.device-command.list.v1")

	c.ValidateResponse(t, []byte(`{"data":[{"id":"cmd-1","deviceId":"d-1","payload":"reboot","status":"pending","createdAt":"2026-01-01T00:00:00Z"}],"hasMore":false}`))
	c.MustRejectResponse(t, []byte(`{"data":"not-array","hasMore":false}`))
}

func TestCommandDeviceCommandAckV1Handle(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "command.device-command.ack.v1")

	c.ValidateResponse(t, []byte(`{"data":{"status":"acked"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}
