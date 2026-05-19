//go:build integration

package integration

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestJTypedEnvelopeRoundtripBusiness5xxStrip implements
// journeys/J-typed-envelope-roundtrip.yaml passCriteria
// "业务 5xx 通过 typed Xxx5xxErrorResponse 返回，wire body details 数组为空
// （MarshalJSON strip）" — checkRef
// journey.J-typed-envelope-roundtrip.business-5xx-strip.
//
// Layer seam: pkg/httputil.WriteError + pkg/errcode.Error.MarshalJSON.
// The fixture has KindInternal + non-empty Details; MarshalJSON must strip
// Details when Kind.IsClient() == false (5xx path), per the v1 schema
// invariant "5xx details=[]".
//
// Docker-free: pure in-process pkg/ call.
func TestJTypedEnvelopeRoundtripBusiness5xxStrip(t *testing.T) {
	t.Parallel()

	ecErr := errcode.New(
		errcode.KindInternal,
		errcode.ErrInternal,
		"internal server error",
		errcode.WithDetails(
			slog.String("sessionId", "sess-fixture-002"),
		),
	)
	w := callWriteError(ecErr)

	assertHTTPStatus(t, w, http.StatusInternalServerError)

	env := mustDecodeWireError(t, w)
	assert.Emptyf(t, env.Error.Details,
		"5xx wire body details must be empty array after MarshalJSON strip; "+
			"got %d detail(s). Invariant: errcode.Error.MarshalJSON emits "+
			"details=[] when Kind.IsClient()==false to prevent PII leakage on "+
			"5xx responses. code=%q message=%q",
		len(env.Error.Details), env.Error.Code, env.Error.Message)

	// Code on 5xx must be the public sentinel, not the internal code.
	assert.Equal(t, "ERR_INTERNAL", env.Error.Code,
		"5xx wire code must be public sentinel ERR_INTERNAL regardless of "+
			"internal code — prevents enumeration of internal error taxonomy")
}
