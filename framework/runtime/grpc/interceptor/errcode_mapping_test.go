package interceptor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

func TestToGRPCCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		kind errcode.Kind
		want codes.Code
	}{
		{"KindInternal", errcode.KindInternal, codes.Internal},
		{"KindInvalid", errcode.KindInvalid, codes.InvalidArgument},
		{"KindUnauthenticated", errcode.KindUnauthenticated, codes.Unauthenticated},
		{"KindPermissionDenied", errcode.KindPermissionDenied, codes.PermissionDenied},
		{"KindNotFound", errcode.KindNotFound, codes.NotFound},
		{"KindConflict", errcode.KindConflict, codes.Aborted},
		{"KindUnprocessable", errcode.KindUnprocessable, codes.InvalidArgument},
		{"KindGone", errcode.KindGone, codes.NotFound},
		{"KindPayloadTooLarge", errcode.KindPayloadTooLarge, codes.ResourceExhausted},
		{"KindRateLimited", errcode.KindRateLimited, codes.ResourceExhausted},
		{"KindClientClosed", errcode.KindClientClosed, codes.Canceled},
		{"KindDeadlineExceeded", errcode.KindDeadlineExceeded, codes.DeadlineExceeded},
		{"KindUnavailable", errcode.KindUnavailable, codes.Unavailable},
		{"KindNotImplemented", errcode.KindNotImplemented, codes.Unimplemented},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := toGRPCCode(tc.kind)
			if got != tc.want {
				t.Errorf("toGRPCCode(%v) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestToGRPCCode_DefaultFallback(t *testing.T) {
	// An unknown Kind value (e.g. future extension) must fall back to Internal.
	t.Parallel()
	unknown := errcode.Kind(999)
	if got := toGRPCCode(unknown); got != codes.Internal {
		t.Errorf("toGRPCCode(unknown) = %v, want Internal", got)
	}
}

func TestErrToStatus_Nil(t *testing.T) {
	t.Parallel()
	if got := errToStatus(nil); got != nil {
		t.Errorf("errToStatus(nil) = %v, want nil", got)
	}
}

func TestErrToStatus_ErrcodeClientError_CarriesMessage(t *testing.T) {
	// 4xx errcode: mapped to the right code AND the errcode message is forwarded.
	t.Parallel()
	err := errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "device_id is required")
	got := errToStatus(err)
	if got == nil {
		t.Fatal("errToStatus returned nil for *errcode.Error")
	}
	if code := status.Code(got); code != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", code)
	}
	// The errcode message must be present (4xx wire-safe).
	msg := status.Convert(got).Message()
	if msg == "" {
		t.Errorf("status message must not be empty for 4xx errcode")
	}
}

func TestErrToStatus_ErrcodeServerError_UsesGenericMessage(t *testing.T) {
	// 5xx errcode: message must be replaced with the generic const — no leak.
	t.Parallel()
	err := errcode.New(errcode.KindInternal, errcode.ErrInternal, "db connection pool exhausted secret-host:5432")
	got := errToStatus(err)
	if got == nil {
		t.Fatal("errToStatus returned nil for *errcode.Error")
	}
	if code := status.Code(got); code != codes.Internal {
		t.Errorf("code = %v, want Internal", code)
	}
	msg := status.Convert(got).Message()
	if msg != msgInternalServerError {
		t.Errorf("5xx message = %q, want generic %q (must not leak internal detail)", msg, msgInternalServerError)
	}
}

func TestErrToStatus_AlreadyStatus_PassThrough(t *testing.T) {
	// An error that already carries a gRPC status with a non-Unknown code must
	// pass through unchanged — e.g. from the auth or recovery interceptors.
	t.Parallel()
	original := status.Error(codes.Unauthenticated, "auth interceptor denial")
	got := errToStatus(original)
	if got == nil {
		t.Fatal("errToStatus returned nil for already-status error")
	}
	if status.Code(got) != codes.Unauthenticated {
		t.Errorf("already-status pass-through: code = %v, want Unauthenticated", status.Code(got))
	}
}

func TestErrToStatus_NonErrcodePlainError_BecomesInternal(t *testing.T) {
	// A plain errors.New error that is neither *errcode.Error nor already a status
	// must surface as codes.Internal with the generic message (fail-closed).
	t.Parallel()
	plain := errors.New("something went wrong")
	got := errToStatus(plain)
	if got == nil {
		t.Fatal("errToStatus returned nil for plain error")
	}
	if code := status.Code(got); code != codes.Internal {
		t.Errorf("plain error code = %v, want Internal", code)
	}
	msg := status.Convert(got).Message()
	if msg != msgInternalServerError {
		t.Errorf("plain error message = %q, want generic %q", msg, msgInternalServerError)
	}
}

