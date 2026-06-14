package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/transport"
)

// newTestRing creates a test HMAC key ring with a 32-byte key.
func newTestRing(t *testing.T) *auth.HMACKeyRing {
	t.Helper()
	ring, err := auth.NewHMACKeyRing([]byte("test-hmac-key-32-bytes-long-xxxxx"), nil)
	require.NoError(t, err)
	return ring
}

// httpTestTransport is a test transport.CellTransport that dials a real
// httptest.Server, standing in for the (US5 #1966) remote transport so the
// configclient's request shaping, service-token signing, and status→errcode
// mapping can be exercised end-to-end against a live handler. The client builds
// a path-only request; this double places it onto the test server's base URL.
type httpTestTransport struct {
	baseURL string
	client  *http.Client
}

func (h httpTestTransport) DoContract(ctx context.Context, _ string, req *http.Request) (*http.Response, error) {
	u, err := url.Parse(h.baseURL + req.URL.String())
	if err != nil {
		return nil, err
	}
	req.URL = u
	//nolint:gosec // G704: test transport dials the in-test httptest.Server; the URL is the test's own base address, not attacker-controlled.
	return h.client.Do(req.WithContext(ctx))
}

// transportTo builds an httpTestTransport for srv with the given client.
func transportTo(baseURL string, client *http.Client) httpTestTransport {
	return httpTestTransport{baseURL: baseURL, client: client}
}

// testTenant is a canonical UUID used across configclient tests.
var testTenant = mustParseTenant("f47ac10b-58cc-4372-a567-0e02b2c3d479")

func mustParseTenant(s string) tenant.TenantID {
	t, err := tenant.ParseTenantID(s)
	if err != nil {
		panic("mustParseTenant: " + err.Error())
	}
	return t
}

func TestHTTPConfigGetter_GetEntry_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/internal/v1/config/app.name", r.URL.Path)
		// Service token header must be present.
		assert.NotEmpty(t, r.Header.Get("Authorization"))
		assert.Contains(t, r.Header.Get("Authorization"), "ServiceToken")
		// X-Tenant-ID must match the tenant passed to GetEntry.
		assert.Equal(t, testTenant.String(), r.Header.Get("X-Tenant-ID"))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"id":        "cfg-1",
				"key":       "app.name",
				"value":     "gocell",
				"sensitive": false,
				"version":   3,
				"createdAt": "2024-01-01T00:00:00Z",
				"updatedAt": "2024-01-02T00:00:00Z",
			},
		})
	}))
	defer srv.Close()

	ring := newTestRing(t)
	client := NewHTTPConfigGetter(transportTo(srv.URL, srv.Client()), ring, clock.Real())
	entry, err := client.GetEntry(context.Background(), testTenant, "app.name")
	require.NoError(t, err)
	assert.Equal(t, "app.name", entry.Key)
	assert.Equal(t, "gocell", entry.Value)
	assert.False(t, entry.Sensitive)
	assert.Equal(t, 3, entry.Version)
}

func TestHTTPConfigGetter_GetEntry_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, testTenant.String(), r.Header.Get("X-Tenant-ID"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"code": "ERR_CONFIG_REPO_NOT_FOUND", "message": "key not found"},
		})
	}))
	defer srv.Close()

	ring := newTestRing(t)
	client := NewHTTPConfigGetter(transportTo(srv.URL, srv.Client()), ring, clock.Real())
	_, err := client.GetEntry(context.Background(), testTenant, "missing.key")
	errcodetest.AssertCode(t, err, errcode.ErrConfigRepoNotFound)
}

func TestHTTPConfigGetter_GetEntry_SensitiveEntry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, testTenant.String(), r.Header.Get("X-Tenant-ID"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"id":        "cfg-2",
				"key":       "db.password",
				"value":     "s3cret!",
				"sensitive": true,
				"version":   1,
				"createdAt": "2024-01-01T00:00:00Z",
				"updatedAt": "2024-01-01T00:00:00Z",
			},
		})
	}))
	defer srv.Close()

	ring := newTestRing(t)
	client := NewHTTPConfigGetter(transportTo(srv.URL, srv.Client()), ring, clock.Real())
	entry, err := client.GetEntry(context.Background(), testTenant, "db.password")
	require.NoError(t, err)
	assert.Equal(t, "db.password", entry.Key)
	assert.True(t, entry.Sensitive)
}

func TestHTTPConfigGetter_GetEntry_UnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ring := newTestRing(t)
	client := NewHTTPConfigGetter(transportTo(srv.URL, srv.Client()), ring, clock.Real())
	_, err := client.GetEntry(context.Background(), testTenant, "any.key")
	require.Error(t, err)
	// 5xx → transient ErrServiceUnavailable; the status + key live on the
	// server-only Internal channel (not the wire/message).
	errcodetest.AssertCode(t, err, errcode.ErrServiceUnavailable)
}

