package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/stretchr/testify/assert"
)

func TestRealIP(t *testing.T) {
	tests := []struct {
		name       string
		xff        string
		xri        string
		remoteAddr string
		wantIP     string
	}{
		{
			name:       "from X-Forwarded-For single",
			xff:        "10.0.0.1",
			remoteAddr: "192.168.1.1:12345",
			wantIP:     "10.0.0.1",
		},
		{
			name:       "from X-Forwarded-For chain (first entry)",
			xff:        "10.0.0.1, 172.16.0.1, 192.168.1.1",
			remoteAddr: "192.168.1.1:12345",
			wantIP:     "10.0.0.1",
		},
		{
			name:       "from X-Real-Ip when no XFF",
			xri:        "10.0.0.2",
			remoteAddr: "192.168.1.1:12345",
			wantIP:     "10.0.0.2",
		},
		{
			name:       "XFF takes precedence over X-Real-Ip",
			xff:        "10.0.0.1",
			xri:        "10.0.0.2",
			remoteAddr: "192.168.1.1:12345",
			wantIP:     "10.0.0.1",
		},
		{
			name:       "fallback to RemoteAddr host",
			remoteAddr: "192.168.1.1:12345",
			wantIP:     "192.168.1.1",
		},
		{
			name:       "fallback to RemoteAddr without port",
			remoteAddr: "192.168.1.1",
			wantIP:     "192.168.1.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotIP string
			handler := RealIP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ip, ok := ctxkeys.RealIPFrom(r.Context())
				assert.True(t, ok)
				gotIP = ip
			}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.xri != "" {
				req.Header.Set("X-Real-Ip", tt.xri)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantIP, gotIP)
		})
	}
}
