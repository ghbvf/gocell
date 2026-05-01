package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithMetricsTokenGuard(t *testing.T) {
	body := []byte("metrics-body")
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	})

	cases := []struct {
		name           string
		configured     string
		submittedHdr   string // "" = header not set; non-empty value sent as X-Metrics-Token
		setHeader      bool
		wantStatus     int
		wantBodyPrefix string
	}{
		{
			name:           "matching token allows through",
			configured:     "secret-token",
			submittedHdr:   "secret-token",
			setHeader:      true,
			wantStatus:     http.StatusOK,
			wantBodyPrefix: "metrics-body",
		},
		{
			name:           "wrong token rejected",
			configured:     "secret-token",
			submittedHdr:   "wrong-token",
			setHeader:      true,
			wantStatus:     http.StatusUnauthorized,
			wantBodyPrefix: "unauthorized",
		},
		{
			name:           "missing header rejected",
			configured:     "secret-token",
			setHeader:      false,
			wantStatus:     http.StatusUnauthorized,
			wantBodyPrefix: "unauthorized",
		},
		{
			name:           "different length tokens rejected without leaking length",
			configured:     "long-configured-token",
			submittedHdr:   "x",
			setHeader:      true,
			wantStatus:     http.StatusUnauthorized,
			wantBodyPrefix: "unauthorized",
		},
		{
			name: "empty configured + missing header — both hash to sha256(\"\"); allowed by " +
				"design (caller responsibility to fail-fast when token unset; see " +
				"buildMetricsHandler which logs warning and skips guard entirely)",
			configured:     "",
			setHeader:      false,
			wantStatus:     http.StatusOK,
			wantBodyPrefix: "metrics-body",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			guard := withMetricsTokenGuard(tc.configured, inner)
			req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
			if tc.setHeader {
				req.Header.Set(metricsAuthHeader, tc.submittedHdr)
			}
			rec := httptest.NewRecorder()
			guard.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Contains(t, rec.Body.String(), tc.wantBodyPrefix)
		})
	}
}

func TestBuildPromStack_Success(t *testing.T) {
	// buildPromStack uses an isolated registry (prom.NewRegistry, not the
	// global default), so this test does not need to clean up between runs.
	stack, err := buildPromStack()
	require.NoError(t, err)
	require.NotNil(t, stack.registry)
	require.NotNil(t, stack.hookObserver)
	require.NotNil(t, stack.metricProvider)
}

func TestBuildPromStack_ProducesIndependentRegistries(t *testing.T) {
	// Calling buildPromStack twice must yield isolated registries; otherwise
	// the second call would observe duplicate-collector errors when the
	// hookObserver re-registers its built-in collectors against the same
	// registry instance.
	first, err := buildPromStack()
	require.NoError(t, err)
	second, err := buildPromStack()
	require.NoError(t, err)
	assert.NotSame(t, first.registry, second.registry)
}
