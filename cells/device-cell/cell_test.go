package devicecell

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/cells/device-cell/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestCell() *DeviceCell {
	return NewDeviceCell(
		WithDeviceRepository(mem.NewDeviceRepository()),
		WithCommandRepository(mem.NewCommandRepository()),
		WithPublisher(eventbus.New()),
	)
}

func TestDeviceCell_Lifecycle(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}

	// Init
	require.NoError(t, c.Init(ctx, deps))
	assert.Len(t, c.OwnedSlices(), 3, "should have 3 slices")

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
	assert.Equal(t, "device-cell", c.ID())
	assert.Equal(t, cell.CellTypeEdge, c.Type())
	assert.Equal(t, cell.L4, c.ConsistencyLevel())
}

func TestDeviceCell_Startup(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}
	require.NoError(t, c.Init(ctx, deps))
	require.NoError(t, c.Start(ctx))
	assert.True(t, c.Ready())
	require.NoError(t, c.Stop(ctx))
}

func TestDeviceCell_InitDefaultsRepositories(t *testing.T) {
	// No repos injected; Init should use in-memory defaults.
	c := NewDeviceCell(WithPublisher(eventbus.New()))
	ctx := context.Background()
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}
	require.NoError(t, c.Init(ctx, deps))
	assert.Len(t, c.OwnedSlices(), 3)
}

func TestDeviceCell_InitNoPublisher(t *testing.T) {
	// No publisher injected; Init should fail-fast (NIL-PUB-P1).
	c := NewDeviceCell(
		WithDeviceRepository(mem.NewDeviceRepository()),
		WithCommandRepository(mem.NewCommandRepository()),
	)
	ctx := context.Background()
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}
	err := c.Init(ctx, deps)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publisher")
	assert.Contains(t, err.Error(), "DiscardPublisher")
}

func TestDeviceCell_RegisterRoutes(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}
	require.NoError(t, c.Init(ctx, deps))

	mux := &stubMux{}
	c.RegisterRoutes(mux)
	assert.GreaterOrEqual(t, mux.handleCount, 3, "should register at least 3 route patterns")
}

// stubMux implements cell.RouteMux for counting route registrations.
type stubMux struct {
	handleCount int
}

func (m *stubMux) Handle(_ string, _ http.Handler) { m.handleCount++ }
func (m *stubMux) Route(_ string, fn func(cell.RouteMux)) {
	m.handleCount++
	fn(m)
}
func (m *stubMux) Mount(_ string, _ http.Handler)                   { m.handleCount++ }
func (m *stubMux) Group(_ func(cell.RouteMux))                      { m.handleCount++ }
func (m *stubMux) With(_ ...func(http.Handler) http.Handler) cell.RouteMux { return m }

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
// on a real chi-based router, ready for HTTP testing.
func initCellWithRouter(t *testing.T) *router.Router {
	t.Helper()
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}
	require.NoError(t, c.Init(ctx, deps))

	r := router.New()
	c.RegisterRoutes(r)
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

	// Now get status.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/devices/"+deviceID+"/status", nil)
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
	req = req.WithContext(auth.TestContext("operator-1", []string{"operator"}))
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestDeviceCell_RouteListPendingCommands(t *testing.T) {
	r := initCellWithRouter(t)

	// Register device.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/", strings.NewReader(`{"name":"sensor-d"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	data := extractData(t, rec.Body.Bytes())
	deviceID := data["id"].(string)

	// List pending (should be empty). Inject auth context: device authenticates as itself.
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
	req = req.WithContext(auth.TestContext("operator-1", []string{"operator"}))
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	cmdData := extractData(t, rec.Body.Bytes())
	cmdID := cmdData["id"].(string)

	// Ack. Inject auth context: device authenticates as itself.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/devices/"+deviceID+"/commands/"+cmdID+"/ack", nil)
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
		WithCommandRepository(mem.NewCommandRepository()),
		WithPublisher(eventbus.New()),
		// No WithCursorCodec — durable mode must refuse the demo fallback.
	)
	err := c.Init(context.Background(), cell.Dependencies{
		Config:         map[string]any{},
		DurabilityMode: cell.DurabilityDurable,
	})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingCodec, ecErr.Code)
	assert.Contains(t, err.Error(), "cursor codec")
}
