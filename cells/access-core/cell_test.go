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
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// noopTxRunner is a test double that executes fn directly without a real transaction.
type noopTxRunner struct{}

func (noopTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}

var _ persistence.TxRunner = noopTxRunner{}

var (
	testKeySet, testPrivKey, _ = auth.MustNewTestKeySet()
	testIssuer                 = mustIssuer(testKeySet)
	testVerifier               = mustVerifier(testKeySet)
)

func mustIssuer(ks *auth.KeySet) *auth.JWTIssuer {
	i, err := auth.NewJWTIssuer(ks, "gocell-access-core", 15*time.Minute)
	if err != nil {
		panic("test setup: " + err.Error())
	}
	return i
}

func mustVerifier(ks *auth.KeySet) *auth.JWTVerifier {
	v, err := auth.NewJWTVerifier(ks)
	if err != nil {
		panic("test setup: " + err.Error())
	}
	return v
}

func newTestCell() *AccessCore {
	return NewAccessCore(
		WithUserRepository(mem.NewUserRepository()),
		WithSessionRepository(mem.NewSessionRepository()),
		WithRoleRepository(mem.NewRoleRepository()),
		WithPublisher(eventbus.New()),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithOutboxWriter(outbox.NoopWriter{}),
		WithTxManager(noopTxRunner{}),
	)
}

func TestAccessCore_Init_RequiresJWTIssuer(t *testing.T) {
	c := NewAccessCore(
		WithUserRepository(mem.NewUserRepository()),
		WithSessionRepository(mem.NewSessionRepository()),
		WithRoleRepository(mem.NewRoleRepository()),
		WithPublisher(eventbus.New()),
		WithJWTVerifier(testVerifier), // issuer missing
		WithOutboxWriter(outbox.NoopWriter{}),
		WithTxManager(noopTxRunner{}),
	)
	err := c.Init(context.Background(), cell.Dependencies{Config: make(map[string]any)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithJWTIssuer")
}

func TestAccessCore_Init_RequiresJWTVerifier(t *testing.T) {
	c := NewAccessCore(
		WithUserRepository(mem.NewUserRepository()),
		WithSessionRepository(mem.NewSessionRepository()),
		WithRoleRepository(mem.NewRoleRepository()),
		WithPublisher(eventbus.New()),
		WithJWTIssuer(testIssuer), // verifier missing
		WithOutboxWriter(outbox.NoopWriter{}),
		WithTxManager(noopTxRunner{}),
	)
	err := c.Init(context.Background(), cell.Dependencies{Config: make(map[string]any)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithJWTVerifier")
}

func TestInit_TxRunnerXOR_OutboxWithoutTx(t *testing.T) {
	// outboxWriter present but txRunner missing → XOR mismatch → error
	c := NewAccessCore(
		WithUserRepository(mem.NewUserRepository()),
		WithSessionRepository(mem.NewSessionRepository()),
		WithRoleRepository(mem.NewRoleRepository()),
		WithPublisher(eventbus.New()),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithOutboxWriter(outbox.NoopWriter{}),
		// txRunner intentionally omitted
	)
	deps := cell.Dependencies{Config: make(map[string]any)}
	err := c.Init(context.Background(), deps)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingOutbox, ecErr.Code)
	assert.Contains(t, err.Error(), "txRunner")
}

func TestInit_TxRunnerXOR_TxWithoutOutbox(t *testing.T) {
	// txRunner present but outboxWriter missing → XOR mismatch → error
	c := NewAccessCore(
		WithUserRepository(mem.NewUserRepository()),
		WithSessionRepository(mem.NewSessionRepository()),
		WithRoleRepository(mem.NewRoleRepository()),
		WithPublisher(eventbus.New()),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithTxManager(noopTxRunner{}),
		// outboxWriter intentionally omitted
	)
	deps := cell.Dependencies{Config: make(map[string]any)}
	err := c.Init(context.Background(), deps)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingOutbox, ecErr.Code)
	assert.Contains(t, err.Error(), "txRunner")
}

func TestInit_TxRunnerXOR_BothPresent(t *testing.T) {
	// Both outboxWriter and txRunner present → should succeed
	c := newTestCell() // newTestCell includes both
	deps := cell.Dependencies{Config: make(map[string]any)}
	require.NoError(t, c.Init(context.Background(), deps))
}

func TestInit_DemoMode_RequiresPublisher(t *testing.T) {
	// L2 cell with neither outbox nor tx, but no publisher → error
	c := NewAccessCore(
		WithInMemoryDefaults(),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		// no publisher, no outbox, no tx
	)
	err := c.Init(context.Background(), cell.Dependencies{Config: make(map[string]any)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publisher")
}

func TestInit_DemoMode_WithPublisher_Succeeds(t *testing.T) {
	// L2 cell, both nil, but publisher present → OK (demo mode with warning)
	c := NewAccessCore(
		WithInMemoryDefaults(),
		WithPublisher(eventbus.New()),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
	)
	err := c.Init(context.Background(), cell.Dependencies{Config: make(map[string]any)})
	require.NoError(t, err)
}

func TestAccessCore_Lifecycle(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Config: make(map[string]any),
	}

	// Init
	require.NoError(t, c.Init(ctx, deps))
	assert.Equal(t, 8, len(c.OwnedSlices()), "should have 8 slices")

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

func TestAccessCore_Startup(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
		Config: make(map[string]any),
	}
	require.NoError(t, c.Init(ctx, deps))
	require.NoError(t, c.Start(ctx))
	assert.True(t, c.Ready())
	require.NoError(t, c.Stop(ctx))
}

func TestAccessCore_TokenVerifierAndAuthorizer(t *testing.T) {
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
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
func (m *stubMux) Mount(_ string, _ http.Handler)                   { m.handleCount++ }
func (m *stubMux) Group(_ func(cell.RouteMux))                      { m.handleCount++ }
func (m *stubMux) With(_ ...func(http.Handler) http.Handler) cell.RouteMux { return m }

// initCellWithRouter creates an initialized AccessCore with routes registered
// on a real chi-based router, ready for HTTP testing.
func initCellWithRouter(t *testing.T) *router.Router {
	t.Helper()
	c := newTestCell()
	ctx := context.Background()
	deps := cell.Dependencies{
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

func TestAccessCore_RouteSessionLogout(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/access/sessions/sess-nonexistent", nil)
	r.ServeHTTP(rec, req)

	// 404 means handler was reached and session not found (correct routing).
	// 405 or chi-level 404 (without JSON body) means routing is broken.
	assert.Equal(t, http.StatusNotFound, rec.Code,
		"DELETE /api/v1/access/sessions/{id} should reach handler (got %d)", rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"),
		"response should be JSON (handler reached, not chi 404)")
}

func TestAccessCore_RouteUserGet(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/usr-nonexistent", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code,
		"GET /api/v1/access/users/{id} should reach handler (got %d)", rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"),
		"response should be JSON (handler reached, not chi 404)")
}

func TestAccessCore_RouteRolesList(t *testing.T) {
	r := initCellWithRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/roles/user-1", nil)
	r.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusNotFound, rec.Code,
		"GET /api/v1/access/roles/{userID} should not return 404 (got %d)", rec.Code)
}