func TestErrToStatus_5xxKindUnavailable_UsesGenericMessage(t *testing.T) {
	// KindUnavailable is server-side (5xx): message must be generic.
	t.Parallel()
	err := errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, "redis unreachable secret-host")
	got := errToStatus(err)
	if got == nil {
		t.Fatal("errToStatus returned nil")
	}
	if code := status.Code(got); code != codes.Unavailable {
		t.Errorf("code = %v, want Unavailable", code)
	}
	msg := status.Convert(got).Message()
	if msg != msgInternalServerError {
		t.Errorf("KindUnavailable message = %q, want generic %q", msg, msgInternalServerError)
	}
}

func TestErrToStatus_AlreadyStatusUnknown_RemapsAsNonErrcode(t *testing.T) {
	// A status.Error(codes.Unknown, ...) is NOT an *errcode.Error. errToStatus sees
	// ok=true from status.FromError but code==Unknown, so it falls through to the
	// errors.As(*errcode.Error) check — which fails — and maps to codes.Internal
	// (fail-closed non-errcode path).
	t.Parallel()
	unknownStatus := status.Error(codes.Unknown, "x")
	got := errToStatus(unknownStatus)
	if got == nil {
		t.Fatal("errToStatus returned nil for status.Error(codes.Unknown, ...)")
	}
	if code := status.Code(got); code != codes.Internal {
		t.Errorf("already-Unknown non-errcode status: code = %v, want Internal (fail-closed)", code)
	}
	msg := status.Convert(got).Message()
	if msg != msgInternalServerError {
		t.Errorf("already-Unknown non-errcode status: message = %q, want generic %q", msg, msgInternalServerError)
	}
}

func TestErrToStatus_ErrcodeWrappedAsUnknownStatus(t *testing.T) {
	// A plain *errcode.Error (KindInvalid) is reported as codes.Unknown by
	// status.FromError (grpc-go synthetic Unknown for non-status errors). errToStatus
	// must remap it to the correct code (InvalidArgument) via the errors.As path.
	t.Parallel()
	ec := errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "field is required")
	got := errToStatus(ec)
	if got == nil {
		t.Fatal("errToStatus returned nil for *errcode.Error")
	}
	if code := status.Code(got); code != codes.InvalidArgument {
		t.Errorf("*errcode.Error(KindInvalid) via Unknown path: code = %v, want InvalidArgument", code)
	}
}

func TestUnaryErrcodeMap_PassThrough_OnNil(t *testing.T) {
	t.Parallel()
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}
	resp, err := UnaryErrcodeMap()(context.Background(), nil, info, func(_ context.Context, _ any) (any, error) {
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("nil error must pass through: %v", err)
	}
	if resp != "ok" {
		t.Errorf("resp = %v, want ok", resp)
	}
}

func TestUnaryErrcodeMap_MapsErrcodeToGRPC(t *testing.T) {
	t.Parallel()
	ec := errcode.New(errcode.KindNotFound, errcode.ErrMetadataNotFound, "device not found")
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Op"}
	_, err := UnaryErrcodeMap()(context.Background(), nil, info, func(_ context.Context, _ any) (any, error) {
		return nil, ec
	})
	if code := status.Code(err); code != codes.NotFound {
		t.Errorf("code = %v, want NotFound", code)
	}
}

func TestStreamErrcodeMap_MapsErrcodeToGRPC(t *testing.T) {
	t.Parallel()
	ec := errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, "forbidden")
	info := &grpc.StreamServerInfo{FullMethod: "/pkg.Svc/Stream"}
	ss := &errcodeMappingFakeStream{ctx: context.Background()}
	err := StreamErrcodeMap()(nil, ss, info, func(_ any, _ grpc.ServerStream) error {
		return ec
	})
	if code := status.Code(err); code != codes.PermissionDenied {
		t.Errorf("stream code = %v, want PermissionDenied", code)
	}
}

// errcodeMappingFakeStream is a minimal grpc.ServerStream for errcode mapping tests.
type errcodeMappingFakeStream struct {
	ctx context.Context
}

