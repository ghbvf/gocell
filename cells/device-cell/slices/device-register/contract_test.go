package deviceregister

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/cells/device-cell/internal/mem"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/stretchr/testify/require"
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

var _ outbox.Publisher = (*recordingPublisher)(nil)

func newContractHandler() (http.Handler, *recordingPublisher) {
	repo := mem.NewDeviceRepository()
	pub := &recordingPublisher{}
	svc := NewService(repo, pub, slog.Default())
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/devices", http.HandlerFunc(NewHandler(svc).HandleRegister))
	return mux, pub
}

func TestHttpDeviceRegisterV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
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
	root := contracttest.ContractsRoot()
	httpContract := contracttest.LoadByID(t, root, "http.device.register.v1")
	c := contracttest.LoadByID(t, root, "event.device-registered.v1")
	h, pub := newContractHandler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(httpContract.HTTP.Method, httpContract.HTTP.Path, strings.NewReader(`{"name":"sensor-01"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	httpContract.ValidateHTTPResponseRecorder(t, rec)

	require.Len(t, pub.calls, 1, "Register must publish one event")
	c.ValidatePayload(t, pub.calls[0].payload)
	c.MustRejectPayload(t, []byte(`{"id":"d-1"}`))
	// Headers validation skipped: device-cell uses publisher.Publish directly
	// (L4, no outbox.Entry per KG-07), so event_id is not emitted at transport level.
}