func TestHTTPConfigGetter_GetEntry_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	ring := newTestRing(t)
	client := NewHTTPConfigGetter(transportTo(srv.URL, srv.Client()), ring, clock.Real())
	_, err := client.GetEntry(context.Background(), testTenant, "some.key")
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthUnauthorized, ec.Code)
}

func TestHTTPConfigGetter_GetEntry_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	ring := newTestRing(t)
	client := NewHTTPConfigGetter(transportTo(srv.URL, srv.Client()), ring, clock.Real())
	_, err := client.GetEntry(context.Background(), testTenant, "some.key")
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthForbidden, ec.Code)
}

// TestHTTPConfigGetter_GetEntry_BadRequest_400 asserts that a 400 response from
// configcore (absent, malformed, or nil-UUID X-Tenant-ID) is returned as a
// permanent ErrValidationFailed errcode so callers can Reject instead of Requeue.
func TestHTTPConfigGetter_GetEntry_BadRequest_400(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	ring := newTestRing(t)
	client := NewHTTPConfigGetter(transportTo(srv.URL, srv.Client()), ring, clock.Real())
	_, err := client.GetEntry(context.Background(), testTenant, "some.key")
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec, "400 response must return *errcode.Error")
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Equal(t, errcode.KindInvalid, ec.Kind)
}

func TestNewHTTPConfigGetter_Constructor(t *testing.T) {
	ring := newTestRing(t)
	// A zero-value transport double suffices — this test only checks construction
	// + interface satisfaction; GetEntry (which would dispatch) is not called.
	g := NewHTTPConfigGetter(httpTestTransport{}, ring, clock.Real())
	require.NotNil(t, g)
	var _ ports.ConfigGetter = g
}

// TestNewHTTPConfigGetter_NilDeps_FailFast asserts the constructor rejects a
// nil/typed-nil transport and a nil keyring at construction (programmer/wiring
// error), rather than deferring the failure to the first request.
func TestNewHTTPConfigGetter_NilDeps_FailFast(t *testing.T) {
	ring := newTestRing(t)
	assert.Panics(t, func() { NewHTTPConfigGetter(nil, ring, clock.Real()) },
		"nil CellTransport must fail-fast at construction")
	assert.Panics(t, func() { NewHTTPConfigGetter((*transport.InProcessTransport)(nil), ring, clock.Real()) },
		"typed-nil CellTransport must fail-fast at construction")
	assert.Panics(t, func() { NewHTTPConfigGetter(httpTestTransport{}, nil, clock.Real()) },
		"nil HMACKeyRing must fail-fast at construction")
}

// TestHTTPConfigGetter_GetEntry_BoundsContext asserts GetEntry bounds the
// dispatch with a deadline before calling the transport, so a long-lived caller
// ctx cannot let a configcore call hang (F3 — restored 5s bound).
func TestHTTPConfigGetter_GetEntry_BoundsContext(t *testing.T) {
	cap := &ctxCapturingTransport{}
	client := NewHTTPConfigGetter(cap, newTestRing(t), clock.Real())
	_, err := client.GetEntry(context.Background(), testTenant, "any.key")
	require.NoError(t, err)
	require.NotNil(t, cap.ctx, "transport must have been called")
	_, ok := cap.ctx.Deadline()
	assert.True(t, ok, "GetEntry must bound the dispatch ctx with a deadline (no unbounded hang)")
}

// ctxCapturingTransport is a CellTransport double recording the ctx it received,
// answering 200 with an empty data envelope.
type ctxCapturingTransport struct{ ctx context.Context }

func (c *ctxCapturingTransport) DoContract(ctx context.Context, _ string, _ *http.Request) (*http.Response, error) {
	c.ctx = ctx
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":{"key":"k","value":"v","sensitive":false,"version":1}}`)),
	}, nil
}

// TestHTTPConfigGetter_GetEntry_BadResponseBody covers the json decode error path.
func TestHTTPConfigGetter_GetEntry_BadResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json-at-all"))
	}))
	defer srv.Close()

	ring := newTestRing(t)
	client := NewHTTPConfigGetter(transportTo(srv.URL, srv.Client()), ring, clock.Real())
	_, err := client.GetEntry(context.Background(), testTenant, "any.key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode response")
}

