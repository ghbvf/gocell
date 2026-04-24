package devicecommand

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// newContractCommandHandler wires h.RegisterRoutes as the single source of
// truth for route+policy metadata. TestMux.Route mirrors production chi so
// auth.Mount strips "/api/v1/devices" off Contract.Path directly — no
// StripPrefix or relative-alias magic.
func newContractCommandHandler() (http.Handler, *mem.DeviceRepository, *commandtest.InMemQueue) {
	devRepo := mem.NewDeviceRepository()
	q := commandtest.NewInMemQueue()
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	svc, err := NewService(q, devRepo, codec, slog.Default(), query.RunModeProd)
	if err != nil {
		panic(err)
	}
	h := NewHandler(svc)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/devices", func(sub cell.RouteMux) { h.RegisterRoutes(sub) })
	return mux, devRepo, q
}

// --- HTTP contract tests (real handler) ---

func TestHttpDeviceCommandEnqueueV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot("iotdevice")
	c := contracttest.LoadByID(t, root, "http.device.command.enqueue.v1")

	handler, devRepo, _ := newContractCommandHandler()
	_ = devRepo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "sensor-a", Status: "online",
	})

	// request schema: payload required, commandType optional
	c.ValidateRequest(t, []byte(`{"payload":"reboot"}`))
	c.ValidateRequest(t, []byte(`{"payload":"reboot","commandType":"firmware-update"}`))
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
	root := contracttest.ExampleContractsRoot("iotdevice")
	c := contracttest.LoadByID(t, root, "http.device.command.dequeue.v1")

	handler, devRepo, q := newContractCommandHandler()
	_ = devRepo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "sensor-a", Status: "online",
	})
	_ = q.Enqueue(context.Background(),
		command.NewEntry("cmd-1", "dev-1", "reboot", []byte("reboot"), command.Timeouts{}, time.Now()),
		command.EnqueueOptions{})

	rec := httptest.NewRecorder()
	path := strings.Replace(c.HTTP.Path, "{id}", "dev-1", 1)
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	req = req.WithContext(auth.TestContext("dev-1", nil))
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

func TestHttpDeviceCommandAckV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot("iotdevice")
	c := contracttest.LoadByID(t, root, "http.device.command.ack.v1")

	handler, devRepo, q := newContractCommandHandler()
	_ = devRepo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "sensor-a", Status: "online",
	})
	_ = q.Enqueue(context.Background(),
		command.NewEntry("cmd-1", "dev-1", "reboot", []byte("reboot"), command.Timeouts{}, time.Now()),
		command.EnqueueOptions{})
	_, _ = q.Dequeue(context.Background(), "dev-1", 1, command.DefaultLeaseDuration)

	rec := httptest.NewRecorder()
	path := strings.Replace(c.HTTP.Path, "{id}", "dev-1", 1)
	path = strings.Replace(path, "{cmdId}", "cmd-1", 1)
	req := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(`{"reason":"success"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("dev-1", nil))
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

// --- Command-kind contract tests (schema validation) ---

func TestCommandDeviceCommandEnqueueV1Handle(t *testing.T) {
	root := contracttest.ExampleContractsRoot("iotdevice")
	c := contracttest.LoadByID(t, root, "command.device-command.enqueue.v1")

	// payload required; commandType optional
	c.ValidateRequest(t, []byte(`{"payload":"reboot"}`))
	c.ValidateRequest(t, []byte(`{"payload":"reboot","commandType":"firmware-update"}`))
	c.ValidateResponse(t, []byte(`{"data":{"id":"cmd-1","deviceId":"d-1","commandType":"reboot","payload":"reboot","status":"pending","attempt":0,"createdAt":"2026-01-01T00:00:00Z"}}`))
	c.MustRejectRequest(t, []byte(`{"payload":"x","extra":"bad"}`))
}

func TestCommandDeviceCommandListV1Handle(t *testing.T) {
	root := contracttest.ExampleContractsRoot("iotdevice")
	c := contracttest.LoadByID(t, root, "command.device-command.dequeue.v1")

	c.ValidateResponse(t, []byte(`{"data":[{"id":"cmd-1","deviceId":"d-1","commandType":"reboot","payload":"reboot","status":"sent","attempt":1,"createdAt":"2026-01-01T00:00:00Z","sentAt":"2026-01-01T00:00:01Z"}]}`))
	c.MustRejectResponse(t, []byte(`{"data":"not-array"}`))
}

func TestCommandDeviceCommandAckV1Handle(t *testing.T) {
	root := contracttest.ExampleContractsRoot("iotdevice")
	c := contracttest.LoadByID(t, root, "command.device-command.ack.v1")

	c.ValidateRequest(t, []byte(`{"reason":"success"}`))
	c.ValidateRequest(t, []byte(`{"reason":"failure"}`))
	c.MustRejectRequest(t, []byte(`{"reason":"retry"}`))
	c.ValidateResponse(t, []byte(`{"data":{"id":"cmd-1","deviceId":"d-1","commandType":"reboot","payload":"reboot","status":"succeeded","attempt":0,"createdAt":"2026-01-01T00:00:00Z","completedAt":"2026-01-01T00:01:00Z"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}
