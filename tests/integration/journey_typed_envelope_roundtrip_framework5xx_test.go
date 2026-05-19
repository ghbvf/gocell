//go:build integration

package integration

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestJTypedEnvelopeRoundtripFramework5xxFallback implements
// journeys/J-typed-envelope-roundtrip.yaml passCriteria
// "framework 5xx (service 返回 nil 或 visit encode 失败) 通过
// httputil.WriteNilResponseInternal/WriteEncodeFaultInternal 兜底，wire 状态码
// 与 details=[] 一致" — checkRef
// journey.J-typed-envelope-roundtrip.framework-5xx-fallback.
//
// Layer seam: pkg/httputil.WriteNilResponseInternal and
// pkg/httputil.WriteEncodeFaultInternal — both are public helpers from
// pkg/httputil that generated handlers call on the two un-declared framework
// 5xx paths (nil response without error, response encode failure).
//
// The criterion asserts:
//  1. Both helpers emit HTTP 500.
//  2. Both emit a well-formed JSON body with details=[] (no PII).
//  3. The code is the public sentinel ERR_INTERNAL.
//
// Docker-free: pure in-process pkg/ call.
func TestJTypedEnvelopeRoundtripFramework5xxFallback(t *testing.T) {
	t.Parallel()

	t.Run("nil-response", func(t *testing.T) {
		t.Parallel()
		w := callWriteNilResponseInternal()

		assertHTTPStatus(t, w, http.StatusInternalServerError)
		env := mustDecodeWireError(t, w)
		assert.Emptyf(t, env.Error.Details,
			"WriteNilResponseInternal wire details must be empty; got %d detail(s)",
			len(env.Error.Details))
		assert.Equal(t, "ERR_INTERNAL", env.Error.Code,
			"WriteNilResponseInternal wire code must be public sentinel")
		assert.NotEmpty(t, env.Error.Message,
			"WriteNilResponseInternal must emit a non-empty message for client diagnostics")
	})

	t.Run("encode-fault", func(t *testing.T) {
		t.Parallel()
		w := callWriteEncodeFaultInternal()

		assertHTTPStatus(t, w, http.StatusInternalServerError)
		env := mustDecodeWireError(t, w)
		assert.Emptyf(t, env.Error.Details,
			"WriteEncodeFaultInternal wire details must be empty; got %d detail(s)",
			len(env.Error.Details))
		assert.Equal(t, "ERR_INTERNAL", env.Error.Code,
			"WriteEncodeFaultInternal wire code must be public sentinel")
		assert.NotEmpty(t, env.Error.Message,
			"WriteEncodeFaultInternal must emit a non-empty message for client diagnostics")
	})
}
