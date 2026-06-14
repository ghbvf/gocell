// Package cellmw provides reusable RouteHandler wrappers for use in cell slice
// handlers.
//
// # Why this lives in runtime/, not cells/
//
// Cell slice handlers that wrap a kcell.RouteHandler to inject middleware must
// forward DeclareHTTPContract(contractspec.ContractSpec) to satisfy the
// kcell.HTTPContractDeclarer interface that auth.Mount type-asserts at route
// registration time. Naming contractspec.ContractSpec in a cells/ file is
// forbidden by archtest CELLS-NO-CONTRACTSPEC-IMPORT-01 (contractspec is for
// generated contracts and runtime/ framework infra only). Moving the wrapper
// here — where runtime/ may freely import kernel/contractspec — resolves the
// violation while keeping the wrapper reusable across all cell slices.
package cellmw

import (
	"net/http"

	kcell "github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/contractspec"
)

// HeaderInjectMux wraps a kcell.RouteHandler and applies a middleware to every
// handler registered via Handle, while transparently forwarding Prefix /
// DeclareAuthMeta / DeclareHTTPContract to the inner mux so the framework's
// type-assertions still see the inner mux's capabilities.
//
// Typical use: a slice's RegisterRoutes wraps the incoming mux with
// NewHeaderInjectMux to inject a request-header value into ctx before the
// generated handler runs, without needing to name contractspec.ContractSpec in
// the slice package.
type HeaderInjectMux struct {
	inner kcell.RouteHandler
	wrap  func(http.Handler) http.Handler
}

// NewHeaderInjectMux returns a HeaderInjectMux that wraps inner with wrap on
// every Handle call. Both inner and wrap must be non-nil.
func NewHeaderInjectMux(inner kcell.RouteHandler, wrap func(http.Handler) http.Handler) *HeaderInjectMux {
	return &HeaderInjectMux{inner: inner, wrap: wrap}
}

// Handle wraps h with the injected middleware and registers the result on the
// inner mux.
func (m *HeaderInjectMux) Handle(pattern string, h http.Handler) {
	m.inner.Handle(pattern, m.wrap(h))
}

// Prefix delegates to the inner mux if it implements kcell.Prefixer.
// auth.Mount type-asserts the mux to Prefixer to compute the chi-relative
// registration path; without this forwarder the wrapper would swallow the
// assertion and the route would be registered with an incorrect path.
func (m *HeaderInjectMux) Prefix() string {
	if p, ok := m.inner.(kcell.Prefixer); ok {
		return p.Prefix()
	}
	return ""
}

// DeclareAuthMeta forwards auth-route metadata to the inner mux.
// auth.Mount type-asserts to kcell.AuthRouteDeclarer; without this forwarder
// the wrapper swallows the assertion and the router's policy-coverage check
// flags the route as "registered without auth.Mount".
func (m *HeaderInjectMux) DeclareAuthMeta(meta kcell.AuthRouteMeta) error {
	if d, ok := m.inner.(kcell.AuthRouteDeclarer); ok {
		return d.DeclareAuthMeta(meta)
	}
	return nil
}

// DeclareHTTPContract forwards the route's ContractSpec to the inner mux.
// auth.Mount type-asserts to kcell.HTTPContractDeclarer; without this
// forwarder the wrapper swallows the assertion and the router loses the
// contract binding required for observability and governance.
//
// This method is the sole reason this package lives in runtime/ rather than
// being inline in a cells/ slice: naming contractspec.ContractSpec in cells/
// is forbidden by CELLS-NO-CONTRACTSPEC-IMPORT-01.
func (m *HeaderInjectMux) DeclareHTTPContract(spec contractspec.ContractSpec) error {
	if d, ok := m.inner.(kcell.HTTPContractDeclarer); ok {
		return d.DeclareHTTPContract(spec)
	}
	return nil
}
