package s3

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"

	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// fakeNetError is a net.Error that reports Timeout()=true for testing the
// net.Error.Timeout() branch of classifyS3Error.
type fakeNetError struct{ timeout bool }

func (e *fakeNetError) Error() string   { return "fake net error" }
func (e *fakeNetError) Timeout() bool   { return e.timeout }
func (e *fakeNetError) Temporary() bool { return false }

var _ net.Error = (*fakeNetError)(nil)

// newSmithyResponseError builds a *smithyhttp.ResponseError with the given
// HTTP status code. This is the real smithy type — no fabrication needed
// because the struct is exported and the Response field is a *smithyhttp.Response
// (which embeds *http.Response).
func newSmithyResponseError(statusCode int) *smithyhttp.ResponseError {
	return &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{
			Response: &http.Response{StatusCode: statusCode},
		},
		Err: errors.New("s3 api error"),
	}
}

func TestClassifyS3Error(t *testing.T) {
	t.Parallel()

	const opCode errcode.Code = "ERR_ADAPTER_S3_UPLOAD"
	const opMsg = "key=test/file.bin"

	tests := []struct {
		name      string
		err       error
		wantTrans bool // true → errcode.IsTransient must be true
	}{
		// ---- transient: AWS HTTP status codes ----
		{
			name:      "smithy 503 ServiceUnavailable → transient",
			err:       newSmithyResponseError(503),
			wantTrans: true,
		},
		{
			name:      "smithy 500 InternalServerError → transient",
			err:       newSmithyResponseError(500),
			wantTrans: true,
		},
		{
			name:      "smithy 429 TooManyRequests → transient",
			err:       newSmithyResponseError(429),
			wantTrans: true,
		},
		{
			name:      "smithy 408 RequestTimeout → transient",
			err:       newSmithyResponseError(408),
			wantTrans: true,
		},
		{
			name:      "smithy 502 BadGateway → transient",
			err:       newSmithyResponseError(502),
			wantTrans: true,
		},
		{
			name:      "smithy 504 GatewayTimeout → transient",
			err:       newSmithyResponseError(504),
			wantTrans: true,
		},

		// ---- transient: context / network ----
		{
			name:      "context.DeadlineExceeded → transient",
			err:       context.DeadlineExceeded,
			wantTrans: true,
		},
		{
			name:      "net.Error Timeout=true → transient",
			err:       &fakeNetError{timeout: true},
			wantTrans: true,
		},

		// ---- permanent: AWS HTTP status codes ----
		{
			name:      "smithy 400 BadRequest → permanent",
			err:       newSmithyResponseError(400),
			wantTrans: false,
		},
		{
			name:      "smithy 403 Forbidden → permanent",
			err:       newSmithyResponseError(403),
			wantTrans: false,
		},
		{
			name:      "smithy 404 NotFound → permanent",
			err:       newSmithyResponseError(404),
			wantTrans: false,
		},

		// ---- permanent: other ----
		{
			name:      "context.Canceled → permanent (NOT transient)",
			err:       context.Canceled,
			wantTrans: false,
		},
		{
			name:      "plain error → permanent",
			err:       errors.New("something went wrong"),
			wantTrans: false,
		},
		{
			name:      "net.Error Timeout=false → permanent",
			err:       &fakeNetError{timeout: false},
			wantTrans: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := classifyS3Error(tc.err, opCode, opMsg)

			assert.NotNil(t, got, "classifyS3Error must never return nil when given non-nil err")

			if tc.wantTrans {
				assert.True(t, errcode.IsTransient(got),
					"expected transient error for %T but got non-transient: %v", tc.err, got)
			} else {
				assert.False(t, errcode.IsTransient(got),
					"expected permanent error for %T but got transient: %v", tc.err, got)
			}
		})
	}
}

func TestIsTransientHTTPStatus_S3(t *testing.T) {
	t.Parallel()

	transient := []int{429, 408, 500, 502, 503, 504}
	permanent := []int{200, 201, 301, 400, 401, 403, 404, 409, 413}

	for _, code := range transient {
		code := code
		t.Run("transient", func(t *testing.T) {
			t.Parallel()
			assert.True(t, isTransientHTTPStatus(code), "status %d should be transient", code)
		})
	}
	for _, code := range permanent {
		code := code
		t.Run("permanent", func(t *testing.T) {
			t.Parallel()
			assert.False(t, isTransientHTTPStatus(code), "status %d should be permanent", code)
		})
	}
}
