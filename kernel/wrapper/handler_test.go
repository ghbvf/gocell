package wrapper_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

func loginSpec() wrapper.ContractSpec {
	return wrapper.ContractSpec{
		ID:        "http.auth.login.v1",
		Kind:      "http",
		Transport: "http",
		Method:    "POST",
		Path:      "/api/v1/auth/login",
	}
}

func okHandler(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	})
}

// TestHTTPHandler_WritesContractIDIntoContext verifies ctxkeys.ContractID
// is set for downstream handlers / slog.
func TestHTTPHandler_WritesContractIDIntoContext(t *testing.T) {
	t.Parallel()
	var seen string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, _ = ctxkeys.ContractIDFrom(r.Context())
	})
	h := wrapper.MustHTTPHandler(loginSpec(), inner)
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, "http.auth.login.v1", seen)
}

// TestHTTPHandler_AppendsContractAttrsToCarrier verifies that when an
// AttrCarrier is present in ctx (installed by the outer Tracing
// middleware), HTTPHandler appends the contract base attributes to it.
func TestHTTPHandler_AppendsContractAttrsToCarrier(t *testing.T) {
	t.Parallel()
	carrier := &wrapper.AttrCarrier{}
	inner := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})
	h := wrapper.MustHTTPHandler(loginSpec(), inner)

	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil).
		WithContext(wrapper.WithAttrCarrier(context.Background(), carrier))
	h.ServeHTTP(httptest.NewRecorder(), req)

	attrs := make(map[string]any, len(carrier.Attrs))
	for _, a := range carrier.Attrs {
		attrs[a.Key] = a.Value
	}
	assert.Equal(t, "http.auth.login.v1", attrs["gocell.contract.id"])
	assert.Equal(t, "http", attrs["gocell.contract.kind"])
	assert.Equal(t, "http", attrs["gocell.contract.transport"])
	assert.Equal(t, "POST", attrs["http.method"])
	assert.Equal(t, "/api/v1/auth/login", attrs["http.route"])
}

// TestHTTPHandler_NoCarrier_StillInvokesInner verifies wrapper silently
// skips attribute contribution when no AttrCarrier is installed (unit
// tests, ad-hoc wiring without the outer Tracing middleware).
func TestHTTPHandler_NoCarrier_StillInvokesInner(t *testing.T) {
	t.Parallel()
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	h := wrapper.MustHTTPHandler(loginSpec(), inner)
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.True(t, called, "inner handler must run even without carrier")
	assert.Equal(t, 200, rec.Code)
}

// TestHTTPHandler_ReturnsErrorOnInvalidSpec asserts registration-time
// validation: HTTPHandler returns a non-nil error rather than panicking.
func TestHTTPHandler_ReturnsErrorOnInvalidSpec(t *testing.T) {
	t.Parallel()
	cases := []wrapper.ContractSpec{
		{},                      // all empty
		{ID: "a", Kind: "http"}, // missing transport/method/path
		{ID: "a", Kind: "http", Transport: "http"},                 // missing method/path
		{ID: "a", Kind: "http", Transport: "http", Method: "POST"}, // missing path
	}
	for _, spec := range cases {
		_, err := wrapper.HTTPHandler(spec, okHandler(200))
		assert.Errorf(t, err, "expected error for %+v", spec)
	}
}

// TestHTTPHandler_ReturnsErrorOnNilHandler ensures nil inner handler is rejected.
func TestHTTPHandler_ReturnsErrorOnNilHandler(t *testing.T) {
	t.Parallel()
	_, err := wrapper.HTTPHandler(loginSpec(), nil)
	require.Error(t, err)
}

// TestHTTPHandler_ReturnsErrorOnNonHTTPKind ensures event-kind specs are rejected.
func TestHTTPHandler_ReturnsErrorOnNonHTTPKind(t *testing.T) {
	t.Parallel()
	_, err := wrapper.HTTPHandler(wrapper.ContractSpec{
		ID: "event.x.v1", Kind: "event", Transport: "amqp", Topic: "x",
	}, okHandler(200))
	require.Error(t, err)
}

// TestMustHTTPHandler_PanicsOnNilHandler asserts the Must wrapper panics on
// the same misuse where HTTPHandler returns error.
func TestMustHTTPHandler_PanicsOnNilHandler(t *testing.T) {
	t.Parallel()
	defer func() {
		require.NotNil(t, recover(), "expected panic on nil handler")
	}()
	_ = wrapper.MustHTTPHandler(loginSpec(), nil)
}

// TestAttrCarrier_Nil_ReturnsCtxUnchanged ensures WithAttrCarrier(nil) is a
// no-op so callers can pass optional carriers.
func TestAttrCarrier_Nil_ReturnsCtxUnchanged(t *testing.T) {
	t.Parallel()
	base := context.Background()
	got := wrapper.WithAttrCarrier(base, nil)
	if got != base {
		t.Fatalf("WithAttrCarrier(nil) must return ctx unchanged")
	}
	_, ok := wrapper.AttrCarrierFrom(got)
	assert.False(t, ok)
}

// TestAttrCarrier_RoundTrip verifies AttrCarrierFrom retrieves the installed carrier.
func TestAttrCarrier_RoundTrip(t *testing.T) {
	t.Parallel()
	c := &wrapper.AttrCarrier{}
	ctx := wrapper.WithAttrCarrier(context.Background(), c)
	got, ok := wrapper.AttrCarrierFrom(ctx)
	require.True(t, ok)
	assert.Same(t, c, got)
}
