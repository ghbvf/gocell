//go:build integration

package integration

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestJTypedEnvelopeRoundtrip5xxKindNormalize implements
// journeys/J-typed-envelope-roundtrip.yaml passCriteria
// "5xx 路径 errcode.Kind 归一化为匹配 status 的 Kind，杜绝 4xx Kind 透传导致
// Details 不 strip" — checkRef
// journey.J-typed-envelope-roundtrip.5xx-kind-normalize.
//
// The scenario: a service mistakenly builds an Xxx5xxErrorResponse typed struct
// but fills the body with a 4xx-Kind errcode (e.g. KindNotFound). If the wire
// status is 500 but the Kind remains KindNotFound (IsClient()==true), then
// MarshalJSON would NOT strip Details — violating the v1 schema "5xx details=[]"
// invariant and potentially leaking PII.
//
// httputil.WriteErrorWithStatus closes this gap: it re-derives Kind from the
// typed-envelope status rather than from ecErr.Kind, normalizing to
// KindInternal for generic 5xx, KindUnavailable for 503, and
// KindDeadlineExceeded for 504. The test drives WriteErrorWithStatus with
// status=500 and a 4xx-Kind errcode body to assert the normalization.
//
// Layer seam: pkg/httputil.WriteErrorWithStatus is public and importable
// from tests/integration without any cells/ boundary crossing.
//
// Docker-free: pure in-process pkg/ call.
func TestJTypedEnvelopeRoundtrip5xxKindNormalize(t *testing.T) {
	t.Parallel()

	// Construct a 4xx-Kind errcode WITH Details — simulating a service that
	// accidentally builds a 5xx typed response from a domain NotFound error
	// without re-wrapping the Kind, while still emitting runtime Details.
	//
	// Without Details, Error.MarshalJSON's wire envelope renders details=[]
	// unconditionally and the Empty(env.Error.Details) assertion below cannot
	// distinguish "Kind normalized → strip applied" from "no details to begin
	// with". Embedding a sensitive `dsn=...` attr forces the strip path to be
	// the only thing keeping it off the wire — if WriteErrorWithStatus stops
	// re-deriving Kind from status, IsClient() stays true (KindNotFound) and
	// MarshalJSON leaks the dsn through, failing this test.
	badKindErr := errcode.New(
		errcode.KindNotFound, // 4xx Kind but wire status will be 500/503/504
		errcode.ErrInternal,
		"internal server error",
		errcode.WithDetails(
			slog.String("dsn", "postgres://user:pwd@host:5432/db"),
			slog.String("query_id", "q-abc-123"),
		),
	)

	// 500 wire status + 4xx Kind errcode body — normalization must produce
	// details=[] and ERR_INTERNAL code.
	w := callWriteErrorWithStatus(http.StatusInternalServerError, badKindErr)

	assertHTTPStatus(t, w, http.StatusInternalServerError)
	env := mustDecodeWireError(t, w)

	assert.Emptyf(t, env.Error.Details,
		"WriteErrorWithStatus must normalize Kind to KindInternal for 5xx wire "+
			"status even when ecErr.Kind is 4xx (IsClient==true); "+
			"Details must be stripped on the 5xx output path. "+
			"got %d detail(s), code=%q",
		len(env.Error.Details), env.Error.Code)

	assert.Equal(t, "ERR_INTERNAL", env.Error.Code,
		"Kind normalization must also replace the wire code with the public "+
			"5xx sentinel, not the original 4xx code")

	// 503 path: KindUnavailable normalization.
	t.Run("503-normalize", func(t *testing.T) {
		t.Parallel()
		w503 := callWriteErrorWithStatus(http.StatusServiceUnavailable, badKindErr)
		assertHTTPStatus(t, w503, http.StatusServiceUnavailable)
		env503 := mustDecodeWireError(t, w503)
		assert.Empty(t, env503.Error.Details,
			"503 path must also strip Details via Kind normalization")
		assert.Equal(t, "ERR_SERVICE_UNAVAILABLE", env503.Error.Code,
			"503 normalizes to ERR_SERVICE_UNAVAILABLE, not ERR_INTERNAL")
	})

	// 504 path: KindDeadlineExceeded normalization.
	t.Run("504-normalize", func(t *testing.T) {
		t.Parallel()
		w504 := callWriteErrorWithStatus(http.StatusGatewayTimeout, badKindErr)
		assertHTTPStatus(t, w504, http.StatusGatewayTimeout)
		env504 := mustDecodeWireError(t, w504)
		assert.Empty(t, env504.Error.Details,
			"504 path must also strip Details via Kind normalization")
		assert.Equal(t, "ERR_SERVER_TIMEOUT", env504.Error.Code,
			"504 normalizes to ERR_SERVER_TIMEOUT "+
				"(errcode.PublicCodeForStatus → errcode.ErrServerTimeout), "+
				"not ERR_INTERNAL")
	})
}
