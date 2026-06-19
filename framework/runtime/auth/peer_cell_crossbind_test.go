package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
)

// crossBindRequest builds a request whose context carries the given peer URI SANs
// (mTLS PeerIdentity) and service-token caller cell (Principal). An empty
// certURI means "no peer identity set"; an empty callerCell means "no principal".
func crossBindRequest(t *testing.T, certURI, callerCell string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/internal/v1/config/k", nil)
	ctx := r.Context()
	if certURI != "" {
		u, err := url.Parse(certURI)
		require.NoError(t, err)
		ctx = ctxkeys.WithPeerIdentity(ctx, ctxkeys.PeerIdentity{URIs: []*url.URL{u}})
	}
	if callerCell != "" {
		ctx = WithPrincipal(ctx, &Principal{Kind: PrincipalService, CallerCellID: callerCell})
	}
	return r.WithContext(ctx)
}

func TestPeerCellCrossBindMiddleware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		certURI    string // peer cert URI SAN ("" = no peer identity in ctx)
		callerCell string // service-token caller cell ("" = no principal in ctx)
		wantStatus int
		wantNext   bool
	}{
		{
			name:       "match: cert cell == caller cell",
			certURI:    "spiffe://example.org/cell/accesscore",
			callerCell: "accesscore",
			wantStatus: http.StatusOK,
			wantNext:   true,
		},
		{
			name:       "mismatch: cert cell != caller cell -> 403",
			certURI:    "spiffe://example.org/cell/accesscore",
			callerCell: "configcore",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "mismatch: cert trust domain != expected -> 403",
			certURI:    "spiffe://other.org/cell/accesscore", // wrong trust domain, same cell name
			callerCell: "accesscore",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "no peer identity -> 401",
			certURI:    "",
			callerCell: "accesscore",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "peer cert has no cell SPIFFE id -> 403",
			certURI:    "spiffe://example.org/ns/edge/sa/wl-1",
			callerCell: "accesscore",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "no service principal -> 403",
			certURI:    "spiffe://example.org/cell/accesscore",
			callerCell: "",
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			nextCalled := false
			h := PeerCellCrossBindMiddleware("example.org")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusOK)
			}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, crossBindRequest(t, tc.certURI, tc.callerCell))
			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Equal(t, tc.wantNext, nextCalled, "next handler invocation")
		})
	}
}

// TestPeerCellCrossBindMiddleware_MultiCellWorkloadCert covers the #2297 allow-set
// model: a peer presenting a MULTI-cell workload cert is authorized by MEMBERSHIP
// — the service-token caller cell must be IN the cert's cell set (replacing the old
// single-identity Equal, which 403'd any multi-SAN cert as "ambiguous"). A caller
// that is a member passes; a non-member 403s; a cert bridging two trust domains
// 403s.
func TestPeerCellCrossBindMiddleware_MultiCellWorkloadCert(t *testing.T) {
	t.Parallel()

	multiSAN := func(t *testing.T, uris ...string) ctxkeys.PeerIdentity {
		t.Helper()
		us := make([]*url.URL, 0, len(uris))
		for _, raw := range uris {
			u, err := url.Parse(raw)
			require.NoError(t, err)
			us = append(us, u)
		}
		return ctxkeys.PeerIdentity{URIs: us}
	}

	tests := []struct {
		name       string
		uris       []string
		callerCell string
		wantStatus int
		wantNext   bool
	}{
		{
			name:       "caller is a member of the multi-cell cert -> 200",
			uris:       []string{"spiffe://example.org/cell/accesscore", "spiffe://example.org/cell/auditcore"},
			callerCell: "auditcore",
			wantStatus: http.StatusOK,
			wantNext:   true,
		},
		{
			name:       "caller is NOT a member -> 403",
			uris:       []string{"spiffe://example.org/cell/accesscore", "spiffe://example.org/cell/auditcore"},
			callerCell: "configcore",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "cert bridges two trust domains -> 403",
			uris:       []string{"spiffe://example.org/cell/accesscore", "spiffe://other.org/cell/auditcore"},
			callerCell: "accesscore",
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/internal/v1/config/k", nil)
			ctx := ctxkeys.WithPeerIdentity(r.Context(), multiSAN(t, tc.uris...))
			ctx = WithPrincipal(ctx, &Principal{Kind: PrincipalService, CallerCellID: tc.callerCell})
			r = r.WithContext(ctx)

			nextCalled := false
			h := PeerCellCrossBindMiddleware("example.org")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusOK)
			}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Equal(t, tc.wantNext, nextCalled, "next handler invocation")
		})
	}
}
