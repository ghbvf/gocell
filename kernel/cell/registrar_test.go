package cell

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Mock implementations for optional registrar interfaces
// ---------------------------------------------------------------------------

// mockRouteMux implements RouteMux for testing.
type mockRouteMux struct {
	prefix string
	routes []string
	groups int
}

func (m *mockRouteMux) Handle(pattern string, _ http.Handler) {
	m.routes = append(m.routes, m.prefix+pattern)
}

func (m *mockRouteMux) Route(pattern string, fn func(RouteMux)) {
	sub := &mockRouteMux{prefix: m.prefix + pattern}
	fn(sub)
	m.routes = append(m.routes, sub.routes...)
}

func (m *mockRouteMux) Mount(pattern string, _ http.Handler) {
	m.routes = append(m.routes, m.prefix+pattern+"/*")
}

func (m *mockRouteMux) Group(fn func(RouteMux)) {
	m.groups++
	fn(m)
}

func (m *mockRouteMux) With(_ ...func(http.Handler) http.Handler) RouteMux { return m }

// Compile-time check: mockRouteMux satisfies RouteMux.
var _ RouteMux = (*mockRouteMux)(nil)

// httpCell is a Cell that also implements HTTPRegistrar.
type httpCell struct {
	BaseCell
	registered bool
}

func (h *httpCell) RegisterRoutes(mux RouteMux) {
	h.registered = true
	mux.Handle("/api/v1/sessions", http.NotFoundHandler())
}

// Compile-time check.
var _ HTTPRegistrar = (*httpCell)(nil)

// mockEventRouter implements EventRouter for testing.
type mockEventRouter struct {
	topics []string
}

func (m *mockEventRouter) AddHandler(topic string, _ outbox.EntryHandler, _ string) {
	m.topics = append(m.topics, topic)
}

// Compile-time check.
var _ EventRouter = (*mockEventRouter)(nil)

// eventCell is a Cell that also implements EventRegistrar.
type eventCell struct {
	BaseCell
	registered bool
}

func (e *eventCell) RegisterSubscriptions(r EventRouter) error {
	e.registered = true
	r.AddHandler("session.created", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}, "test")
	return nil
}

// Compile-time check.
var _ EventRegistrar = (*eventCell)(nil)

// dualCell implements both HTTPRegistrar and EventRegistrar.
type dualCell struct {
	BaseCell
	httpRegistered  bool
	eventRegistered bool
}

func (d *dualCell) RegisterRoutes(mux RouteMux) {
	d.httpRegistered = true
	mux.Handle("/api/v1/devices", http.NotFoundHandler())
}

func (d *dualCell) RegisterSubscriptions(r EventRouter) error {
	d.eventRegistered = true
	r.AddHandler("device.enrolled", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}, "test")
	return nil
}

// Compile-time checks.
var (
	_ HTTPRegistrar  = (*dualCell)(nil)
	_ EventRegistrar = (*dualCell)(nil)
)

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestHTTPRegistrar_TypeAssertion(t *testing.T) {
	hc := &httpCell{BaseCell: *NewBaseCell(CellMetadata{ID: "accesscore"})}

	// The bootstrap pattern: type-assert from Cell interface.
	var c Cell = hc
	r, ok := c.(HTTPRegistrar)
	assert.True(t, ok, "httpCell should satisfy HTTPRegistrar")

	mux := &mockRouteMux{}
	r.RegisterRoutes(mux)

	assert.True(t, hc.registered)
	assert.Equal(t, []string{"/api/v1/sessions"}, mux.routes)
}

func TestHTTPRegistrar_NegativeTypeAssertion(t *testing.T) {
	plain := NewBaseCell(CellMetadata{ID: "plain-cell"})

	var c Cell = plain
	_, ok := c.(HTTPRegistrar)
	assert.False(t, ok, "plain BaseCell should NOT satisfy HTTPRegistrar")
}

