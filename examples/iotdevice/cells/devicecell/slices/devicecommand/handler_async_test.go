package devicecommand

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecmd"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/dto"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	idemkey "github.com/ghbvf/gocell/runtime/http/idempotency"
)

// newSeededAsyncHandler builds the composite Handler over a devicecmd.Service
// wired with the given command emitter, with deviceID pre-seeded so the
// async-enqueue device-existence check passes. Shared by the async handler tests,
// the contract test, and the cross-cell e2e (each supplies its own emitter:
// a recorder for assertion, or a writer-emitter over a relay-pollable store).
func newSeededAsyncHandler(t *testing.T, emitter outbox.CellEmitter, deviceID string) http.Handler {
	t.Helper()
	devRepo := mem.NewDeviceRepository()
	require.NoError(t, devRepo.Create(context.Background(),
		&domain.Device{ID: deviceID, Name: "sensor-a", Status: "online"}))
	codec, err := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	require.NoError(t, err)
	svc, err := devicecmd.NewService(
		clock.Real(), commandtest.NewInMemQueue(), devRepo,
		codec, slog.Default(), query.RunModeProd,
		devicecmd.WithSliceName("devicecommand"),
		devicecmd.WithCommandEmitter(emitter),
	)
	require.NoError(t, err)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/devices", func(sub cell.RouteMux) {
		require.NoError(t, NewHandler(svc).RegisterRoutes(sub))
	})
	return mux
}

// setupAsyncMux builds an async handler over a recorder emitter (device "dev-1"
// seeded), returning the recorder so tests can assert the emitted command.
func setupAsyncMux(t *testing.T) (http.Handler, *outboxtest.Recorder) {
	t.Helper()
	rec := outboxtest.NewRecorder()
	return newSeededAsyncHandler(t, rec.CellEmitter(), "dev-1"), rec
}

// reqIDCtx injects a sealed RequestIdentity into ctx (caller + fixed fingerprint +
// key), as the HTTP idempotency middleware would mint — for handler/contract tests
// that drive the mux without the middleware in front.
func reqIDCtx(t *testing.T, base context.Context, caller, key string) context.Context {
	t.Helper()
	id, err := idemkey.NewRequestIdentity(caller, "fp-test", key)
	require.NoError(t, err)
	return idemkey.WithRequestIdentity(base, id)
}

// TestHandleEnqueueAsync_Accepted: a POST with an Idempotency-Key (injected into
// ctx as the middleware would) returns 202 and emits exactly one command.
func TestHandleEnqueueAsync_Accepted(t *testing.T) {
	mux, rec := setupAsyncMux(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/dev-1/async-commands",
		strings.NewReader(`{"commandType":"reboot","payload":"now"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx := reqIDCtx(t, auth.TestContext("admin-user", []string{dto.RoleAdmin}), "admin-user", "idem-1")
	mux.ServeHTTP(w, req.WithContext(ctx))

	assert.Equal(t, http.StatusAccepted, w.Code)
	require.Len(t, rec.Entries(), 1)
}

// TestHandleEnqueueAsync_MissingKey: with a valid device but no Idempotency-Key the
// bridge fail-closes → 400 and nothing is emitted.
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