func (f *errcodeMappingFakeStream) Context() context.Context       { return f.ctx }
func (f *errcodeMappingFakeStream) SendMsg(_ any) error            { return nil }
func (f *errcodeMappingFakeStream) RecvMsg(_ any) error            { return nil }
func (f *errcodeMappingFakeStream) SetHeader(_ metadata.MD) error  { return nil }
func (f *errcodeMappingFakeStream) SendHeader(_ metadata.MD) error { return nil }
func (f *errcodeMappingFakeStream) SetTrailer(_ metadata.MD)       {}

// TestErrToStatus_4xx_PublicDetails_CarriedInErrorInfo asserts that 4xx *errcode.Error
// values with PublicDetail attributes are forwarded in a google.rpc.ErrorInfo detail
// embedded in the gRPC status (#2482). Each PublicDetail type is verified; the
// rendered Metadata values must match the HTTP marshalJSONValue semantics:
//   - PublicString: raw string value (no JSON quoting).
//   - PublicInt: decimal integer string (e.g. "42").
//   - PublicBool: "true" / "false".
//   - PublicDuration: nanosecond integer string (matching json.Marshal(int64(d))).
//   - PublicTime: RFC3339Nano string, unquoted (same instant as json.Marshal(time.Time),
//     minus the structural JSON quotes — a flat map value is the string itself).
func TestErrToStatus_4xx_PublicDetails_CarriedInErrorInfo(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	dur := 5 * time.Second

	ec := errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "invalid input",
		errcode.WithDetails(
			errcode.PublicString("field", "device_id"),
			errcode.PublicInt("count", 42),
			errcode.PublicBool("active", true),
			errcode.PublicDuration("elapsed", dur),
			errcode.PublicTime("timestamp", now),
		),
	)

	got := errToStatus(ec)
	if got == nil {
		t.Fatal("errToStatus returned nil for *errcode.Error")
	}
	if code := status.Code(got); code != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", code)
	}

	st, ok := status.FromError(got)
	if !ok {
		t.Fatal("result is not a gRPC status error")
	}

	var ei *errdetails.ErrorInfo
	for _, d := range st.Details() {
		if e, ok2 := d.(*errdetails.ErrorInfo); ok2 {
			ei = e
			break
		}
	}
	if ei == nil {
		t.Fatal("no ErrorInfo detail found in 4xx status")
	}

	if ei.Domain != errcodeDomain {
		t.Errorf("ErrorInfo.Domain = %q, want %q", ei.Domain, errcodeDomain)
	}
	if ei.Reason != string(errcode.ErrValidationFailed) {
		t.Errorf("ErrorInfo.Reason = %q, want %q", ei.Reason, string(errcode.ErrValidationFailed))
	}

	wantMeta := map[string]string{
		"field":     "device_id",
		"count":     "42",
		"active":    "true",
		"elapsed":   "5000000000",
		"timestamp": "2024-01-02T03:04:05Z",
	}
	for k, want := range wantMeta {
		if got2 := ei.Metadata[k]; got2 != want {
			t.Errorf("Metadata[%q] = %q, want %q", k, got2, want)
		}
	}
}

// TestErrToStatus_5xx_PublicDetails_Stripped asserts that 5xx *errcode.Error values
// with PublicDetail attributes do NOT produce any ErrorInfo detail on the wire (#2482).
// The split-constructor design (HARD: typed function choice) makes 5xx-details carry
// structurally inexpressible — clientStatusWithDetails is only called for IsClient().
func TestErrToStatus_5xx_PublicDetails_Stripped(t *testing.T) {
	t.Parallel()

	ec := errcode.New(errcode.KindInternal, errcode.ErrInternal, "db pool exhausted secret-host:5432",
		errcode.WithDetails(errcode.PublicString("host", "db-secret:5432")),
	)

	got := errToStatus(ec)
	if got == nil {
		t.Fatal("errToStatus returned nil for *errcode.Error")
	}

	if code := status.Code(got); code != codes.Internal {
		t.Errorf("code = %v, want Internal", code)
	}
	if msg := status.Convert(got).Message(); msg != msgInternalServerError {
		t.Errorf("5xx message = %q, want %q", msg, msgInternalServerError)
	}

	st, ok := status.FromError(got)
	if !ok {
		t.Fatal("result is not a gRPC status error")
	}
	for _, d := range st.Details() {
		if _, isEI := d.(*errdetails.ErrorInfo); isEI {
			t.Error("5xx status must NOT carry ErrorInfo detail (split-constructor HARD guarantee)")
		}
	}
}

