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
	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	commandruntime "github.com/ghbvf/gocell/runtime/command"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/ghbvf/gocell/runtime/outbox/outboxtest"
)

func newTestCell() *DeviceCell {
	c := NewDeviceCell(
		clock.Real(),
		WithDeviceRepository(mem.NewDeviceRepository()),
		WithDirectPublisher(outbox.WrapPublisherForCell(eventbus.New(clock.Real()))),
		WithBootstrapEmitter(testBootstrapEmitter()),
		WithCommandRegistry(commandruntime.NewRegistry()),
	)
	c.RegisterCommandQueue(commandtest.NewInMemQueue())
	return c
}

// testBootstrapEmitter builds a writer-backed sealed CellEmitter for the
// devicebootstrap slice (#1698), mirroring the composition root: a WriterEmitter
// over an in-memory FakeStore (which satisfies kout.Writer).
func testBootstrapEmitter() outbox.CellEmitter {
	we, err := outbox.NewWriterEmitter(outboxtest.NewFakeStore())
	if err != nil {
		panic(err)
	}
	return outbox.WrapEmitterForCell(we)
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
	return cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
}

func TestDeviceCell_Lifecycle(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	rec := newTestRec()

	// Init
	require.NoError(t, c.Init(ctx, rec))
	assert.Len(t, c.OwnedSlices(), 8,
		"8 slices: 7 prior + devicecertrenewal reconcile producer (#1757)")

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
	assert.Equal(t, cellvocab.CellTypeEdge, c.Type())
	assert.Equal(t, cellvocab.L4, c.ConsistencyLevel())
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

func TestDeviceCell_InitNoDeviceRepository_FailsFast(t *testing.T) {
	// "No soft fallback": devicecell never silently constructs a mem repo. Demo
	// callers must wire mem.NewDeviceRepository() explicitly via the option.
	c := NewDeviceCell(
		clock.Real(),
		WithDirectPublisher(outbox.WrapPublisherForCell(eventbus.New(clock.Real()))),
	)
	c.RegisterCommandQueue(commandtest.NewInMemQueue())
	err := c.Init(context.Background(), newTestRec())
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
	assert.Contains(t, err.Error(), "device repository")
}

func TestDeviceCell_InitNoCommandQueue_FailsFast(t *testing.T) {
	// Companion of the device-repo case: nil commandQueue is also rejected in
	// every mode. RegisterCommandQueue is the only sanctioned wiring point.
	c := NewDeviceCell(
		clock.Real(),
		WithDeviceRepository(mem.NewDeviceRepository()),
		WithDirectPublisher(outbox.WrapPublisherForCell(eventbus.New(clock.Real()))),
		WithBootstrapEmitter(testBootstrapEmitter()),
	)
	err := c.Init(context.Background(), newTestRec())
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
	assert.Contains(t, err.Error(), "command queue")
}

func TestDeviceCell_InitNoCommandRegistry_FailsFast(t *testing.T) {
	// Symmetric with the commandQueue case: the sync command-bus registry is a
	// required cell dep (#1580). Omitting WithCommandRegistry must fail fast in
	// Init rather than silently leaving the generated funnel unregistered
	// (dead-but-compiles). Full deps minus the registry so initSlices reaches the
	// registry guard (which sits after the commandQueue guard).
	c := NewDeviceCell(
		clock.Real(),
		WithDeviceRepository(mem.NewDeviceRepository()),
		WithDirectPublisher(outbox.WrapPublisherForCell(eventbus.New(clock.Real()))),
		WithBootstrapEmitter(testBootstrapEmitter()),
	)
	c.RegisterCommandQueue(commandtest.NewInMemQueue())
	err := c.Init(context.Background(), newTestRec())
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
	assert.Contains(t, err.Error(), "command registry")
}

func TestDeviceCell_InitNoPublisher(t *testing.T) {
	// No publisher injected; Init should fail-fast (NIL-PUB-P1).
	c := NewDeviceCell(
		clock.Real(),
		WithDeviceRepository(mem.NewDeviceRepository()),
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

	r := mustNewRouter(t)
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
		clock.Real(),
		WithDeviceRepository(mem.NewDeviceRepository()),
		WithDirectPublisher(outbox.WrapPublisherForCell(eventbus.New(clock.Real()))),

		// No WithCursorCodec — durable mode must refuse the demo fallback.
	)
	err := c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, outbox.DurabilityDurable))
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingCodec, ecErr.Code)
	assert.Contains(t, err.Error(), "cursor codec")
}

