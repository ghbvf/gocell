package devicecommand

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecmd"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/dto"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	idemkey "github.com/ghbvf/gocell/runtime/http/idempotency"
)

// setupAsyncMux builds the composite Handler over a Service wired with a recorder
// CellEmitter so the async-enqueue route can be exercised end-to-end (route →
// adapter → Service.EnqueueAsync → emitter).
func setupAsyncMux(t *testing.T) (http.Handler, *outboxtest.Recorder) {
	t.Helper()
	codec, err := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	require.NoError(t, err)
	rec := outboxtest.NewRecorder()
	svc, err := devicecmd.NewService(
		clock.Real(), commandtest.NewInMemQueue(), mem.NewDeviceRepository(),
		codec, slog.Default(), query.RunModeProd,
		devicecmd.WithSliceName("devicecommand"),
		devicecmd.WithCommandEmitter(rec.CellEmitter()),
	)
	require.NoError(t, err)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/devices", func(sub cell.RouteMux) {
		require.NoError(t, NewHandler(svc).RegisterRoutes(sub))
	})
	return mux, rec
}

// TestHandleEnqueueAsync_Accepted: a POST with an Idempotency-Key (injected into
// ctx as the middleware would) returns 202 and emits exactly one command.
func TestHandleEnqueueAsync_Accepted(t *testing.T) {
	mux, rec := setupAsyncMux(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/dev-1/async-commands",
		strings.NewReader(`{"commandType":"reboot","payload":"now"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx := idemkey.WithKey(auth.TestContext("admin-user", []string{dto.RoleAdmin}), "idem-1")
	mux.ServeHTTP(w, req.WithContext(ctx))

	assert.Equal(t, http.StatusAccepted, w.Code)
	require.Len(t, rec.Entries(), 1)
}

// TestHandleEnqueueAsync_MissingKey: without an Idempotency-Key the bridge
// fail-closes → 400 and nothing is emitted.
func TestHandleEnqueueAsync_MissingKey(t *testing.T) {
	mux, rec := setupAsyncMux(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/dev-1/async-commands",
		strings.NewReader(`{"payload":"now"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("admin-user", []string{dto.RoleAdmin}))
	mux.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	require.Empty(t, rec.Entries())
}
