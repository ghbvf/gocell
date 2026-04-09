// Package celltest provides test utilities for kernel/cell types.
// It follows the net/http → net/http/httptest pattern.
package celltest

import (
	"net/http"

	"github.com/ghbvf/gocell/kernel/cell"
)

// Compile-time check: TestMux implements cell.RouteMux.
var _ cell.RouteMux = (*TestMux)(nil)

// TestMux adapts http.ServeMux to cell.RouteMux for testing.
// It uses Go 1.22+ ServeMux pattern matching ("GET /path/{param}").
type TestMux struct {
	*http.ServeMux
}

// NewTestMux creates a TestMux backed by a stdlib ServeMux.
func NewTestMux() *TestMux {
	return &TestMux{http.NewServeMux()}
}

// Handle registers a handler for the given pattern.
func (m *TestMux) Handle(pattern string, handler http.Handler) {
	m.ServeMux.Handle(pattern, handler)
}

// Route creates a sub-mux with prefix stripping.
func (m *TestMux) Route(pattern string, fn func(cell.RouteMux)) {
	sub := NewTestMux()
	fn(sub)
	m.ServeMux.Handle(pattern+"/", http.StripPrefix(pattern, sub.ServeMux))
}

// Mount attaches an http.Handler under the given prefix with stripping.
func (m *TestMux) Mount(pattern string, handler http.Handler) {
	m.ServeMux.Handle(pattern+"/", http.StripPrefix(pattern, handler))
}

// Group calls fn with the same mux (no prefix change).
func (m *TestMux) Group(fn func(cell.RouteMux)) {
	fn(m)
}

// Use is a no-op in TestMux; stdlib ServeMux has no middleware chain.
func (m *TestMux) Use(_ ...func(http.Handler) http.Handler) {}
