package prometheus

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/runtime/observability/metrics"
	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollector_ImplementsInterface(t *testing.T) {
	var _ metrics.Collector = (*Collector)(nil)
}

func TestNewCollector_MissingCellID(t *testing.T) {
	_, err := NewCollector(CollectorConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CellID")
}

func TestCollector_RecordRequest(t *testing.T) {
	registry := prom.NewRegistry()
	c, err := NewCollector(CollectorConfig{
		CellID:   "test-cell",
		Registry: registry,
	})
	require.NoError(t, err)

	c.RecordRequest("GET", "/api/v1/users", 200, 0.15)
	c.RecordRequest("GET", "/api/v1/users", 200, 0.25)
	c.RecordRequest("POST", "/api/v1/users", 201, 0.10)

	// Gather metrics and verify.
	families, err := registry.Gather()
	require.NoError(t, err)

	var foundCounter, foundHistogram bool
	for _, f := range families {
		switch f.GetName() {
		case "gocell_http_requests_total":
			foundCounter = true
			// Should have 2 label combinations.
			require.Len(t, f.GetMetric(), 2)
		case "gocell_http_request_duration_seconds":
			foundHistogram = true
		}
	}
	assert.True(t, foundCounter, "should have requests_total counter")
	assert.True(t, foundHistogram, "should have request_duration_seconds histogram")
}

func TestCollector_Handler(t *testing.T) {
	registry := prom.NewRegistry()
	c, err := NewCollector(CollectorConfig{
		CellID:   "test-cell",
		Registry: registry,
	})
	require.NoError(t, err)

	// Record a request so metrics are non-empty.
	c.RecordRequest("GET", "/test", 200, 0.01)

	handler := c.Handler()
	require.NotNil(t, handler)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.True(t, strings.Contains(body, "gocell_http_requests_total"),
		"response should contain requests_total metric")
	assert.True(t, strings.Contains(body, "gocell_http_request_duration_seconds"),
		"response should contain request_duration_seconds metric")
}

func TestCollector_Labels(t *testing.T) {
	registry := prom.NewRegistry()
	c, err := NewCollector(CollectorConfig{
		CellID:   "access-core",
		Registry: registry,
	})
	require.NoError(t, err)

	c.RecordRequest("GET", "/api/v1/sessions", 200, 0.05)

	families, err := registry.Gather()
	require.NoError(t, err)

	for _, f := range families {
		if f.GetName() == "gocell_http_requests_total" {
			require.Len(t, f.GetMetric(), 1)
			m := f.GetMetric()[0]
			labels := make(map[string]string)
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			assert.Equal(t, "GET", labels["method"])
			assert.Equal(t, "/api/v1/sessions", labels["route"])
			assert.Equal(t, "200", labels["status"])
			assert.Equal(t, "access-core", labels["cell"])
		}
	}
}

func TestCollectorConfig_Defaults(t *testing.T) {
	cfg := CollectorConfig{}
	cfg.defaults()

	assert.Equal(t, "gocell", cfg.Namespace)
	assert.NotNil(t, cfg.Registry)
	assert.NotEmpty(t, cfg.DurationBuckets)
}

func TestNewCollector_CustomBuckets(t *testing.T) {
	registry := prom.NewRegistry()
	buckets := []float64{0.01, 0.05, 0.1, 0.5, 1.0}
	c, err := NewCollector(CollectorConfig{
		CellID:          "test-cell",
		Registry:        registry,
		DurationBuckets: buckets,
	})
	require.NoError(t, err)
	require.NotNil(t, c)
}
