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

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecmd"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/dto"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/command"
	"github.com/ghbvf/gocell/framework/kernel/command/commandtest"
	"github.com/ghbvf/gocell/framework/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/tests/contracttest"
)

// newContractCommandHandler wires all public handlers via the composite NewHandler
// on a TestMux. TestMux.Route mirrors production chi so auth.Mount strips
// "/api/v1/devices" off Contract.Path directly — no StripPrefix magic.
// Note: the internal list handler lives in devicecommandinternal; its contract
// test is in that package.
func newContractCommandHandler() (http.Handler, *mem.DeviceRepository, *commandtest.InMemQueue) {
	devRepo := mem.NewDeviceRepository()
	q := commandtest.NewInMemQueue()
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	svc, err := devicecmd.NewService(
		clock.Real(), q, devRepo, codec, slog.Default(), query.RunModeProd,
		devicecmd.WithSliceName("devicecommand"),
	)
	if err != nil {
		panic(err)
	}

	h := NewHandler(svc)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/devices", func(sub cell.RouteMux) {
		if err := h.RegisterRoutes(sub); err != nil {
			panic(err)
		}
	})
	return mux, devRepo, q
}

// --- HTTP contract tests (real handler) ---

func TestHttpDeviceCommandEnqueueV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "iotdevice")
	c := contracttest.LoadByID(t, root, "http.device.command.enqueue.v1")

	handler, devRepo, _ := newContractCommandHandler()
	_ = devRepo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "sensor-a", Status: "online",
	})

	// request schema: payload + commandType both required + non-empty (#1694 F9:
	// commandType made required — the silent "default" fallback was removed).
	c.ValidateRequest(t, []byte(`{"payload":"reboot","commandType":"reboot"}`))
	c.MustRejectRequest(t, []byte(`{"payload":"reboot"}`))                                        // missing required commandType
	c.MustRejectRequest(t, []byte(`{"payload":"x","commandType":""}`))                            // commandType minLength 1
	c.MustRejectRequest(t, []byte(`{"payload":"x","commandType":"`+strings.Repeat("a", 65)+`"}`)) // commandType maxLength 64
	c.MustRejectRequest(t, []byte(`{"commandType":"x"}`))                                         // missing required payload
	c.MustRejectRequest(t, []byte(`{"payload":"","commandType":"x"}`))                            // payload minLength 1
	c.MustRejectRequest(t, []byte(`{"payload":"x","commandType":"y","extra":"bad"}`))

	rec := httptest.NewRecorder()
	path := strings.Replace(c.HTTP.Path, "{id}", "dev-1", 1)
	req := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(`{"payload":"reboot","commandType":"reboot"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withTestAuth("operator-1", []string{dto.RoleOperator}))
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

func TestHttpDeviceCommandEnqueueAsyncV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "iotdevice")
	c := contracttest.LoadByID(t, root, "http.device.command.enqueue-async.v1")

	// request schema mirrors enqueue: payload required + non-empty, commandType optional.
	c.ValidateRequest(t, []byte(`{"payload":"reboot"}`))
	c.ValidateRequest(t, []byte(`{"payload":"reboot","commandType":"firmware-update"}`))
	c.MustRejectRequest(t, []byte(`{"payload":""}`))                // payload minLength 1
	c.MustRejectRequest(t, []byte(`{"payload":"x","extra":"bad"}`)) // additionalProperties false
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))

	// emitter-wired handler with device "dev-1" seeded; the RequestIdentity is
	// injected into ctx as the HTTP idempotency middleware would (the TestMux omits
	// the middleware, so the bridge reads it from here).
	rec := outboxtest.NewRecorder()
	handler := newSeededAsyncHandler(t, rec.CellEmitter(), "dev-1")
	path := strings.Replace(c.HTTP.Path, "{id}", "dev-1", 1)

	// 202 happy path (operator) + emit assertion.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(`{"payload":"reboot"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(reqIDCtx(t, withTestAuth("operator-1", []string{dto.RoleOperator}), "operator-1", "idem-contract-1"))
	handler.ServeHTTP(w, req)
	c.ValidateHTTPResponseRecorder(t, w) // 202 + response schema
	if n := len(rec.Entries()); n != 1 {
		t.Fatalf("async enqueue must emit exactly one command, got %d", n)
	}

	// auth boundary: RequirePermission(PermDeviceCommand()) → PDP deny for non-admin/operator.
	for _, tc := range []struct {
		name  string
		roles []string
	}{
		{"device role denied", []string{"device"}},
		{"no role denied", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ww := httptest.NewRecorder()
			rr := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(`{"payload":"reboot"}`))
			rr.Header.Set("Content-Type", "application/json")
			rr = rr.WithContext(reqIDCtx(t, withTestAuth("sub", tc.roles), "sub", "idem-deny"))
			handler.ServeHTTP(ww, rr)
			if ww.Code != http.StatusForbidden {
				t.Errorf("want 403, got %d", ww.Code)
			}
		})
	}

	// path param: overlong {id} (>256) → 400 (generated handler validates 1..256).
	t.Run("overlong id 400", func(t *testing.T) {
		ww := httptest.NewRecorder()
		longPath := strings.Replace(c.HTTP.Path, "{id}", strings.Repeat("d", 257), 1)
		rr := httptest.NewRequest(c.HTTP.Method, longPath, strings.NewReader(`{"payload":"reboot"}`))
		rr.Header.Set("Content-Type", "application/json")
		rr = rr.WithContext(reqIDCtx(t, withTestAuth("admin-user", []string{dto.RoleAdmin}), "admin-user", "idem-long"))
		handler.ServeHTTP(ww, rr)
		if ww.Code != http.StatusBadRequest {
			t.Errorf("want 400 for overlong id, got %d", ww.Code)
		}
	})

	// missing Idempotency-Key (no RequestIdentity) → 400 at the bridge.
	t.Run("missing idempotency key 400", func(t *testing.T) {
		ww := httptest.NewRecorder()
		rr := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(`{"payload":"reboot"}`))
		rr.Header.Set("Content-Type", "application/json")
		rr = rr.WithContext(withTestAuth("admin-user", []string{dto.RoleAdmin}))
		handler.ServeHTTP(ww, rr)
		if ww.Code != http.StatusBadRequest {
			t.Errorf("want 400 for missing Idempotency-Key, got %d", ww.Code)
		}
	})
}

