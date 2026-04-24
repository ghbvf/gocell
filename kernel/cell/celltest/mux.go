// Package celltest provides test utilities for kernel/cell types.
// It follows the net/http → net/http/httptest pattern.
//
// # Auth metadata recording
//
// TestMux implements [cell.AuthRouteDeclarer]: every auth.Declare call on a
// TestMux (or on a sub-mux produced by Route) records an [cell.AuthRouteMeta]
// in the root TestMux's authMetas slice. Tests that care about auth metadata
// inspect [TestMux.DeclaredAuthMetas] directly.
//
// TestMux does NOT enforce Public/Policy semantics — it only records metadata.
// Tests that must assert 401/403 behaviour should use the production Router
// wired with a fake verifier (runtime/http/router.WithAuthMiddleware) rather
// than TestMux.
package celltest

import (
	"net/http"
	"path"

	"github.com/ghbvf/gocell/kernel/cell"
)

// Compile-time checks.
var _ cell.RouteMux = (*TestMux)(nil)
var _ cell.AuthRouteDeclarer = (*TestMux)(nil)
var _ cell.PrefixedMux = (*TestMux)(nil)

// TestMux adapts http.ServeMux to cell.RouteMux for testing.
// It uses Go 1.22+ ServeMux pattern matching ("GET /path/{param}").
//
// Auth metadata: every auth.Declare call forwards the declared
// [cell.AuthRouteMeta] to the root TestMux via DeclareAuthMeta. Sub-muxes
// created by Route compose the mount prefix before forwarding so the root
// always sees the full path (e.g. "/api/v1/access/sessions/{id}").
type TestMux struct {
	*http.ServeMux
	// root is the top-level TestMux that owns the authMetas slice.
	// For a root TestMux, root == m. For sub-muxes, root points to the root.
	root *TestMux
	// prefix is the composed mount path for this sub-mux (empty for root).
	prefix string
	// authMetas accumulates metadata only on the root TestMux.
	authMetas []cell.AuthRouteMeta
}

// NewTestMux creates a TestMux backed by a stdlib ServeMux.
func NewTestMux() *TestMux {
	m := &TestMux{ServeMux: http.NewServeMux()}
	m.root = m // root points to itself
	return m
}

// Prefix returns the composed mount prefix for this test mux. Root muxes
// return ""; sub-muxes created by Route return the same prefix production
// chiRouterAdapter exposes, allowing auth.Mount to derive chi-relative
// registration paths from fully-qualified Contract.Path literals.
func (m *TestMux) Prefix() string {
	return m.prefix
}

// UseRelativeContractAliases allows auth.Mount to install compatibility
// aliases only for root-level slice tests. Production routers do not
// implement this method, so aliases cannot leak into served applications.
func (m *TestMux) UseRelativeContractAliases() bool {
	return m.prefix == ""
}

// DeclareAuthMeta records an auth route declaration.
// Sub-muxes compose their prefix with meta.Path before forwarding to root.
func (m *TestMux) DeclareAuthMeta(meta cell.AuthRouteMeta) {
	if m.prefix != "" {
		meta.Path = path.Clean(m.prefix + meta.Path)
	}
	m.root.authMetas = append(m.root.authMetas, meta)
}

// DeclaredAuthMetas returns a copy of all auth metadata recorded on this root
// TestMux, in declaration order.
func (m *TestMux) DeclaredAuthMetas() []cell.AuthRouteMeta {
	if len(m.root.authMetas) == 0 {
		return nil
	}
	out := make([]cell.AuthRouteMeta, len(m.root.authMetas))
	copy(out, m.root.authMetas)
	return out
}

// Handle registers a handler for the given pattern.
func (m *TestMux) Handle(pattern string, handler http.Handler) {
	m.ServeMux.Handle(pattern, handler)
}

// Route creates a sub-mux with prefix stripping.
// The sub-mux's DeclareAuthMeta forwards metadata to the root with the
// composed prefix so declared paths reflect the full mount path.
func (m *TestMux) Route(pattern string, fn func(cell.RouteMux)) {
	composedPrefix := path.Clean(m.prefix + pattern)
	sub := &TestMux{
		ServeMux: http.NewServeMux(),
		root:     m.root,
		prefix:   composedPrefix,
	}
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

// With returns the same TestMux; stdlib ServeMux has no middleware chain.
func (m *TestMux) With(_ ...func(http.Handler) http.Handler) cell.RouteMux {
	return m
}
