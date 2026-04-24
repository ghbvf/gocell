package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loginContractSpec() wrapper.ContractSpec {
	return wrapper.ContractSpec{
		ID:        "http.auth.login.v1",
		Kind:      "http",
		Transport: "http",
		Method:    "POST",
		Path:      "/api/v1/auth/login",
	}
}

func TestMount_ContractDrivenRoute_RegistersAtRouteMethodPath(t *testing.T) {
	mux := newCaptureMux()
	var handlerCalled bool
	Mount(mux, Route{
		Contract: loginContractSpec(),
		Method:   "POST",
		Path:     "/api/v1/auth/login",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			handlerCalled = true
			w.WriteHeader(http.StatusOK)
		}),
		Public: true,
	})

	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.True(t, handlerCalled, "handler not invoked")
	require.Len(t, mux.metas, 1)
	assert.Equal(t, "POST", mux.metas[0].Method)
	assert.Equal(t, "/api/v1/auth/login", mux.metas[0].Path)
	assert.True(t, mux.metas[0].Public)
}

func TestMount_ContractDrivenRoute_WrapsHandlerForContractID(t *testing.T) {
	mux := newCaptureMux()
	var seenContract string
	Mount(mux, Route{
		Contract: loginContractSpec(),
		Method:   "POST",
		Path:     "/api/v1/auth/login",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seenContract = wrapper.ContractIDFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}),
		Public: true,
	})

	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	mux.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, "http.auth.login.v1", seenContract,
		"wrapper.HTTPHandler must have installed ContractID into context")
}

func TestMount_PanicsWhenContractMethodMismatchesRoute(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic")
		assert.Contains(t, r.(string), "does not match Contract.Method")
	}()
	Mount(newCaptureMux(), Route{
		Contract: loginContractSpec(), // Contract.Method = "POST"
		Method:   "GET",               // deliberate mismatch
		Path:     "/api/v1/auth/login",
		Handler:  noopHandler,
	})
}

func TestMount_PanicsOnNonHTTPContractKind(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic")
		assert.Contains(t, r.(string), "Contract.Kind")
	}()
	Mount(newCaptureMux(), Route{
		Contract: wrapper.ContractSpec{ID: "event.x.v1", Kind: "event", Transport: "amqp", Topic: "x"},
		Method:   "POST",
		Path:     "/x",
		Handler:  noopHandler,
	})
}

func TestMount_LegacyFields_NoContract_RoutesWithoutWrapper(t *testing.T) {
	mux := newCaptureMux()
	var seenContract string
	Mount(mux, Route{
		Method: "GET",
		Path:   "/legacy",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Legacy registrations don't flow through wrapper, so no
			// ContractID is injected — the absence is the test.
			if v, ok := ctxkeys.ContractIDFrom(r.Context()); ok {
				seenContract = v
			}
			w.WriteHeader(http.StatusOK)
		}),
	})

	req := httptest.NewRequest("GET", "/legacy", nil)
	mux.ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, "", seenContract, "legacy route must not carry ContractID in ctx")
}

func TestMount_HandlerNil_Panics(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic")
	}()
	Mount(newCaptureMux(), Route{Contract: loginContractSpec(), Method: "POST", Path: "/x"})
}

func TestMount_PathNormalised(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{in: "/a//b/../c", want: "/a/c"},
		{in: "/login/", want: "/login"}, // path.Clean strips trailing slashes
		{in: "/api/v1/access/login", want: "/api/v1/access/login"},
		{in: "/a//b", want: "/a/b"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			mux := newCaptureMux()
			Mount(mux, Route{Method: "GET", Path: tc.in, Handler: noopHandler})
			require.Len(t, mux.metas, 1)
			assert.Equal(t, tc.want, mux.metas[0].Path)
		})
	}
}
