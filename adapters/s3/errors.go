package s3

import (
	"context"
	"errors"
	"net"

	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// retryableS3ErrorCodes mirrors aws-sdk-go-v2 retry.DefaultRetryableErrorCodes
// + DefaultThrottleErrorCodes (plus S3 InternalError / ServiceUnavailable).
// These codes are retry-safe regardless of HTTP status — notably
// RequestTimeout / RequestTimeoutException arrive as HTTP 400, which the
// status-only check would mis-classify as permanent.
//
// ref: aws/aws-sdk-go-v2 aws/retry/standard.go DefaultRetryableErrorCodes /
// DefaultThrottleErrorCodes.
var retryableS3ErrorCodes = map[string]struct{}{
	"RequestTimeout":                         {},
	"RequestTimeoutException":                {},
	"InternalError":                          {},
	"ServiceUnavailable":                     {},
	"SlowDown":                               {},
	"Throttling":                             {},
	"ThrottlingException":                    {},
	"ThrottledException":                     {},
	"RequestThrottled":                       {},
	"RequestThrottledException":              {},
	"TooManyRequestsException":               {},
	"RequestLimitExceeded":                   {},
	"BandwidthLimitExceeded":                 {},
	"LimitExceededException":                 {},
	"PriorRequestNotComplete":                {},
	"ProvisionedThroughputExceededException": {},
}

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
//  3. smithyhttp.ResponseError with HTTP status → transient if status is
//     retryable; otherwise fall through (status alone is not authoritative).
//     3b. smithy.APIError with a retryable/throttle ErrorCode → transient
//     (RequestTimeout / SlowDown / Throttling… — retry-safe regardless of
//     HTTP status; RequestTimeout is HTTP 400).
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
		if isTransientHTTPStatus(respErr.HTTPStatusCode()) {
			return true
		}
		// fall through to the API error-code check below: some retryable
		// codes (RequestTimeout) arrive with a non-retryable HTTP status.
	}

	// 3b. AWS SDK API error code — retryable/throttle codes are retry-safe
	// independent of HTTP status (RequestTimeout is HTTP 400).
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		if _, ok := retryableS3ErrorCodes[apiErr.ErrorCode()]; ok {
			return true
		}
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
