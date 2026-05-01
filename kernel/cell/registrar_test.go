package cell

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
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

// routeGroupCell is a Cell that implements RouteGroupContributor.
type routeGroupCell struct {
	BaseCell
	groups []RouteGroup
}

func (r *routeGroupCell) RouteGroups() []RouteGroup { return r.groups }

// Compile-time check.
var _ RouteGroupContributor = (*routeGroupCell)(nil)

// mockEventRouter implements EventRouter for testing.
type mockEventRouter struct {
	topics []string
}

func (m *mockEventRouter) AddContractHandler(spec wrapper.ContractSpec, _ outbox.EntryHandler, _ string, _ ...SubscriptionOption) error {
	m.topics = append(m.topics, spec.Topic)
	return nil
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
	return r.AddContractHandler(testEventSpec("session.created"), func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}, "test")
}

// Compile-time check.
var _ EventRegistrar = (*eventCell)(nil)

// dualRouteGroupEventCell implements both RouteGroupContributor and EventRegistrar.
type dualRouteGroupEventCell struct {
	BaseCell
	groups          []RouteGroup
	eventRegistered bool
}

func (d *dualRouteGroupEventCell) RouteGroups() []RouteGroup { return d.groups }

func (d *dualRouteGroupEventCell) RegisterSubscriptions(r EventRouter) error {
	d.eventRegistered = true
	return r.AddContractHandler(testEventSpec("device.enrolled"), func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}, "test")
}

// Compile-time checks.
var (
	_ RouteGroupContributor = (*dualRouteGroupEventCell)(nil)
	_ EventRegistrar        = (*dualRouteGroupEventCell)(nil)
)

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRouteGroupContributor_TypeAssertion(t *testing.T) {
	ref := PrimaryListener
	rg := &routeGroupCell{
		BaseCell: *NewBaseCell(CellMetadata{ID: "accesscore"}),
		groups: []RouteGroup{
			{
				Listener: ref,
				Prefix:   "/api/v1/sessions",
				Register: func(mux RouteMux) error { mux.Handle("/login", http.NotFoundHandler()); return nil },
			},
		},
	}

	// The bootstrap pattern: type-assert from Cell interface.
	var c Cell = rg
	rgc, ok := c.(RouteGroupContributor)
	assert.True(t, ok, "routeGroupCell should satisfy RouteGroupContributor")

	groups := rgc.RouteGroups()
	assert.Len(t, groups, 1)
	assert.Equal(t, ref, groups[0].Listener)
	assert.Equal(t, "/api/v1/sessions", groups[0].Prefix)
	assert.NotNil(t, groups[0].Register)
}

func TestRouteGroupContributor_NegativeTypeAssertion(t *testing.T) {
	plain := NewBaseCell(CellMetadata{ID: "plain-cell"})

	var c Cell = plain
	_, ok := c.(RouteGroupContributor)
	assert.False(t, ok, "plain BaseCell should NOT satisfy RouteGroupContributor")
}

func TestRouteGroupContributor_EmptyGroups(t *testing.T) {
	rg := &routeGroupCell{
		BaseCell: *NewBaseCell(CellMetadata{ID: "empty-cell"}),
		groups:   nil,
	}
	var c Cell = rg
	rgc, ok := c.(RouteGroupContributor)
	assert.True(t, ok)
	assert.Empty(t, rgc.RouteGroups(), "nil groups is a valid opt-out")
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

func TestDualRouteGroupEventCell_BothInterfaces(t *testing.T) {
	ref := PrimaryListener
	dc := &dualRouteGroupEventCell{
		BaseCell: *NewBaseCell(CellMetadata{ID: "device-core"}),
		groups: []RouteGroup{
			{
				Listener: ref,
				Prefix:   "/api/v1/devices",
				Register: func(mux RouteMux) error { mux.Handle("/", http.NotFoundHandler()); return nil },
			},
		},
	}

	var c Cell = dc

	// RouteGroupContributor
	rgc, ok := c.(RouteGroupContributor)
	assert.True(t, ok)
	groups := rgc.RouteGroups()
	assert.Len(t, groups, 1)
	assert.Equal(t, "/api/v1/devices", groups[0].Prefix)

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

// routeGroupAndReloaderCell implements both RouteGroupContributor and ConfigReloader.
type routeGroupAndReloaderCell struct {
	BaseCell
	groups    []RouteGroup
	lastEvent *ConfigChangeEvent
}

func (c *routeGroupAndReloaderCell) RouteGroups() []RouteGroup { return c.groups }

func (c *routeGroupAndReloaderCell) OnConfigReload(event ConfigChangeEvent) error {
	c.lastEvent = &event
	return nil
}

// Compile-time checks.
var (
	_ RouteGroupContributor = (*routeGroupAndReloaderCell)(nil)
	_ ConfigReloader        = (*routeGroupAndReloaderCell)(nil)
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

func TestConfigReloader_DualRouteGroupAndReloader(t *testing.T) {
	ref := PrimaryListener
	hrc := &routeGroupAndReloaderCell{
		BaseCell: *NewBaseCell(CellMetadata{ID: "accesscore"}),
		groups: []RouteGroup{
			{
				Listener: ref,
				Prefix:   "/api/v1/keys",
				Register: func(mux RouteMux) error { mux.Handle("/", http.NotFoundHandler()); return nil },
			},
		},
	}

	var c Cell = hrc

	// RouteGroupContributor
	rgc, ok := c.(RouteGroupContributor)
	assert.True(t, ok)
	groups := rgc.RouteGroups()
	assert.Len(t, groups, 1)
	assert.Equal(t, "/api/v1/keys", groups[0].Prefix)

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

func (c *collectingDeclarer) DeclareAuthMeta(m AuthRouteMeta) error {
	c.metas = append(c.metas, m)
	return nil
}

var _ AuthRouteDeclarer = (*collectingDeclarer)(nil)

func TestAuthRouteMeta_ZeroValue(t *testing.T) {
	var m AuthRouteMeta
	assert.Empty(t, m.Method)
	assert.Empty(t, m.Path)
	assert.False(t, m.Public)
	assert.False(t, m.PasswordResetExempt)
	assert.False(t, m.IsInternal(), "zero-value AuthRouteMeta should not be internal (empty path)")
}

func TestAuthRouteMeta_IsInternal(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		internal bool
	}{
		{name: "internal path", path: "/internal/v1/rbac/check", internal: true},
		{name: "api path", path: "/api/v1/sessions", internal: false},
		{name: "empty path", path: "", internal: false},
		{name: "partial prefix", path: "/internal/", internal: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := AuthRouteMeta{Path: tt.path}
			assert.Equal(t, tt.internal, m.IsInternal())
		})
	}
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
	// *http.ServeMux does NOT satisfy AuthRouteDeclarer — auth.Mount falls
	// back to route-only registration in that case.
	var mux RouteHandler = http.NewServeMux()
	_, ok := mux.(AuthRouteDeclarer)
	assert.False(t, ok, "stdlib ServeMux must not satisfy AuthRouteDeclarer")

	// The collecting stub satisfies the interface.
	var d AuthRouteDeclarer = &collectingDeclarer{}
	require.NoError(t, d.DeclareAuthMeta(AuthRouteMeta{Method: "POST", Path: "/x", Public: true}))
	require.NoError(t, d.DeclareAuthMeta(AuthRouteMeta{Method: "GET", Path: "/y"}))

	got := d.(*collectingDeclarer).metas
	assert.Len(t, got, 2)
	assert.Equal(t, "POST", got[0].Method)
	assert.True(t, got[0].Public)
	assert.Equal(t, "/y", got[1].Path)
}
