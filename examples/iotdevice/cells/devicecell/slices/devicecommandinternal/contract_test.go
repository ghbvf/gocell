package devicecommandinternal

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecmd"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/tests/contracttest"
)

// newContractInternalCommandHandler wires the internal list handler via the
// composite NewHandler on a TestMux.
func newContractInternalCommandHandler() (*celltest.TestMux, *mem.DeviceRepository, *commandtest.InMemQueue) {
	devRepo := mem.NewDeviceRepository()
	q := commandtest.NewInMemQueue()
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	svc, err := devicecmd.NewService(
		q, devRepo, codec, slog.Default(), query.RunModeProd,
		devicecmd.WithClock(clock.Real()),
		devicecmd.WithSliceName("devicecommandinternal"),
	)
	if err != nil {
		panic(err)
	}

	h := NewHandler(svc)
	mux := celltest.NewTestMux()
	if err := h.RegisterRoutes(mux); err != nil {
		panic(err)
	}
	return mux, devRepo, q
}

func TestHttpInternalDeviceCommandsListV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "iotdevice")
	c := contracttest.LoadByID(t, root, "http.internal.devicecommands.list.v1")

	mux, devRepo, q := newContractInternalCommandHandler()
	_ = devRepo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "sensor-a", Status: "online",
	})
	_ = q.Enqueue(context.Background(),
		command.NewEntry("cmd-1", "dev-1", "reboot", []byte("reboot"), command.Timeouts{}, time.Now()),
		command.EnqueueOptions{})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path+"?deviceId=dev-1&statuses=pending", nil)
	req = req.WithContext(auth.TestServiceContext("devicecell"))
	mux.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

// TestCommandInternalListTypeAlias verifies the composite handler construction works.
func TestCommandInternalListTypeAlias(t *testing.T) {
	devRepo := mem.NewDeviceRepository()
	q := commandtest.NewInMemQueue()
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	svc, err := devicecmd.NewService(
		q, devRepo, codec, slog.Default(), query.RunModeProd,
		devicecmd.WithClock(clock.Real()),
		devicecmd.WithSliceName("devicecommandinternal"),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Verify composite handler construction and route registration.
	h := NewHandler(svc)
	mux := celltest.NewTestMux()
	if err := h.RegisterRoutes(mux); err != nil {
		t.Fatal(err)
	}
}
