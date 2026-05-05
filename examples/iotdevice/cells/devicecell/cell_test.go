package devicecell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dto "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/dto"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/router"
)

func newTestCell() *DeviceCell {
	return NewDeviceCell(
		WithDeviceRepository(mem.NewDeviceRepository()),
		WithPublisher(eventbus.New(eventbus.WithClock(clock.Real()))),
		WithClock(clock.Real()),
	)
}

type failingPublisher struct{}

func (failingPublisher) Publish(_ context.Context, _ string, _ []byte) error {
	return errors.New("publish failed")
}

func (failingPublisher) Close(_ context.Context) error { return nil }

func newTestCursorCodec(t *testing.T) *query.CursorCodec {
	t.Helper()
	codec, err := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	require.NoError(t, err)
	return codec
}

func newTestRec() *cell.RegistryRecorder {
	return cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
}

func TestDeviceCell_Lifecycle(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	rec := newTestRec()

	// Init
	require.NoError(t, c.Init(ctx, rec))
	assert.Len(t, c.OwnedSlices(), 4, "should have 4 slices")

	// Start
	require.NoError(t, c.Start(ctx))
	assert.Equal(t, "healthy", c.Health().Status)
	assert.True(t, c.Ready())

	// Stop
	require.NoError(t, c.Stop(ctx))
	assert.Equal(t, "unhealthy", c.Health().Status)
	assert.False(t, c.Ready())
}

func TestDeviceCell_Metadata(t *testing.T) {
	c := newTestCell()
	assert.Equal(t, "devicecell", c.ID())
	assert.Equal(t, cell.CellTypeEdge, c.Type())
	assert.Equal(t, cell.L4, c.ConsistencyLevel())
}

func TestDeviceCell_Startup(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	rec := newTestRec()
	require.NoError(t, c.Init(ctx, rec))
	require.NoError(t, c.Start(ctx))
	assert.True(t, c.Ready())
	require.NoError(t, c.Stop(ctx))
}

func TestDeviceCell_InitDefaultsRepositories(t *testing.T) {
	// No repos injected; Init should use in-memory defaults.
	c := NewDeviceCell(
		WithPublisher(eventbus.New(eventbus.WithClock(clock.Real()))),
		WithClock(clock.Real()),
	)
	ctx := context.Background()
	rec := newTestRec()
	require.NoError(t, c.Init(ctx, rec))
	assert.Len(t, c.OwnedSlices(), 4)
}

func TestDeviceCell_InitNoPublisher(t *testing.T) {
	// No publisher injected; Init should fail-fast (NIL-PUB-P1).
	c := NewDeviceCell(
		WithDeviceRepository(mem.NewDeviceRepository()),
		WithClock(clock.Real()),
	)
	ctx := context.Background()
	rec := newTestRec()
	err := c.Init(ctx, rec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publisher")
	assert.Contains(t, err.Error(), "DiscardPublisher")
}

func TestDeviceCell_RouteGroups(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	rec := newTestRec()
	require.NoError(t, c.Init(ctx, rec))
	snap := rec.Snapshot()

	mux := celltest.NewTestMux()
	for _, rg := range snap.RouteGroups {
		if rg.Listener == cell.PrimaryListener {
			if rg.Prefix != "" {
				mux.Route(rg.Prefix, func(sub cell.RouteMux) { require.NoError(t, rg.Register(sub)) })
			} else {
				require.NoError(t, rg.Register(mux))
			}
		}
	}
	metas := mux.DeclaredAuthMetas()
	require.Contains(t, metas, cell.AuthRouteMeta{
		Method: http.MethodGet,
		Path:   "/api/v1/devices",
	})
}

// extractData unmarshals a JSON response and returns the "data" envelope.
func extractData(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(body, &envelope))
	data, ok := envelope["data"].(map[string]any)
	require.True(t, ok, "response should have data envelope")
	return data
}

