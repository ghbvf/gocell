// Package celltest provides test utilities for kernel/cell types.
// It follows the net/http → net/http/httptest pattern.
//
// # Auth metadata recording
//
// TestMux implements [cell.AuthRouteDeclarer]: every auth.Mount call on a
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
	"strings"

	"github.com/ghbvf/gocell/kernel/cell"
)

// Compile-time checks.
var _ cell.RouteMux = (*TestMux)(nil)
var _ cell.AuthRouteDeclarer = (*TestMux)(nil)
var _ cell.Prefixer = (*TestMux)(nil)

// TestMux adapts http.ServeMux to cell.RouteMux for testing.
// It uses Go 1.22+ ServeMux pattern matching ("GET /path/{param}").
//
// Route-composition model: sub-muxes created by Route share the root's
// underlying *http.ServeMux and register every pattern as a fully-qualified
// path (prefix + sub-relative pattern). This mirrors chi's Route semantics
// and avoids the stdlib StripPrefix + 307-redirect pitfall where a POST to
// "/api/v1/access/users" would redirect to "/api/v1/access/users/" and drop
// its body. All Handle calls on any sub ultimately register an absolute
// pattern on the root ServeMux.
//
// Auth metadata: every auth.Mount call forwards the declared
// [cell.AuthRouteMeta] to the root TestMux via DeclareAuthMeta. Sub-muxes
// compose the mount prefix before forwarding so the root always sees the
// full path (e.g. "/api/v1/access/sessions/{id}").
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

// Handle registers a handler for the given pattern. For sub-muxes, the
// configured prefix is composed into the pattern before registration so the
// root ServeMux sees an absolute path — matching chi's Route + Handle
// semantics and avoiding stdlib StripPrefix trailing-slash redirects.
//
// For the collection-root case (relative pattern "/"), Handle registers
// both `prefix` and `prefix+"/"` so the root mux matches the resource
// regardless of whether the Contract author spelled the path with or
// without a trailing slash (/api/v1/config vs /api/v1/config/). This
// mirrors chi's redirect-free behaviour.
func (m *TestMux) Handle(pattern string, handler http.Handler) {
	for _, p := range m.composePatterns(pattern) {
		m.root.ServeMux.Handle(p, handler)
	}
}

// composePatterns returns the absolute pattern(s) to register on the root
// mux for the given sub-relative pattern. The returned slice has one entry
// for non-root patterns and two entries (with/without trailing slash) for
// the collection-root case.
func (m *TestMux) composePatterns(pattern string) []string {
	if m.prefix == "" {
		return []string{pattern}
	}
	if idx := strings.IndexByte(pattern, ' '); idx >= 0 {
		method := pattern[:idx]
		p := pattern[idx+1:]
		return prependMethod(method, composePaths(m.prefix, p))
	}
	return composePaths(m.prefix, pattern)
}

func prependMethod(method string, paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = method + " " + p
	}
	return out
}

func composePaths(prefix, relative string) []string {
	if relative == "/" || relative == "" {
		// Root of the sub-tree: match both prefix and prefix+"/" so the
		// Contract.Path author can spell it either way.
		return []string{prefix, prefix + "/"}
	}
	return []string{path.Clean(prefix + relative)}
}

// Route creates a sub-mux that shares the root ServeMux. Patterns registered
// on the sub are composed into absolute paths via the sub's prefix — no
// StripPrefix, no trailing-slash redirects.
func (m *TestMux) Route(pattern string, fn func(cell.RouteMux)) {
	composedPrefix := path.Clean(m.prefix + pattern)
	if composedPrefix == "." {
		composedPrefix = ""
	}
	sub := &TestMux{
		ServeMux: m.root.ServeMux,
		root:     m.root,
		prefix:   composedPrefix,
	}
	fn(sub)
}

// Mount attaches an http.Handler under the given prefix with stripping.
func (m *TestMux) Mount(pattern string, handler http.Handler) {
	m.root.ServeMux.Handle(path.Clean(m.prefix+pattern)+"/", http.StripPrefix(path.Clean(m.prefix+pattern), handler))
}

// Group calls fn with the same mux (no prefix change).
func (m *TestMux) Group(fn func(cell.RouteMux)) {
	fn(m)
}

// With returns the same TestMux; stdlib ServeMux has no middleware chain.
func (m *TestMux) With(_ ...func(http.Handler) http.Handler) cell.RouteMux {
	return m
}
