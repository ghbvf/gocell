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
		name           string
		trustedProxies []string
		xff            string
		xri            string
		remoteAddr     string
		wantIP         string
	}{
		{
			name:           "trusted proxy: XFF single",
			trustedProxies: []string{"192.168.1.1"},
			xff:            "10.0.0.1",
			remoteAddr:     "192.168.1.1:12345",
			wantIP:         "10.0.0.1",
		},
		{
			name:           "trusted proxy: XFF chain (first entry)",
			trustedProxies: []string{"192.168.1.1"},
			xff:            "10.0.0.1, 172.16.0.1, 192.168.1.1",
			remoteAddr:     "192.168.1.1:12345",
			wantIP:         "10.0.0.1",
		},
		{
			name:           "trusted proxy: X-Real-Ip when no XFF",
			trustedProxies: []string{"192.168.1.1"},
			xri:            "10.0.0.2",
			remoteAddr:     "192.168.1.1:12345",
			wantIP:         "10.0.0.2",
		},
		{
			name:           "trusted proxy: XFF takes precedence over X-Real-Ip",
			trustedProxies: []string{"192.168.1.1"},
			xff:            "10.0.0.1",
			xri:            "10.0.0.2",
			remoteAddr:     "192.168.1.1:12345",
			wantIP:         "10.0.0.1",
		},
		{
			name:           "trusted proxy: fallback to RemoteAddr when no headers",
			trustedProxies: []string{"192.168.1.1"},
			remoteAddr:     "192.168.1.1:12345",
			wantIP:         "192.168.1.1",
		},
		{
			name:       "untrusted proxy: XFF ignored, use RemoteAddr",
			xff:        "10.0.0.1",
			remoteAddr: "192.168.1.1:12345",
			wantIP:     "192.168.1.1",
		},
		{
			name:       "untrusted proxy: X-Real-Ip ignored, use RemoteAddr",
			xri:        "10.0.0.2",
			remoteAddr: "192.168.1.1:12345",
			wantIP:     "192.168.1.1",
		},
		{
			name:           "untrusted peer despite trusted list: XFF ignored",
			trustedProxies: []string{"10.10.10.10"},
			xff:            "10.0.0.1",
			remoteAddr:     "192.168.1.1:12345",
			wantIP:         "192.168.1.1",
		},
		{
			name:       "no trusted proxies: fallback to RemoteAddr host",
			remoteAddr: "192.168.1.1:12345",
			wantIP:     "192.168.1.1",
		},
		{
			name:       "no trusted proxies: RemoteAddr without port",
			remoteAddr: "192.168.1.1",
			wantIP:     "192.168.1.1",
		},
		{
			name:       "nil trusted proxies (empty list): XFF ignored",
			xff:        "10.0.0.1",
			remoteAddr: "192.168.1.1:12345",
			wantIP:     "192.168.1.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotIP string
			handler := RealIP(tt.trustedProxies)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