func TestHttpDeviceCommandDequeueV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "iotdevice")
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
	req = req.WithContext(withTestAuth("dev-1", nil))
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

func TestHttpDeviceCommandAckV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "iotdevice")
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
	req = req.WithContext(withTestAuth("dev-1", nil))
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	// enum was removed from the schema to support codegen; validate remaining constraints.
	c.MustRejectRequest(t, []byte(`{}`))                  // missing required "reason"
	c.MustRejectRequest(t, []byte(`{"reason":""}`))       // minLength: 1 violation
	c.MustRejectRequest(t, []byte(`{"unknown":"field"}`)) // additionalProperties: false
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestHttpDeviceCommandReportV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "iotdevice")
	c := contracttest.LoadByID(t, root, "http.device.command.report.v1")

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
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	req = req.WithContext(withTestAuth("dev-1", nil))
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

func TestHttpDeviceCommandExtendLeaseV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "iotdevice")
	c := contracttest.LoadByID(t, root, "http.device.command.extend-lease.v1")

	handler, devRepo, q := newContractCommandHandler()
	_ = devRepo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "sensor-a", Status: "online",
	})
	_ = q.Enqueue(context.Background(),
		command.NewEntry("cmd-1", "dev-1", "reboot", []byte("reboot"), command.Timeouts{}, time.Now()),
		command.EnqueueOptions{})
	_, _ = q.Dequeue(context.Background(), "dev-1", 1, command.DefaultLeaseDuration)

	c.ValidateRequest(t, []byte(`{"extensionSeconds":60}`))
	c.MustRejectRequest(t, []byte(`{"extensionSeconds":0}`))
	c.MustRejectRequest(t, []byte(`{"extensionSeconds":3601}`))
	c.MustRejectRequest(t, []byte(`{"extensionSeconds":60,"extra":"bad"}`))

	rec := httptest.NewRecorder()
	path := strings.Replace(c.HTTP.Path, "{id}", "dev-1", 1)
	path = strings.Replace(path, "{cmdId}", "cmd-1", 1)
	req := httptest.NewRequest(c.HTTP.Method, path, strings.NewReader(`{"extensionSeconds":60}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withTestAuth("dev-1", nil))
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

// --- Command-kind contract tests (schema validation) ---

func TestCommandRemoteCommandV1Handle(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "iotdevice")
	c := contracttest.LoadByID(t, root, "command.remotecommand.v1")

	// deviceId + payload + commandType all required (#1580: deviceId added so the
	// sync command-bus handler can target a device — see RemoteCommandAdapter;
	// #1694 F9: commandType made required, silent "default" fallback removed).
	c.ValidateRequest(t, []byte(`{"deviceId":"d-1","payload":"reboot","commandType":"firmware-update"}`))
	c.MustRejectRequest(t, []byte(`{"deviceId":"d-1","payload":"reboot"}`))             // missing required commandType
	c.MustRejectRequest(t, []byte(`{"payload":"reboot","commandType":"x"}`))            // missing required deviceId
	c.MustRejectRequest(t, []byte(`{"deviceId":"","payload":"x","commandType":"y"}`))   // deviceId minLength 1
	c.MustRejectRequest(t, []byte(`{"deviceId":"d-1","payload":"","commandType":"y"}`)) // payload minLength 1 (Service.Enqueue rejects empty)
	enqueueResp := `{"data":{"id":"cmd-1","deviceId":"d-1","commandType":"reboot",` +
		`"payload":"reboot","status":"pending","attempt":0,"createdAt":"2026-01-01T00:00:00Z"}}`
	c.ValidateResponse(t, []byte(enqueueResp))
	c.MustRejectRequest(t, []byte(`{"deviceId":"d-1","payload":"x","extra":"bad"}`))
}

func TestCommandDeviceCommandDequeueV1Handle(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "iotdevice")
	c := contracttest.LoadByID(t, root, "command.devicecommand.dequeue.v1")

	dequeueResp := `{"data":[{"id":"cmd-1","deviceId":"d-1","commandType":"reboot",` +
		`"payload":"reboot","status":"sent","attempt":1,` +
		`"createdAt":"2026-01-01T00:00:00Z","sentAt":"2026-01-01T00:00:01Z"}]}`
	c.ValidateResponse(t, []byte(dequeueResp))
	c.MustRejectResponse(t, []byte(`{"data":"not-array"}`))
}

func TestCommandDeviceCommandAckV1Handle(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "iotdevice")
	c := contracttest.LoadByID(t, root, "command.devicecommand.ack.v1")

	c.ValidateRequest(t, []byte(`{"reason":"success"}`))
	c.ValidateRequest(t, []byte(`{"reason":"failure"}`))
	c.MustRejectRequest(t, []byte(`{"reason":"timeout"}`))
	c.MustRejectRequest(t, []byte(`{"reason":"failed"}`))
	c.MustRejectRequest(t, []byte(`{"reason":"retry"}`))
	ackResp := `{"data":{"id":"cmd-1","deviceId":"d-1","commandType":"reboot",` +
		`"payload":"reboot","status":"succeeded","attempt":0,` +
		`"createdAt":"2026-01-01T00:00:00Z","completedAt":"2026-01-01T00:01:00Z"}}`
	c.ValidateResponse(t, []byte(ackResp))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestCommandDeviceCommandReportV1Handle(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "iotdevice")
	c := contracttest.LoadByID(t, root, "command.devicecommand.report.v1")

	c.ValidateRequest(t, []byte(`{}`))
	c.MustRejectRequest(t, []byte(`{"extra":"bad"}`))
	reportResp := `{"data":{"id":"cmd-1","deviceId":"d-1","commandType":"reboot",` +
		`"payload":"reboot","status":"delivered","attempt":1,` +
		`"createdAt":"2026-01-01T00:00:00Z","sentAt":"2026-01-01T00:00:01Z",` +
		`"deliveredAt":"2026-01-01T00:00:02Z"}}`
	c.ValidateResponse(t, []byte(reportResp))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestCommandDeviceCommandExtendLeaseV1Handle(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "iotdevice")
	c := contracttest.LoadByID(t, root, "command.devicecommand.extend-lease.v1")

	c.ValidateRequest(t, []byte(`{"extensionSeconds":60}`))
	c.MustRejectRequest(t, []byte(`{"extensionSeconds":0}`))
	c.MustRejectRequest(t, []byte(`{"extensionSeconds":3601}`))
	c.MustRejectRequest(t, []byte(`{"extensionSeconds":60,"extra":"bad"}`))
	leaseResp := `{"data":{"id":"cmd-1","deviceId":"d-1","commandType":"reboot",` +
		`"payload":"reboot","status":"sent","attempt":1,` +
		`"createdAt":"2026-01-01T00:00:00Z","sentAt":"2026-01-01T00:00:01Z"}}`
	c.ValidateResponse(t, []byte(leaseResp))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}
