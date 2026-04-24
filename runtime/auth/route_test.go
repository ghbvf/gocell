package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureMux pairs a stdlib ServeMux with an AuthRouteDeclarer counter so
// tests can assert both sides of auth.Mount's dispatch in a single mux.
type captureMux struct {
	*http.ServeMux
	metas []cell.AuthRouteMeta
}

func newCaptureMux() *captureMux { return &captureMux{ServeMux: http.NewServeMux()} }

func (m *captureMux) Handle(pattern string, h http.Handler) { m.ServeMux.Handle(pattern, h) }

func (m *captureMux) DeclareAuthMeta(meta cell.AuthRouteMeta) {
	m.metas = append(m.metas, meta)
}

var (
	_ cell.RouteHandler      = (*captureMux)(nil)
	_ cell.AuthRouteDeclarer = (*captureMux)(nil)
)

var noopHandler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

func loginContractSpec() wrapper.ContractSpec {
	return wrapper.ContractSpec{
		ID: "http.auth.login.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/access/sessions/login",
	}
}

func TestMount_ContractDrivenRoute_RegistersAndForwardsMeta(t *testing.T) {
	mux := newCaptureMux()
	handlerCalled := false
	Mount(mux, Route{
		Contract: loginContractSpec(),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalled = true
			w.WriteHeader(http.StatusOK)
		}),
		Public: true,
	})

	req := httptest.NewRequest("POST", "/api/v1/access/sessions/login", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.True(t, handlerCalled)
	require.Len(t, mux.metas, 1)
	assert.Equal(t, "POST", mux.metas[0].Method)
	assert.Equal(t, "/api/v1/access/sessions/login", mux.metas[0].Path)
	assert.True(t, mux.metas[0].Public)
}

func TestMount_WritesContractIDIntoContext(t *testing.T) {
	mux := newCaptureMux()
	var seen string
	Mount(mux, Route{
		Contract: loginContractSpec(),
		Handler: http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen, _ = ctxkeys.ContractIDFrom(r.Context())
		}),
		Public: true,
	})
	req := httptest.NewRequest("POST", "/api/v1/access/sessions/login", nil)
	mux.ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, "http.auth.login.v1", seen)
}

func TestMount_PanicsOnMissingContractID(t *testing.T) {
	defer func() {
		require.NotNil(t, recover(), "expected panic on empty Contract.ID")
	}()
	Mount(newCaptureMux(), Route{Handler: noopHandler})
}

func TestMount_PanicsOnNilHandler(t *testing.T) {
	defer func() {
		require.NotNil(t, recover(), "expected panic on nil Handler")
	}()
	Mount(newCaptureMux(), Route{Contract: loginContractSpec()})
}

func TestMount_PanicsOnNonHTTPKind(t *testing.T) {
	defer func() {
		require.NotNil(t, recover(), "expected panic on non-http kind")
	}()
	Mount(newCaptureMux(), Route{
		Contract: wrapper.ContractSpec{ID: "event.x.v1", Kind: "event", Transport: "amqp", Topic: "x"},
		Handler:  noopHandler,
	})
}

func TestMount_PanicsOnInvalidMethod(t *testing.T) {
	defer func() {
		require.NotNil(t, recover(), "expected panic on invalid method")
	}()
	Mount(newCaptureMux(), Route{
		Contract: wrapper.ContractSpec{
			ID: "http.x.v1", Kind: "http", Transport: "http",
			Method: "foo", Path: "/x",
		},
		Handler: noopHandler,
	})
}

func TestMount_PanicsPublicWithPolicy(t *testing.T) {
	defer func() {
		require.NotNil(t, recover(), "expected panic on Public+Policy")
	}()
	Mount(newCaptureMux(), Route{
		Contract: loginContractSpec(),
		Handler:  noopHandler,
		Public:   true,
		Policy:   Authenticated(),
	})
}

func TestRequirePolicy_NilPanics(t *testing.T) {
	assert.PanicsWithValue(t, "auth.RequirePolicy: policy must not be nil", func() {
		RequirePolicy(nil)
	})
}
