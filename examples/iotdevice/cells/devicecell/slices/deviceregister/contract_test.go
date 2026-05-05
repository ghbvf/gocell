package deviceregister

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	registercontract "github.com/ghbvf/gocell/generated/contracts/http/device/register/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/tests/contracttest"
)

// contractSpecID mirrors the generated contractSpec.ID for test assertions.
var (
	contractSpecID     = "http.device.register.v1"
	contractSpecMethod = "POST"
	contractSpecPath   = "/api/v1/devices"
)

// --- contract test doubles ---

type recordingPublisher struct {
	calls []publishCall
}

type publishCall struct {
	topic   string
	payload []byte
}

func (p *recordingPublisher) Publish(_ context.Context, topic string, payload []byte) error {
	p.calls = append(p.calls, publishCall{topic: topic, payload: payload})
	return nil
}
func (p *recordingPublisher) Close(_ context.Context) error { return nil }

var _ outbox.Publisher = (*recordingPublisher)(nil)

func newContractHandler() (http.Handler, *recordingPublisher) {
	repo := mem.NewDeviceRepository()
	pub := &recordingPublisher{}
	emitter, err := outbox.NewDirectEmitter(
		pub, outbox.DirectPublishFailOpen, metrics.NopProvider{}, clock.Real(), "devicecell",
		outbox.WithLogger(slog.Default()),
	)
	if err != nil {
		panic(err)
	}
	svc := NewService(repo, slog.Default(), WithEmitter(emitter), WithClock(clock.Real()))
	handler := registercontract.NewHandler(svc) // Public endpoint: NewHandler takes no policy (auth.Route{Public:true})
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/devices", handler)
	return mux, pub
}

func TestDeviceRegisterContractSpecMatchesContract(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "iotdevice")
	c := contracttest.LoadByID(t, root, contractSpecID)
	require.NotNil(t, c.HTTP)
	require.Equal(t, contractSpecID, c.ID)
	require.Equal(t, contractSpecMethod, c.HTTP.Method)
	require.Equal(t, contractSpecPath, c.HTTP.Path)
}

func TestHttpDeviceRegisterV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "iotdevice")
	c := contracttest.LoadByID(t, root, "http.device.register.v1")
	h, _ := newContractHandler()

	c.ValidateRequest(t, []byte(`{"name":"sensor-01"}`))
	c.MustRejectRequest(t, []byte(`{"name":"a","extra":"bad"}`))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(`{"name":"sensor-01"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

func TestEventDeviceRegisteredV1Publish(t *testing.T) {
	root := contracttest.ExampleContractsRoot(t, "iotdevice")
	httpContract := contracttest.LoadByID(t, root, "http.device.register.v1")
	c := contracttest.LoadByID(t, root, "event.device-registered.v1")
	h, pub := newContractHandler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(httpContract.HTTP.Method, httpContract.HTTP.Path, strings.NewReader(`{"name":"sensor-01"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	httpContract.ValidateHTTPResponseRecorder(t, rec)

	require.Len(t, pub.calls, 1, "Register must publish one event")
	// The cell wraps the business payload in a v1 wire envelope before publishing
	// so the eventbus fail-closed schema check (P1-14) accepts the message.
	// Unwrap here to validate against the business-payload contract schema.
	entry, err := outbox.UnmarshalEnvelope(pub.calls[0].topic, pub.calls[0].payload)
	require.NoError(t, err, "published payload must be a valid v1 envelope")
	c.ValidatePayload(t, entry.Payload)
	c.MustRejectPayload(t, []byte(`{"id":"d-1"}`))
	// Headers validation skipped: devicecell uses publisher.Publish directly
	// (L4, no outbox.Entry per KG-07), so event_id is not emitted at transport level.
}