func TestEventRegistrar_TypeAssertion(t *testing.T) {
	ec := &eventCell{BaseCell: *NewBaseCell(CellMetadata{ID: "auditcore"})}

	var c Cell = ec
	r, ok := c.(EventRegistrar)
	assert.True(t, ok, "eventCell should satisfy EventRegistrar")

	router := &mockEventRouter{}
	err := r.RegisterSubscriptions(router)
	assert.NoError(t, err)

	assert.True(t, ec.registered)
	assert.Equal(t, []string{"session.created"}, router.topics)
}

func TestEventRegistrar_NegativeTypeAssertion(t *testing.T) {
	plain := NewBaseCell(CellMetadata{ID: "plain-cell"})

	var c Cell = plain
	_, ok := c.(EventRegistrar)
	assert.False(t, ok, "plain BaseCell should NOT satisfy EventRegistrar")
}

func TestDualRegistrar_BothInterfaces(t *testing.T) {
	dc := &dualCell{BaseCell: *NewBaseCell(CellMetadata{ID: "device-core"})}

	var c Cell = dc

	// HTTP
	hr, ok := c.(HTTPRegistrar)
	assert.True(t, ok)
	mux := &mockRouteMux{}
	hr.RegisterRoutes(mux)
	assert.True(t, dc.httpRegistered)
	assert.Equal(t, []string{"/api/v1/devices"}, mux.routes)

	// Event
	er, ok := c.(EventRegistrar)
	assert.True(t, ok)
	router := &mockEventRouter{}
	err := er.RegisterSubscriptions(router)
	assert.NoError(t, err)
	assert.True(t, dc.eventRegistered)
	assert.Equal(t, []string{"device.enrolled"}, router.topics)
}

// ---------------------------------------------------------------------------
// ConfigReloader mock + tests
// ---------------------------------------------------------------------------

// configReloaderCell implements ConfigReloader.
type configReloaderCell struct {
	BaseCell
	lastEvent *ConfigChangeEvent
	err       error // configurable error to return
}

func (c *configReloaderCell) OnConfigReload(event ConfigChangeEvent) error {
	c.lastEvent = &event
	return c.err
}

// Compile-time check.
var _ ConfigReloader = (*configReloaderCell)(nil)

// httpAndReloaderCell implements both HTTPRegistrar and ConfigReloader.
type httpAndReloaderCell struct {
	BaseCell
	httpRegistered bool
	lastEvent      *ConfigChangeEvent
}

func (c *httpAndReloaderCell) RegisterRoutes(mux RouteMux) {
	c.httpRegistered = true
	mux.Handle("/api/v1/keys", http.NotFoundHandler())
}

func (c *httpAndReloaderCell) OnConfigReload(event ConfigChangeEvent) error {
	c.lastEvent = &event
	return nil
}

// Compile-time checks.
var (
	_ HTTPRegistrar  = (*httpAndReloaderCell)(nil)
	_ ConfigReloader = (*httpAndReloaderCell)(nil)
)

func TestConfigReloader_TypeAssertion(t *testing.T) {
	rc := &configReloaderCell{BaseCell: *NewBaseCell(CellMetadata{ID: "auth-core"})}

	var c Cell = rc
	cr, ok := c.(ConfigReloader)
	assert.True(t, ok, "configReloaderCell should satisfy ConfigReloader")

	event := ConfigChangeEvent{
		Added:   []string{"new.key"},
		Updated: []string{"server.port"},
		Removed: []string{"old.key"},
		Config:  map[string]any{"new.key": "val", "server.port": 9090},
	}
	err := cr.OnConfigReload(event)
	assert.NoError(t, err)
	assert.Equal(t, &event, rc.lastEvent)
}

func TestConfigReloader_NegativeTypeAssertion(t *testing.T) {
	plain := NewBaseCell(CellMetadata{ID: "plain-cell"})

	var c Cell = plain
	_, ok := c.(ConfigReloader)
	assert.False(t, ok, "plain BaseCell should NOT satisfy ConfigReloader")
}