// TestHTTPConfigGetter_GetEntry_TenantIDForwarded asserts that GetEntry sets
// the X-Tenant-ID header to the string representation of the tenant.TenantID
// argument, and that two distinct tenants produce two distinct header values.
func TestHTTPConfigGetter_GetEntry_TenantIDForwarded(t *testing.T) {
	var receivedTenant string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedTenant = r.Header.Get("X-Tenant-ID")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"key": "k", "value": "v", "sensitive": false, "version": 1,
			},
		})
	}))
	defer srv.Close()

	ring := newTestRing(t)
	client := NewHTTPConfigGetter(transportTo(srv.URL, srv.Client()), ring, clock.Real())

	tid1 := mustParseTenant("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	_, err := client.GetEntry(context.Background(), tid1, "k")
	require.NoError(t, err)
	assert.Equal(t, tid1.String(), receivedTenant, "X-Tenant-ID must match the tenant argument")

	tid2 := mustParseTenant("11111111-2222-3333-4444-555555555555")
	_, err = client.GetEntry(context.Background(), tid2, "k")
	require.NoError(t, err)
	assert.Equal(t, tid2.String(), receivedTenant, "X-Tenant-ID must update with each call")
}

// configEntryHandler returns an http.Handler that answers the internal config
// GET with a fixed 200 {data:...} envelope. It is the downstream handler the
// service-token middleware guards in the passthrough tests below.
func configEntryHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"key": "app.name", "value": "gocell", "sensitive": false, "version": 3,
			},
		})
	})
}

// TestHTTPConfigGetter_GetEntry_MiddlewareVerified drives the production
// GetEntry call site through the real auth.ServiceTokenMiddleware (same ring +
// replay-safe nonce store), proving the signed service token and the X-Tenant-ID
// wire header this PR binds into the MAC actually pass verification end to end —
// not just that the headers are non-empty. The middleware reconstructs the MAC
// from the live X-Tenant-ID header (#1717 SignedHeaders binding); a 200 here
// means signed tenant == wire tenant survives the full verify path.
func TestHTTPConfigGetter_GetEntry_MiddlewareVerified(t *testing.T) {
	ring := newTestRing(t)
	ns, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clock.Real())
	require.NoError(t, err)

	guarded := auth.ServiceTokenMiddleware(ring, clock.Real(),
		auth.WithServiceTokenNonceStore(ns))(configEntryHandler())
	srv := httptest.NewServer(guarded)
	defer srv.Close()

	client := NewHTTPConfigGetter(transportTo(srv.URL, srv.Client()), ring, clock.Real())
	entry, err := client.GetEntry(context.Background(), testTenant, "app.name")
	require.NoError(t, err, "signed token + matching X-Tenant-ID must pass middleware verification")
	assert.Equal(t, "app.name", entry.Key)
	assert.Equal(t, "gocell", entry.Value)
	assert.Equal(t, 3, entry.Version)
}

// tamperTenantTransport rewrites the X-Tenant-ID header after GetEntry has signed
// the request, simulating a wire-level man-in-the-middle altering the tenant
// assertion while leaving the (already computed) Authorization MAC untouched.
type tamperTenantTransport struct {
	inner  http.RoundTripper
	tenant string
}

func (tt tamperTenantTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set(auth.HeaderTenantID, tt.tenant)
	return tt.inner.RoundTrip(req)
}

// TestHTTPConfigGetter_GetEntry_TamperedTenantHeaderRejected is the negative leg
// of the binding proof at the production call site: when the on-the-wire
// X-Tenant-ID diverges from the value GetEntry signed (testTenant), the MAC the
// middleware reconstructs no longer matches, so verification fails with 401 —
// which GetEntry maps to the permanent errcode.ErrAuthUnauthorized (consumers
// Reject, never Requeue). The guarded handler must never be reached.
func TestHTTPConfigGetter_GetEntry_TamperedTenantHeaderRejected(t *testing.T) {
	ring := newTestRing(t)
	ns, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clock.Real())
	require.NoError(t, err)

	var handlerReached bool
	guarded := auth.ServiceTokenMiddleware(ring, clock.Real(),
		auth.WithServiceTokenNonceStore(ns))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerReached = true
		w.WriteHeader(http.StatusOK)
	}))
	srv := httptest.NewServer(guarded)
	defer srv.Close()

	// Tamper the wire tenant to a different canonical UUID than the signed one.
	tamper := mustParseTenant("99999999-9999-4999-8999-999999999999")
	tampered := &http.Client{Transport: tamperTenantTransport{inner: http.DefaultTransport, tenant: tamper.String()}}

	client := NewHTTPConfigGetter(transportTo(srv.URL, tampered), ring, clock.Real())
	_, err = client.GetEntry(context.Background(), testTenant, "app.name")
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec, "wire-tampered tenant must surface a typed errcode")
	assert.Equal(t, errcode.ErrAuthUnauthorized, ec.Code,
		"tampered X-Tenant-ID must break the MAC binding → 401 → permanent auth error")
	assert.False(t, handlerReached, "middleware must reject before the guarded handler runs")
}