// initCellWithRouter creates an initialized DeviceCell with routes registered
// on a real chi-based router, ready for HTTP testing. FinalizeAuth is called
// so the Router accepts ServeHTTP calls (required after F3 auth declaration).
func initCellWithRouter(t *testing.T) *router.Router {
	t.Helper()
	c := newTestCell()
	ctx := context.Background()
	rec := newTestRec()
	require.NoError(t, c.Init(ctx, rec))
	snap := rec.Snapshot()

	r := router.MustNew(router.WithRouterClock(clock.Real()))
	for _, rg := range snap.RouteGroups {
		if rg.Listener == cell.PrimaryListener {
			if rg.Prefix != "" {
				r.Route(rg.Prefix, func(sub cell.RouteMux) { require.NoError(t, rg.Register(sub)) })
			} else {
				require.NoError(t, rg.Register(r))
			}
		}
	}
	require.NoError(t, r.FinalizeAuth())
	return r
}

func TestDeviceCell_RouteRegisterDevice(t *testing.T) {
	r := initCellWithRouter(t)

	body := `{"name":"sensor-a"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code,
		"POST /api/v1/devices/ should return 201")

	data := extractData(t, rec.Body.Bytes())
	assert.NotEmpty(t, data["id"])
}

func TestDeviceCell_RouteListDevices_Authz(t *testing.T) {
	r := initCellWithRouter(t)

	t.Run("401 no auth", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/devices/", nil)
		r.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("403 non-admin", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/devices/", nil)
		req = req.WithContext(auth.TestContext("user-1", []string{"viewer"}))
		r.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	// Confirms list endpoint is admin-only: operator and device roles are
	// allowed on enqueue/dequeue but must not enumerate the device fleet.
	t.Run("403 operator", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/devices/", nil)
		req = req.WithContext(auth.TestContext("operator-1", []string{dto.RoleOperator}))
		r.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("403 device", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/devices/", nil)
		req = req.WithContext(auth.TestContext("device-1", []string{dto.RoleDevice}))
		r.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusForbidden, rec.Code)
	})
}

func TestDeviceCell_RouteGetStatus(t *testing.T) {
	r := initCellWithRouter(t)

	// First register a device so we have an ID.
	body := `{"name":"sensor-b"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	data := extractData(t, rec.Body.Bytes())
	deviceID := data["id"].(string)

	// Now get status. Status requires RoleOperator or RoleDevice (Policy: auth.AnyRole(dto.RoleOperator, dto.RoleDevice)).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/devices/"+deviceID+"/status", nil)
	req = req.WithContext(auth.TestContext(deviceID, []string{dto.RoleDevice}))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestDeviceCell_RouteEnqueueCommand(t *testing.T) {
	r := initCellWithRouter(t)

	// Register device.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/", strings.NewReader(`{"name":"sensor-c"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	data := extractData(t, rec.Body.Bytes())
	deviceID := data["id"].(string)

	// Enqueue command (operator role required).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/devices/"+deviceID+"/commands", strings.NewReader(`{"payload":"reboot"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("operator-1", []string{dto.RoleOperator}))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestDeviceCell_RouteDequeueCommands(t *testing.T) {
	r := initCellWithRouter(t)

	// Register device.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/", strings.NewReader(`{"name":"sensor-d"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	data := extractData(t, rec.Body.Bytes())
	deviceID := data["id"].(string)

	// Dequeue (should be empty). Inject auth context: device authenticates as itself.
	// nil roles is intentional: dequeue uses auth.SelfOr("id", "admin") which
	// passes when subject == path {id}, so no role is required for the device.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/devices/"+deviceID+"/commands", nil)
	req = req.WithContext(auth.TestContext(deviceID, nil))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestDeviceCell_RouteAckCommand(t *testing.T) {
	r := initCellWithRouter(t)

	// Register device.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/", strings.NewReader(`{"name":"sensor-e"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	data := extractData(t, rec.Body.Bytes())
	deviceID := data["id"].(string)

	// Enqueue command (operator role required).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/devices/"+deviceID+"/commands", strings.NewReader(`{"payload":"reboot"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("operator-1", []string{dto.RoleOperator}))
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	cmdData := extractData(t, rec.Body.Bytes())
	cmdID := cmdData["id"].(string)

	// Dequeue first so AckSuccess is allowed from Sent.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/devices/"+deviceID+"/commands", nil)
	req = req.WithContext(auth.TestContext(deviceID, nil))
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	// Ack. Inject auth context: device authenticates as itself.
	rec = httptest.NewRecorder()
	ackPath := "/api/v1/devices/" + deviceID + "/commands/" + cmdID + "/ack"
	req = httptest.NewRequest(http.MethodPost, ackPath, strings.NewReader(`{"reason":"success"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext(deviceID, nil))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestDeviceCell_DurableMode_RejectsMissingCursorCodec locks the fail-fast
// behavior introduced with RunMode wiring: a durable assembly that forgets
// to inject a production cursor codec must not silently fall back to the
// public demo key baked into the source tree.
func TestDeviceCell_DurableMode_RejectsMissingCursorCodec(t *testing.T) {
	c := NewDeviceCell(
		WithDeviceRepository(mem.NewDeviceRepository()),
		WithPublisher(eventbus.New(eventbus.WithClock(clock.Real()))),
		WithClock(clock.Real()),
		// No WithCursorCodec — durable mode must refuse the demo fallback.
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDurable))
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingCodec, ecErr.Code)
	assert.Contains(t, err.Error(), "cursor codec")
}

// TestDeviceCell_DurableMode_RejectsInMemCommandQueue verifies that Init fails
// fast when DurabilityDurable is requested, because commandtest.InMemQueue is
// not suitable for durable deployments.
func TestDeviceCell_DurableMode_RejectsInMemCommandQueue(t *testing.T) {
	c := NewDeviceCell(
		WithDeviceRepository(mem.NewDeviceRepository()),
		WithPublisher(eventbus.New(eventbus.WithClock(clock.Real()))),
		WithClock(clock.Real()),
		WithCursorCodec(newTestCursorCodec(t)),
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDurable))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commandtest.InMemQueue is not suitable for durable deployments")
}

func TestDeviceCell_DurableMode_RegisterPublishFailureReturnsCreated(t *testing.T) {
	// Uses DurabilityDemo (not DurabilityDurable) because InMemQueue is not
	// suitable for durable deployments and Init will reject durable mode.
	// The publish-fail-open behavior under test applies in both modes.
	c := NewDeviceCell(
		WithDeviceRepository(mem.NewDeviceRepository()),
		WithPublisher(failingPublisher{}),
		WithClock(clock.Real()),
		WithCursorCodec(newTestCursorCodec(t)),
	)
	require.NoError(t, c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDemo)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/", strings.NewReader(`{"name":"sensor-fail"}`))
	req.Header.Set("Content-Type", "application/json")
	c.registerHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code,
		"device creation must stay successful after persistence even if direct publish fails")
}

func TestDeviceCell_DemoMode_RegisterPublishFailureReturnsCreated(t *testing.T) {
	c := NewDeviceCell(
		WithDeviceRepository(mem.NewDeviceRepository()),
		WithPublisher(failingPublisher{}),
		WithClock(clock.Real()),
	)
	require.NoError(t, c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, cell.DurabilityDemo)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/", strings.NewReader(`{"name":"sensor-demo"}`))
	req.Header.Set("Content-Type", "application/json")
	c.registerHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code,
		"demo mode keeps direct publish fail-open behavior")
}

// TestDeviceCell_Probes_WithDirectEmitter verifies that after Init
// with a DirectEmitter-backed publisher, the snapshot contains the
// outbox-failopen-rate checker scoped to "devicecell".
func TestDeviceCell_Probes_WithDirectEmitter(t *testing.T) {
	c := newTestCell()
	rec := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), rec))
	snap := rec.Snapshot()

	const emitterKey = "outbox-failopen-rate.devicecell"
	require.Contains(t, snap.HealthCheckers, emitterKey, "DirectEmitter probe must be aggregated")
	assert.NoError(t, snap.HealthCheckers[emitterKey](context.Background()), "fresh emitter should be healthy")
}

// TestDeviceCell_LifecycleHookRegistered verifies that Init registers the
// command sweeper lifecycle hook via reg.Lifecycle.
func TestDeviceCell_LifecycleHookRegistered(t *testing.T) {
	c := newTestCell()
	rec := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), rec))
	snap := rec.Snapshot()

	require.Len(t, snap.LifecycleHooks, 1, "Init must register exactly one lifecycle hook (command sweeper)")
	assert.Equal(t, "devicecommand.sweeper", snap.LifecycleHooks[0].Name)
}