func TestConfigReloader_DualHTTPAndReloader(t *testing.T) {
	hrc := &httpAndReloaderCell{BaseCell: *NewBaseCell(CellMetadata{ID: "accesscore"})}

	var c Cell = hrc

	// HTTP
	hr, ok := c.(HTTPRegistrar)
	assert.True(t, ok)
	mux := &mockRouteMux{}
	hr.RegisterRoutes(mux)
	assert.True(t, hrc.httpRegistered)
	assert.Equal(t, []string{"/api/v1/keys"}, mux.routes)

	// ConfigReloader
	cr, ok := c.(ConfigReloader)
	assert.True(t, ok)
	event := ConfigChangeEvent{
		Updated: []string{"auth.signing_key"},
		Config:  map[string]any{"auth.signing_key": "new-key"},
	}
	err := cr.OnConfigReload(event)
	assert.NoError(t, err)
	assert.Equal(t, &event, hrc.lastEvent)
}

func TestConfigReloader_ReturnsError(t *testing.T) {
	rc := &configReloaderCell{
		BaseCell: *NewBaseCell(CellMetadata{ID: "failing-cell"}),
		err:      errors.New("reload failed"),
	}

	var c Cell = rc
	cr, ok := c.(ConfigReloader)
	assert.True(t, ok)

	err := cr.OnConfigReload(ConfigChangeEvent{})
	assert.EqualError(t, err, "reload failed")
}

// --- HealthContributor ---

// healthContributorCell implements HealthContributor.
type healthContributorCell struct {
	BaseCell
	checkers map[string]func(context.Context) error
}

var _ HealthContributor = (*healthContributorCell)(nil)

func (c *healthContributorCell) HealthCheckers() map[string]func(context.Context) error {
	return c.checkers
}

func TestHealthContributor_TypeAssertion(t *testing.T) {
	hc := &healthContributorCell{
		BaseCell: *NewBaseCell(CellMetadata{ID: "accesscore"}),
		checkers: map[string]func(context.Context) error{
			"session-store": func(_ context.Context) error { return nil },
		},
	}

	var c Cell = hc
	hcc, ok := c.(HealthContributor)
	assert.True(t, ok, "healthContributorCell should satisfy HealthContributor")

	checkers := hcc.HealthCheckers()
	assert.Contains(t, checkers, "session-store")
	assert.NoError(t, checkers["session-store"](context.Background()))
}

func TestHealthContributor_NegativeTypeAssertion(t *testing.T) {
	plain := NewBaseCell(CellMetadata{ID: "plain-cell"})

	var c Cell = plain
	_, ok := c.(HealthContributor)
	assert.False(t, ok, "plain BaseCell should NOT satisfy HealthContributor")
}

func TestHealthContributor_EmptyMap(t *testing.T) {
	hc := &healthContributorCell{
		BaseCell: *NewBaseCell(CellMetadata{ID: "no-probes"}),
		checkers: map[string]func(context.Context) error{},
	}
	assert.Empty(t, hc.HealthCheckers())
}

func TestRouteMux_Group(t *testing.T) {
	mux := &mockRouteMux{}

	mux.Group(func(sub RouteMux) {
		sub.Handle("/api/v1/health", http.NotFoundHandler())
		sub.Handle("/api/v1/ready", http.NotFoundHandler())
	})

	assert.Equal(t, 1, mux.groups)
	assert.Equal(t, []string{"/api/v1/health", "/api/v1/ready"}, mux.routes)
}

func TestRouteMux_Route(t *testing.T) {
	mux := &mockRouteMux{}

	mux.Route("/api/v1", func(sub RouteMux) {
		sub.Handle("/ping", http.NotFoundHandler())
		sub.Route("/sessions", func(s RouteMux) {
			s.Handle("/login", http.NotFoundHandler())
		})
	})

	assert.Contains(t, mux.routes, "/api/v1/ping")
	assert.Contains(t, mux.routes, "/api/v1/sessions/login")
}

func TestRouteMux_Mount(t *testing.T) {
	mux := &mockRouteMux{}
	mux.Mount("/api/v1/users", http.NotFoundHandler())
	assert.Equal(t, []string{"/api/v1/users/*"}, mux.routes)
}

// ---------------------------------------------------------------------------
// AuthRouteMeta / AuthRouteDeclarer
// ---------------------------------------------------------------------------

// collectingDeclarer is a minimal AuthRouteDeclarer stub used to verify the
// metadata-forwarding contract without pulling runtime/auth into the tests.
type collectingDeclarer struct {
	metas []AuthRouteMeta
}

