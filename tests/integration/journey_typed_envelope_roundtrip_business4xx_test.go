//go:build integration

package integration

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestJTypedEnvelopeRoundtripBusiness4xxDetails implements
// journeys/J-typed-envelope-roundtrip.yaml passCriteria
// "业务 4xx 通过 typed Xxx4xxErrorResponse 返回，wire body 含非空 details 数组"
// — checkRef journey.J-typed-envelope-roundtrip.business-4xx-details.
//
// Layer seam: we drive httputil.WriteError directly with an
// errcode.New(KindNotFound, ..., WithDetails(...)) value, which is the same
// code path that generated handlers use when the service layer returns a
// typed 4xx error. The integration seam is pkg/httputil + pkg/errcode —
// both are public packages importable from tests/integration without any
// cells/*/internal/ boundary compromise.
//
// The criterion asserts that:
//  1. HTTP status is 4xx (404).
//  2. wire body contains `"details"` array with at least one entry
//     (the WithDetails slog.Attr is visible to the client on 4xx).
//
// Docker-free: no database, no broker, no container runtime required.
func TestJTypedEnvelopeRoundtripBusiness4xxDetails(t *testing.T) {
	t.Parallel()

	ecErr := errcode.New(
		errcode.KindNotFound,
		errcode.ErrSessionNotFound,
		"session not found",
		errcode.WithDetails(
			slog.String("sessionId", "sess-fixture-001"),
		),
	)
	w := callWriteError(ecErr)

	assertHTTPStatus(t, w, http.StatusNotFound)

	env := mustDecodeWireError(t, w)
	require.NotEmptyf(t, env.Error.Details,
		"4xx wire body must contain non-empty details array; "+
			"errcode.Error.MarshalJSON must forward Details when Kind.IsClient()==true. "+
			"got code=%q message=%q",
		env.Error.Code, env.Error.Message)

	assert.Equal(t, "ERR_SESSION_NOT_FOUND", env.Error.Code,
		"wire code must match the errcode sentinel, not a public redaction")
}
