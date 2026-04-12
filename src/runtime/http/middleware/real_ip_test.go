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
		// --- Exact IP trust ---
		{
			name:           "trusted proxy: XFF single",
			trustedProxies: []string{"192.168.1.1"},
			xff:            "10.0.0.1",
			remoteAddr:     "192.168.1.1:12345",
			wantIP:         "10.0.0.1",
		},
		{
			name:           "trusted proxy: XFF chain — rightmost untrusted",
			trustedProxies: []string{"192.168.1.1"},
			xff:            "10.0.0.1, 172.16.0.1, 192.168.1.1",
			remoteAddr:     "192.168.1.1:12345",
			// Right-to-left: 192.168.1.1 trusted, 172.16.0.1 NOT trusted → return it
			wantIP: "172.16.0.1",
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

		// --- CIDR trust ---
		{
			name:           "CIDR: 10.0.0.0/8 matches",
			trustedProxies: []string{"10.0.0.0/8"},
			xff:            "203.0.113.50",
			remoteAddr:     "10.255.0.1:12345",
			wantIP:         "203.0.113.50",
		},
		{
			name:           "CIDR: 172.16.0.0/12 matches",
			trustedProxies: []string{"172.16.0.0/12"},
			xff:            "198.51.100.1",
			remoteAddr:     "172.20.5.3:443",
			wantIP:         "198.51.100.1",
		},
		{
			name:           "CIDR: no match — use RemoteAddr",
			trustedProxies: []string{"10.0.0.0/8"},
			xff:            "203.0.113.50",
			remoteAddr:     "192.168.1.1:12345",
			wantIP:         "192.168.1.1",
		},
		{
			name:           "CIDR: mixed exact and CIDR",
			trustedProxies: []string{"192.168.1.1", "10.0.0.0/8"},
			xff:            "203.0.113.50",
			remoteAddr:     "10.0.0.5:12345",
			wantIP:         "203.0.113.50",
		},
		{
			name:           "CIDR: IPv6",
			trustedProxies: []string{"fd00::/8"},
			xff:            "2001:db8::1",
			remoteAddr:     "[fd00::5]:12345",
			wantIP:         "2001:db8::1",
		},

		// --- Right-to-left XFF scanning ---
		{
			name:           "right-to-left: first untrusted from right",
			trustedProxies: []string{"10.0.0.0/8", "192.168.1.1"},
			xff:            "203.0.113.50, 10.0.0.1, 10.0.0.2",
			remoteAddr:     "192.168.1.1:12345",
			wantIP:         "203.0.113.50",
		},
		{
			name:           "right-to-left: all trusted except client",
			trustedProxies: []string{"10.0.0.0/8"},
			xff:            "198.51.100.1, 10.0.0.1, 10.0.0.2",
			remoteAddr:     "10.0.0.3:12345",
			wantIP:         "198.51.100.1",
		},
		{
			name:           "right-to-left: all entries trusted (spoof attempt) — return leftmost",
			trustedProxies: []string{"10.0.0.0/8"},
			xff:            "10.0.0.100, 10.0.0.1",
			remoteAddr:     "10.0.0.3:12345",
			wantIP:         "10.0.0.100",
		},
		{
			name:           "right-to-left: single entry XFF",
			trustedProxies: []string{"10.0.0.0/8"},
			xff:            "203.0.113.1",
			remoteAddr:     "10.0.0.1:12345",
			wantIP:         "203.0.113.1",
		},
		{
			name:           "right-to-left: empty entries in XFF are skipped",
			trustedProxies: []string{"10.0.0.0/8"},
			xff:            "203.0.113.1, , 10.0.0.1",
			remoteAddr:     "10.0.0.3:12345",
			wantIP:         "203.0.113.1",
		},

		// --- IPv6 normalization ---
		{
			name:           "IPv6 normalization: expanded form matches compact",
			trustedProxies: []string{"::1"},
			xff:            "2001:db8::1",
			remoteAddr:     "[0:0:0:0:0:0:0:1]:12345",
			wantIP:         "2001:db8::1",
		},
		{
			name:           "invalid proxy string: warned and skipped, XFF ignored",
			trustedProxies: []string{"not-an-ip"},
			xff:            "10.0.0.1",
			remoteAddr:     "192.168.1.1:12345",
			wantIP:         "192.168.1.1",
		},

		// --- XFF token validation (F1) ---
		{
			name:           "XFF: garbage token skipped, falls back to RemoteAddr",
			trustedProxies: []string{"10.0.0.0/8"},
			xff:            "garbage-value",
			remoteAddr:     "10.0.0.1:12345",
			wantIP:         "10.0.0.1",
		},
		{
			name:           "XFF: token with port stripped",
			trustedProxies: []string{"10.0.0.0/8"},
			xff:            "203.0.113.50:8080",
			remoteAddr:     "10.0.0.1:12345",
			wantIP:         "203.0.113.50",
		},
		{
			name:           "XFF: bracketed IPv6 normalized",
			trustedProxies: []string{"10.0.0.0/8"},
			xff:            "[2001:db8::1]",
			remoteAddr:     "10.0.0.1:12345",
			wantIP:         "2001:db8::1",
		},
		{
			name:           "XFF: mixed valid and garbage, rightmost valid untrusted returned",
			trustedProxies: []string{"10.0.0.0/8"},
			xff:            "203.0.113.1, garbage, 10.0.0.2",
			remoteAddr:     "10.0.0.3:12345",
			wantIP:         "203.0.113.1",
		},
		{
			name:           "XFF: all entries garbage, falls back to RemoteAddr",
			trustedProxies: []string{"10.0.0.0/8"},
			xff:            "foo, bar, baz",
			remoteAddr:     "10.0.0.1:12345",
			wantIP:         "10.0.0.1",
		},
		{
			name:           "X-Real-Ip: garbage token, falls back to RemoteAddr",
			trustedProxies: []string{"10.0.0.0/8"},
			xri:            "not-a-valid-ip",
			remoteAddr:     "10.0.0.1:12345",
			wantIP:         "10.0.0.1",
		},
		{
			name:           "X-Real-Ip: valid IP accepted",
			trustedProxies: []string{"10.0.0.0/8"},
			xri:            "203.0.113.50",
			remoteAddr:     "10.0.0.1:12345",
			wantIP:         "203.0.113.50",
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