func (c *collectingDeclarer) DeclareAuthMeta(m AuthRouteMeta) {
	c.metas = append(c.metas, m)
}

var _ AuthRouteDeclarer = (*collectingDeclarer)(nil)

func TestAuthRouteMeta_ZeroValue(t *testing.T) {
	var m AuthRouteMeta
	assert.Empty(t, m.Method)
	assert.Empty(t, m.Path)
	assert.False(t, m.Public)
	assert.False(t, m.PasswordResetExempt)
	assert.False(t, m.Delegated)
}

// ---------------------------------------------------------------------------
// LifecycleContributor
// ---------------------------------------------------------------------------

// lifecycleContributorCell implements LifecycleContributor for testing.
type lifecycleContributorCell struct {
	BaseCell
	hooks []LifecycleHook
}

var _ LifecycleContributor = (*lifecycleContributorCell)(nil)

func (c *lifecycleContributorCell) LifecycleHooks() []LifecycleHook {
	return c.hooks
}

func TestLifecycleContributor_ReturnsTwoHooks(t *testing.T) {
	onStart1 := func(_ context.Context) error { return nil }
	onStop1 := func(_ context.Context) error { return nil }
	onStart2 := func(_ context.Context) error { return nil }

	lcc := &lifecycleContributorCell{
		BaseCell: *NewBaseCell(CellMetadata{ID: "mycore"}),
		hooks: []LifecycleHook{
			{Name: "hook-alpha", OnStart: onStart1, OnStop: onStop1},
			{Name: "hook-beta", OnStart: onStart2},
		},
	}

	var c Cell = lcc
	lc, ok := c.(LifecycleContributor)
	assert.True(t, ok, "lifecycleContributorCell should satisfy LifecycleContributor")

	hooks := lc.LifecycleHooks()
	assert.Len(t, hooks, 2)
	assert.Equal(t, "hook-alpha", hooks[0].Name)
	assert.Equal(t, "hook-beta", hooks[1].Name)
}

func TestLifecycleContributor_ReturnsNil(t *testing.T) {
	lcc := &lifecycleContributorCell{
		BaseCell: *NewBaseCell(CellMetadata{ID: "mycore"}),
		hooks:    nil,
	}
	hooks := lcc.LifecycleHooks()
	assert.Nil(t, hooks, "nil return is a valid opt-out")
}

func TestLifecycleContributor_ReturnsEmptySlice(t *testing.T) {
	lcc := &lifecycleContributorCell{
		BaseCell: *NewBaseCell(CellMetadata{ID: "mycore"}),
		hooks:    []LifecycleHook{},
	}
	hooks := lcc.LifecycleHooks()
	assert.Empty(t, hooks, "empty slice is a valid opt-out")
}

func TestLifecycleContributor_NegativeTypeAssertion(t *testing.T) {
	plain := NewBaseCell(CellMetadata{ID: "plain-cell"})
	var c Cell = plain
	_, ok := c.(LifecycleContributor)
	assert.False(t, ok, "plain BaseCell should NOT satisfy LifecycleContributor")
}

func TestAuthRouteDeclarer_InterfaceAssertion(t *testing.T) {
	// *http.ServeMux does NOT satisfy AuthRouteDeclarer — auth.Declare falls
	// back to route-only registration in that case.
	var mux RouteHandler = http.NewServeMux()
	_, ok := mux.(AuthRouteDeclarer)
	assert.False(t, ok, "stdlib ServeMux must not satisfy AuthRouteDeclarer")

	// The collecting stub satisfies the interface.
	var d AuthRouteDeclarer = &collectingDeclarer{}
	d.DeclareAuthMeta(AuthRouteMeta{Method: "POST", Path: "/x", Public: true})
	d.DeclareAuthMeta(AuthRouteMeta{Method: "GET", Path: "/y"})

	got := d.(*collectingDeclarer).metas
	assert.Len(t, got, 2)
	assert.Equal(t, "POST", got[0].Method)
	assert.True(t, got[0].Public)
	assert.Equal(t, "/y", got[1].Path)
}
