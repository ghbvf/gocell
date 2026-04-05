package accesscore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	testPrivKey, testPubKey = auth.MustGenerateTestKeyPair()
	testIssuer              = auth.NewJWTIssuer(testPrivKey, "gocell-access-core", 15*time.Minute)
	testVerifier            = auth.NewJWTVerifier(testPubKey)
)

// noopWriter is a no-op outbox.Writer for testing.
type noopWriter struct{}

func (noopWriter) Write(_ context.Context, _ outbox.Entry) error { return nil }

func newTestCell() *AccessCore {
	return NewAccessCore(
		WithUserRepository(mem.NewUserRepository()),
		WithSessionRepository(mem.NewSessionRepository()),
		WithRoleRepository(mem.NewRoleRepository()),
		WithPublisher(eventbus.New()),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithOutboxWriter(noopWriter{}),
	)
}

func TestAccessCore_Lifecycle(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Cells:     make(map[string]cell.Cell),
		Contracts: make(map[string]cell.Contract),
		Config:    make(map[string]any),
	}

	// Init
	require.NoError(t, c.Init(ctx, deps))
	assert.Equal(t, 7, len(c.OwnedSlices()), "should have 7 slices")

	// Start
	require.NoError(t, c.Start(ctx))
	assert.Equal(t, "healthy", c.Health().Status)
	assert.True(t, c.Ready())

	// Stop
	require.NoError(t, c.Stop(ctx))
	assert.Equal(t, "unhealthy", c.Health().Status)
	assert.False(t, c.Ready())
}

func TestAccessCore_Metadata(t *testing.T) {
	c := newTestCell()
	assert.Equal(t, "access-core", c.ID())
	assert.Equal(t, cell.CellTypeCore, c.Type())
	assert.Equal(t, cell.L2, c.ConsistencyLevel())
}

func TestAccessCore_TokenVerifierAndAuthorizer(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Cells: make(map[string]cell.Cell), Contracts: make(map[string]cell.Contract),
		Config: make(map[string]any),
	}
	require.NoError(t, c.Init(ctx, deps))

	assert.NotNil(t, c.TokenVerifier())
	assert.NotNil(t, c.Authorizer())
}

func TestAccessCore_RegisterRoutes(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Cells: make(map[string]cell.Cell), Contracts: make(map[string]cell.Contract),
		Config: make(map[string]any),
	}
	require.NoError(t, c.Init(ctx, deps))

	mux := &stubMux{}
	c.RegisterRoutes(mux)
	assert.GreaterOrEqual(t, mux.handleCount, 3, "should register at least 3 route patterns")
}

// stubMux implements cell.RouteMux for testing.
type stubMux struct {
	handleCount int
}

func (m *stubMux) Handle(_ string, _ http.Handler) { m.handleCount++ }
func (m *stubMux) Route(_ string, fn func(cell.RouteMux)) {
	m.handleCount++
	fn(m)
}
func (m *stubMux) Mount(_ string, _ http.Handler)  { m.handleCount++ }
func (m *stubMux) Group(_ func(cell.RouteMux))     { m.handleCount++ }

// initCellWithRouter creates an initialized AccessCore with routes registered
// on a real chi-based router, ready for HTTP testing.
func initCellWithRouter(t *testing.T) *router.Router {
	t.Helper()
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Cells: make(map[string]cell.Cell), Contracts: make(map[string]cell.Contract),
		Config: make(map[string]any),
	}
	require.NoError(t, c.Init(ctx, deps))

	r := router.New()
	c.RegisterRoutes(r)
	return r
}

func TestAccessCore_RouteSessionLogin(t *testing.T) {
	r := initCellWithRouter(t)

	body := `{"username":"alice","password":"secret"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/sessions/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	// We expect a non-404 status. The exact status depends on business logic
	// (e.g. 401 for bad credentials), but 404 means routing is broken.
	assert.NotEqual(t, http.StatusNotFound, rec.Code,
		"POST /api/v1/access/sessions/login should not return 404 (got %d)", rec.Code)
}

func TestAccessCore_RouteSessionRefresh(t *testing.T) {
	r := initCellWithRouter(t)

	body := `{"refreshToken":"tok"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/sessions/refresh", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusNotFound, rec.Code,
		"POST /api/v1/access/sessions/refresh should not return 404 (got %d)", rec.Code)
}

func TestAccessCore_RouteUserCreate(t *testing.T) {
	r := initCellWithRouter(t)

	body := `{"username":"bob","email":"bob@example.com","password":"secret123"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/users/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusNotFound, rec.Code,
		"POST /api/v1/access/users/ should not return 404 (got %d)", rec.Code)
}

func TestAccessCore_RouteRolesList(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/roles/user-1", nil)
	r.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusNotFound, rec.Code,
		"GET /api/v1/access/roles/{userID} should not return 404 (got %d)", rec.Code)
}