func TestDeviceCell_DurableMode_RegisterPublishFailureReturnsCreated(t *testing.T) {
	// The publish-fail-open behavior under test applies in both modes; we use
	// demo mode here purely to dodge the cursor-codec durability check, since
	// the focus is on persistence vs publish separation.
	c := NewDeviceCell(
		clock.Real(),
		WithDeviceRepository(mem.NewDeviceRepository()),
		WithDirectPublisher(outbox.WrapPublisherForCell(failingPublisher{})),
		WithBootstrapEmitter(testBootstrapEmitter()),

		WithCursorCodec(newTestCursorCodec(t)),
		WithCommandRegistry(commandruntime.NewRegistry()),
	)
	c.RegisterCommandQueue(commandtest.NewInMemQueue())
	require.NoError(t, c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, outbox.DurabilityDemo)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/", strings.NewReader(`{"name":"sensor-fail"}`))
	req.Header.Set("Content-Type", "application/json")
	c.registerHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code,
		"device creation must stay successful after persistence even if direct publish fails")
}

func TestDeviceCell_DemoMode_RegisterPublishFailureReturnsCreated(t *testing.T) {
	c := NewDeviceCell(
		clock.Real(),
		WithDeviceRepository(mem.NewDeviceRepository()),
		WithDirectPublisher(outbox.WrapPublisherForCell(failingPublisher{})),
		WithBootstrapEmitter(testBootstrapEmitter()),
		WithCommandRegistry(commandruntime.NewRegistry()),
	)
	c.RegisterCommandQueue(commandtest.NewInMemQueue())
	require.NoError(t, c.Init(context.Background(), cell.NewRegistryRecorder(map[string]any{}, outbox.DurabilityDemo)))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/", strings.NewReader(`{"name":"sensor-demo"}`))
	req.Header.Set("Content-Type", "application/json")
	c.registerHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code,
		"demo mode keeps direct publish fail-open behavior")
}

// TestDeviceCell_Probes_WithDirectEmitter verifies that after Init
// with a DirectEmitter-backed publisher, the outbox_failopen_rate probe
// scoped to "devicecell" is registered.
func TestDeviceCell_Probes_WithDirectEmitter(t *testing.T) {
	c := newTestCell()
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), rec))
	agg := drainProbeSnapshot(t, rec)

	const emitterKey = "outbox_failopen_rate_devicecell"
	require.True(t, agg.HasProbe(emitterKey), "DirectEmitter probe must be registered")
	assert.NoError(t, agg.Probe(emitterKey).Check(context.Background()), "fresh emitter should be healthy")
}

// TestDeviceCell_LifecycleHookRegistered verifies that Init registers both
// reconcile-loop lifecycle hooks via reg.Lifecycle: the command sweeper and the
// cert-renewal loop (#1757).
func TestDeviceCell_LifecycleHookRegistered(t *testing.T) {
	c := newTestCell()
	rec := cell.NewRegistryRecorder(make(map[string]any), outbox.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), rec))
	snap := rec.Snapshot()

	require.Len(t, snap.LifecycleHooks, 2,
		"Init must register two lifecycle hooks (command sweeper + cert renewal)")
	names := map[string]cell.LifecycleHook{}
	for _, h := range snap.LifecycleHooks {
		names[h.Name] = h
		assert.NotNil(t, h.OnStart, "hook %q must have OnStart", h.Name)
		assert.NotNil(t, h.OnStop, "hook %q must have OnStop", h.Name)
	}
	assert.Contains(t, names, "devicecommand.sweeper")
	assert.Contains(t, names, "devicecert.renewal")
}

// TestDeviceCell_CommandSweeper_MetricsBranches verifies that Init (and
// therefore Loop construction) succeeds both when no metrics provider is set
// (the default NopProvider path) and when WithMetricsProvider is explicitly
// supplied with a non-nil provider.
func TestDeviceCell_CommandSweeper_MetricsBranches(t *testing.T) {
	t.Parallel()

	t.Run("without metrics provider (default NopProvider)", func(t *testing.T) {
		t.Parallel()
		c := newTestCell() // no WithMetricsProvider — uses NopProvider{} internally
		rec := newTestRec()
		require.NoError(t, c.Init(context.Background(), rec))
	})

	t.Run("with explicit NopProvider", func(t *testing.T) {
		t.Parallel()
		c := NewDeviceCell(
			clock.Real(),
			WithDeviceRepository(mem.NewDeviceRepository()),
			WithDirectPublisher(outbox.WrapPublisherForCell(eventbus.New(clock.Real()))),
			WithBootstrapEmitter(testBootstrapEmitter()),
			WithCommandRegistry(commandruntime.NewRegistry()),
			WithMetricsProvider(metrics.NopProvider{}),
		)
		c.RegisterCommandQueue(commandtest.NewInMemQueue())
		rec := newTestRec()
		require.NoError(t, c.Init(context.Background(), rec))
	})
}

func mustNewRouter(t *testing.T) *router.Router {
	t.Helper()
	r, err := router.New(clock.Real())
	if err != nil {
		t.Fatalf("router.New: %v", err)
	}
	return r
}
