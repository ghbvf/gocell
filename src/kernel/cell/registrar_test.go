package cell

import (
	"context"
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

func (m *mockRouteMux) Use(_ ...func(http.Handler) http.Handler) {}

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

// mockSubscriber implements outbox.Subscriber for testing.
type mockSubscriber struct {
	topics []string
}

func (m *mockSubscriber) Subscribe(_ context.Context, topic string, _ func(context.Context, outbox.Entry) error) error {
	m.topics = append(m.topics, topic)
	return nil
}

func (m *mockSubscriber) Close() error { return nil }

// Compile-time check.
var _ outbox.Subscriber = (*mockSubscriber)(nil)

// eventCell is a Cell that also implements EventRegistrar.
type eventCell struct {
	BaseCell
	registered bool
}

func (e *eventCell) RegisterSubscriptions(sub outbox.Subscriber) {
	e.registered = true
	_ = sub.Subscribe(context.Background(), "session.created", func(_ context.Context, _ outbox.Entry) error {
		return nil
	})
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

func (d *dualCell) RegisterSubscriptions(sub outbox.Subscriber) {
	d.eventRegistered = true
	_ = sub.Subscribe(context.Background(), "device.enrolled", func(_ context.Context, _ outbox.Entry) error {
		return nil
	})
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
	hc := &httpCell{BaseCell: *NewBaseCell(CellMetadata{ID: "access-core"})}

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
	ec := &eventCell{BaseCell: *NewBaseCell(CellMetadata{ID: "audit-core"})}

	var c Cell = ec
	r, ok := c.(EventRegistrar)
	assert.True(t, ok, "eventCell should satisfy EventRegistrar")

	sub := &mockSubscriber{}
	r.RegisterSubscriptions(sub)

	assert.True(t, ec.registered)
	assert.Equal(t, []string{"session.created"}, sub.topics)
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
	sub := &mockSubscriber{}
	er.RegisterSubscriptions(sub)
	assert.True(t, dc.eventRegistered)
	assert.Equal(t, []string{"device.enrolled"}, sub.topics)
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
