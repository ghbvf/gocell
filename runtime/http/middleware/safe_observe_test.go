package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSafeObserve_PanicDoesNotPropagate(t *testing.T) {
	assert.NotPanics(t, func() {
		safeObserve(func() {
			panic("kaboom")
		})
	})
}

func TestMetrics_CollectorPanicDoesNotCrash(t *testing.T) {
	// A collector that panics in RecordRequest must not crash the request.
	handler := Recorder(Metrics(&panicCollector{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	assert.NotPanics(t, func() {
		handler.ServeHTTP(rec, req)
	})
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAccessLog_PanicDoesNotCrash(t *testing.T) {
	// Even if slog panics (e.g. custom handler), the request must complete.
	// We test via safeObserve directly since we cannot easily inject a panicking slog.
	assert.NotPanics(t, func() {
		safeObserve(func() {
			panic("slog boom")
		})
	})
}

// panicCollector implements metrics.Collector but panics on RecordRequest.
type panicCollector struct{}

func (p *panicCollector) RecordRequest(_, _ string, _ int, _ float64) {
	panic("collector panic in RecordRequest")
}
