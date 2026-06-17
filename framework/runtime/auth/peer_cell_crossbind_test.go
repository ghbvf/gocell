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
			h := PeerCellCrossBindMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
