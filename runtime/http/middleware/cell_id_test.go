package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
)

func TestWithCellIDContext_InjectsCellIDIntoCtx(t *testing.T) {
	var observed string
	handler := WithCellIDContext("accesscore")(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		observed = ctxkeys.MustCellIDFrom(r.Context())
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))

	assert.Equal(t, "accesscore", observed)
}

func TestWithCellIDContext_GroupOverridesRoot(t *testing.T) {
	// Listener-root middleware (_runtime sentinel) wraps the route-group
	// middleware (cell ID), which wraps the handler. The handler must
	// observe the route-group cell ID — the inner WithValue overrides the
	// outer one for the same key.
	var observed string
	final := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		observed = ctxkeys.MustCellIDFrom(r.Context())
	})
	chain := WithCellIDContext("_runtime")(WithCellIDContext("accesscore")(final))

	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil))

	assert.Equal(t, "accesscore", observed)
}

func TestWithCellIDContext_RuntimeSentinelReachesUnmatchedHandler(t *testing.T) {
	// When only the listener-root layer is installed (no RouteGroup matched),
	// the handler still observes a non-empty cell ID — the framework sentinel.
	var observed string
	handler := WithCellIDContext("_runtime")(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		observed = ctxkeys.MustCellIDFrom(r.Context())
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	assert.Equal(t, "_runtime", observed)
}

func TestWithCellIDContext_EmptyCellIDInjectsEmptyValue(t *testing.T) {
	// WithCellIDContext does not validate cellID — both call sites
	// (router.go literal "_runtime" and bootstrap.mountOneRouteGroup
	// guarded by rg.CellID != "") already enforce non-empty. The
	// downstream consumer middleware.Metrics calls MustCellIDFrom and
	// panics on empty value — that is the single fail-fast point. This
	// test pins the lightweight constructor contract: empty in, empty
	// out, no early panic.
	var observed string
	final := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		v, _ := ctxkeys.CellIDFrom(r.Context())
		observed = v
	})
	chain := WithCellIDContext("")(final)
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	assert.Equal(t, "", observed)
}
