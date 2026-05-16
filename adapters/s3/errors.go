package s3

import (
	"context"
	"errors"
	"net"

	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// S3 adapter error codes.
const (
	ErrAdapterS3Config errcode.Code = "ERR_ADAPTER_S3_CONFIG"
	ErrAdapterS3Upload errcode.Code = "ERR_ADAPTER_S3_UPLOAD"
	ErrAdapterS3Health errcode.Code = "ERR_ADAPTER_S3_HEALTH"
)

// classifyS3Error classifies an S3 SDK error as transient or permanent and
// wraps it via the appropriate errcode funnel:
//
//   - Transient (safe to retry): AWS SDK response with HTTP status in
//     {408, 429, 500, 502, 503, 504}; context.DeadlineExceeded;
//     net.Error.Timeout() == true. Routed through errcode.WrapInfra so that
//     errcode.IsTransient returns true.
//   - Permanent: all other errors (403, 404, 400, context.Canceled, plain
//     errors). Routed through errcode.Wrap with KindInternal.
//
// opCode is the caller's error code (e.g. ErrAdapterS3Upload); opMsg is
// placed in WithInternal and never surfaces on the wire.
//
// ref: aws/aws-sdk-go-v2 aws/retry RetryableHTTPStatusCode
// ref: ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01 archtest; errcode.WrapInfra funnel.
func classifyS3Error(err error, opCode errcode.Code, opMsg string) error {
	if isTransientS3Error(err) {
		return errcode.WrapInfra(opCode,
			"s3: transient error", err,
			errcode.WithInternal(opMsg))
	}
	return errcode.Wrap(errcode.KindInternal, opCode,
		"s3: operation failed", err,
		errcode.WithInternal(opMsg))
}

// isTransientS3Error reports whether err should be retried.
//
// Classification order:
//  1. errcode.IsTransient in chain → transient (respects WrapInfra marker).
//  2. Any other *errcode.Error in chain → permanent (already classified).
//  3. smithyhttp.ResponseError with HTTP status → classify by status code.
//  4. context.DeadlineExceeded → transient; context.Canceled → permanent.
//  5. net.Error.Timeout() == true → transient.
//  6. Everything else → permanent (fail-closed).
func isTransientS3Error(err error) bool {
	// 1. Already has WrapInfra transient marker.
	if errcode.IsTransient(err) {
		return true
	}

	// 2. Any other errcode.Error in chain → already classified as permanent.
	var ec *errcode.Error
	if errors.As(err, &ec) {
		return false
	}

	// 3. AWS SDK smithy HTTP response error — classify by status code.
	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) {
		return isTransientHTTPStatus(respErr.HTTPStatusCode())
	}

	// 4. Context errors: deadline is transient, canceled is permanent.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return false
	}

	// 5. Network timeout.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	// 6. Unknown error → permanent (fail-closed).
	return false
}

// isTransientHTTPStatus reports whether an HTTP status code indicates a
// condition safe to retry after back-off.
//
// ref: aws/aws-sdk-go-v2 aws/retry RetryableHTTPStatusCode
func isTransientHTTPStatus(code int) bool {
	switch code {
	case 429, 408, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}