// TestErrToStatus_4xx_NoDetails_NoErrorInfo asserts that a 4xx *errcode.Error with
// no PublicDetails does not produce an empty ErrorInfo detail — the Metadata guard
// must not attach a zero-Metadata ErrorInfo.
func TestErrToStatus_4xx_NoDetails_NoErrorInfo(t *testing.T) {
	t.Parallel()

	ec := errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "bad request")

	got := errToStatus(ec)
	if got == nil {
		t.Fatal("errToStatus returned nil")
	}

	st, ok := status.FromError(got)
	if !ok {
		t.Fatal("result is not a gRPC status error")
	}
	if len(st.Details()) != 0 {
		t.Errorf("4xx with no PublicDetails: expected 0 details, got %d", len(st.Details()))
	}
}

// TestErrcodeDomain_DifferentFromDenyReasonDomain asserts that errcodeDomain and
// denyReasonDomain are distinct values, so gRPC clients can tell apart a business
// 4xx ErrorInfo from an auth/authz denial ErrorInfo without inspecting the Reason field.
func TestErrcodeDomain_DifferentFromDenyReasonDomain(t *testing.T) {
	t.Parallel()
	if errcodeDomain == denyReasonDomain {
		t.Errorf("errcodeDomain (%q) must differ from denyReasonDomain (%q)", errcodeDomain, denyReasonDomain)
	}
}

// TestRenderDetailValue verifies that renderDetailValue renders each PublicDetail
// type to its expected string representation, aligned with HTTP marshalJSONValue
// semantics (F11 direct unit test for the rendering function).
//
// Expected values:
//   - PublicString: raw string, no JSON quoting.
//   - PublicInt: decimal integer string ("42").
//   - PublicBool: "true" / "false".
//   - PublicDuration: nanosecond integer string ("5000000000" for 5s).
//   - PublicTime: RFC3339Nano string, unquoted ("2024-01-02T03:04:05Z").
func TestRenderDetailValue(t *testing.T) {
	t.Parallel()

	fixedTime := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	dur := 5 * time.Second

	cases := []struct {
		name   string
		detail errcode.PublicDetail
		want   string
	}{
		{
			name:   "PublicString_no_json_quoting",
			detail: errcode.PublicString("k", "device_id"),
			want:   "device_id",
		},
		{
			name:   "PublicInt_decimal",
			detail: errcode.PublicInt("k", 42),
			want:   "42",
		},
		{
			name:   "PublicBool_true",
			detail: errcode.PublicBool("k", true),
			want:   "true",
		},
		{
			name:   "PublicBool_false",
			detail: errcode.PublicBool("k", false),
			want:   "false",
		},
		{
			name:   "PublicDuration_nanoseconds",
			detail: errcode.PublicDuration("k", dur),
			want:   "5000000000",
		},
		{
			name:   "PublicTime_rfc3339nano_unquoted",
			detail: errcode.PublicTime("k", fixedTime),
			want:   "2024-01-02T03:04:05Z",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := renderDetailValue(tc.detail)
			if got != tc.want {
				t.Errorf("renderDetailValue(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestErrToStatus_ContextErrors asserts a handler-returned context error is mapped
// to the canonical gRPC code (Canceled / DeadlineExceeded), NOT the generic Internal
// fallback (#2479 review F1). A canceled/drained stream is a normal outcome; mapping
// it to Internal would let the outer CircuitBreaker count it as a server failure.
func TestErrToStatus_ContextErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want codes.Code
		// breakerTrips encodes the INTENDED circuit-breaker interaction:
		//   - Canceled (client cancel / graceful drain) must NOT trip the breaker —
		//     this is F1's core fix (previously these mapped to Internal and polluted
		//     breaker health).
		//   - DeadlineExceeded (the server was too slow) IS a legitimate server-health
		//     signal and DOES trip the breaker — unchanged, and correct.
		breakerTrips bool
	}{
		{"context.Canceled", context.Canceled, codes.Canceled, false},
		{"context.DeadlineExceeded", context.DeadlineExceeded, codes.DeadlineExceeded, true},
		{"wrapped Canceled", fmt.Errorf("handler: %w", context.Canceled), codes.Canceled, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := status.Code(errToStatus(tc.err))
			if got != tc.want {
				t.Errorf("errToStatus(%v) code = %v, want %v", tc.err, got, tc.want)
			}
			if isServerFailureCode(got) != tc.breakerTrips {
				t.Errorf("isServerFailureCode(%v) = %v, want %v (breaker interaction)",
					got, isServerFailureCode(got), tc.breakerTrips)
			}
		})
	}
}
