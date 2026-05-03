package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
)

func TestWithCellIDContext_WritesCtxkey(t *testing.T) {
	// The sub-mux WithCellIDContext middleware must update ctxkeys.CellID so
	// downstream logging/tracing observes the cell ID inside the handler.
	var observed string
	handler := WithCellIDContext("accesscore")(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		v, _ := ctxkeys.CellIDFrom(r.Context())
		observed = v
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))

	assert.Equal(t, "accesscore", observed)
}

func TestWithCellIDContext_MutatesCellIDStateForOuterMetrics(t *testing.T) {
	// Recreate the production layout: an outer middleware seeds *cellIDState
	// with RuntimeCellIDSentinel (the analog of metrics.Metrics on the
	// listener-root mux); a sub-mux WithCellIDContext mutates the same
	// pointer; the outer middleware reads the resolved value after next.
	var resolved string
	outer := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cs := withCellIDState(r.Context(), RuntimeCellIDSentinel)
			next.ServeHTTP(w, r.WithContext(ctx))
			resolved = cs.cellID
		})
	}
	final := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})
	chain := outer(WithCellIDContext("accesscore")(final))

	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))

	assert.Equal(t, "accesscore", resolved,
		"outer (root-mux) middleware must observe the cell ID written by the inner (sub-mux) layer; "+
			"this is the chi-style mutable-pointer pattern that the metrics recorder depends on")
}

func TestWithCellIDContext_NoUpstreamStateIsNop(t *testing.T) {
	// When there is no metrics layer upstream (e.g. a unit test exercises
	// WithCellIDContext alone), mutating the absent state must not panic and
	// the ctxkeys write must still happen so logging/tracing keeps working.
	var seen string
	handler := WithCellIDContext("accesscore")(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		v, _ := ctxkeys.CellIDFrom(r.Context())
		seen = v
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	assert.Equal(t, "accesscore", seen)
}

func TestCellIDStateFrom_ReturnsNilWhenUnattached(t *testing.T) {
	assert.Nil(t, cellIDStateFrom(context.Background()),
		"cellIDStateFrom must return nil when no metrics layer attached the state — "+
			"callers must handle this rather than dereference unconditionally")
}

func TestRuntimeCellIDSentinel_IsExportedConstant(t *testing.T) {
	// Pin the sentinel value: dashboards / alerts match against this literal,
	// and the archtest pulls it directly from this constant.
	assert.Equal(t, "_runtime", RuntimeCellIDSentinel)
}
