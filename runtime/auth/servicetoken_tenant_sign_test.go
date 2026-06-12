package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
)

// Canonical tenant UUIDs (lowercase, dashed) mirroring what tenant.TenantID.String()
// emits on the wire. The MAC binding folds the raw header value, so these double as
// the signed tenant and the wire header value in the table below.
const (
	tenantSignA = "11111111-1111-4111-8111-111111111111"
	tenantSignB = "22222222-2222-4222-8222-222222222222"
)

// TestServiceTokenMiddleware_TenantHeaderBinding exercises the four-state closure
// of binding X-Tenant-ID into the service-token MAC (#1717):
//   - the signed tenant and the live wire header agree → 200;
//   - any divergence (tamper / inject / strip) → MAC mismatch → 401.
//
// Spec: buildServiceTokenMessage unconditionally folds the X-Tenant-ID value
// (empty for no-tenant tokens). verifyServiceTokenPayload reconstructs it from
// r.Header.Get(HeaderTenantID); a different reconstructed value fails the MAC.
func TestServiceTokenMiddleware_TenantHeaderBinding(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name       string
		signTenant string // value passed to GenerateServiceToken (folded into MAC)
		setHeader  bool   // whether the request carries an X-Tenant-ID header
		wireHeader string // header value when setHeader is true
		wantStatus int
		wantCalled bool
	}{
		{
			name:       "matching tenant accepted",
			signTenant: tenantSignA,
			setHeader:  true,
			wireHeader: tenantSignA,
			wantStatus: http.StatusOK,
			wantCalled: true,
		},
		{
			name:       "no tenant, no header accepted",
			signTenant: "",
			setHeader:  false,
			wantStatus: http.StatusOK,
			wantCalled: true,
		},
		{
			name:       "no tenant, empty header accepted",
			signTenant: "",
			setHeader:  true,
			wireHeader: "",
			wantStatus: http.StatusOK,
			wantCalled: true,
		},
		{
			name:       "tampered tenant rejected",
			signTenant: tenantSignA,
			setHeader:  true,
			wireHeader: tenantSignB,
			wantStatus: http.StatusUnauthorized,
			wantCalled: false,
		},
		{
			name:       "injected tenant rejected",
			signTenant: "",
			setHeader:  true,
			wireHeader: tenantSignB,
			wantStatus: http.StatusUnauthorized,
			wantCalled: false,
		},
		{
			name:       "stripped tenant rejected",
			signTenant: tenantSignA,
			setHeader:  false,
			wantStatus: http.StatusUnauthorized,
			wantCalled: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ring := mustTestRing(t, testHMACKey, "")

			token := GenerateServiceToken(ring, "accesscore", http.MethodGet, "/internal/v1/resource", "", tc.signTenant, now)
			require.NotEmpty(t, token, "token generation must succeed")

			var called bool
			handler := ServiceTokenMiddleware(
				ring, clockmock.New(now),
				WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
			)(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					called = true
					w.WriteHeader(http.StatusOK)
				}),
			)

			req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
			req.Header.Set("Authorization", "ServiceToken "+token)
			if tc.setHeader {
				req.Header.Set(HeaderTenantID, tc.wireHeader)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code, "status code")
			assert.Equal(t, tc.wantCalled, called, "handler invocation")
		})
	}
}

// TestBuildServiceTokenMessage_TenantSegment_Golden freezes the canonical MAC
// message format including the unconditional x-tenant-id segment. Any drift in
// the MAC material (dropping, renaming, reordering the tenant segment) shows up
// as a byte-level diff here — the static-guard leg of the #1717 closure.
//
// INVARIANT: X-Tenant-ID is unconditionally part of the service-token MAC
// material; the segment is the lowercased canonical "x-tenant-id=<value>".
func TestBuildServiceTokenMessage_TenantSegment_Golden(t *testing.T) {
	const (
		ts    = "1700000000"
		nonce = "abcdef0123456789abcdef0123456789"
	)

	cases := []struct {
		name       string
		method     string
		path       string
		rawQuery   string
		callerCell string
		tenantID   string
		want       string
	}{
		{
			name:       "with tenant, no query",
			method:     http.MethodGet,
			path:       "/internal/v1/resource",
			rawQuery:   "",
			callerCell: "accesscore",
			tenantID:   tenantSignA,
			want: "GET /internal/v1/resource 1700000000 abcdef0123456789abcdef0123456789 accesscore " +
				"x-tenant-id=11111111-1111-4111-8111-111111111111",
		},
		{
			name:       "no tenant, no query",
			method:     http.MethodGet,
			path:       "/internal/v1/resource",
			rawQuery:   "",
			callerCell: "accesscore",
			tenantID:   "",
			want:       "GET /internal/v1/resource 1700000000 abcdef0123456789abcdef0123456789 accesscore x-tenant-id=",
		},
		{
			name:       "with tenant and canonicalized query",
			method:     http.MethodGet,
			path:       "/api",
			rawQuery:   "b=2&a=1",
			callerCell: "accesscore",
			tenantID:   tenantSignA,
			want:       "GET /api?a=1&b=2 1700000000 abcdef0123456789abcdef0123456789 accesscore x-tenant-id=11111111-1111-4111-8111-111111111111",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildServiceTokenMessage(tc.method, tc.path, tc.rawQuery, ts, nonce, tc.callerCell, tc.tenantID)
			assert.Equal(t, tc.want, got, "canonical MAC message must match golden")
		})
	}
}
