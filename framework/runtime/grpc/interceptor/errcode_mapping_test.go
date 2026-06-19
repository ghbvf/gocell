package interceptor

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

func TestToGRPCCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind errcode.Kind
		want codes.Code
	}{
		{errcode.KindInternal, codes.Internal},
		{errcode.KindInvalid, codes.InvalidArgument},
		{errcode.KindUnauthenticated, codes.Unauthenticated},
		{errcode.KindPermissionDenied, codes.PermissionDenied},
		{errcode.KindNotFound, codes.NotFound},
		{errcode.KindConflict, codes.Aborted},
		{errcode.KindUnprocessable, codes.InvalidArgument},
		{errcode.KindGone, codes.NotFound},
		{errcode.KindPayloadTooLarge, codes.ResourceExhausted},
		{errcode.KindRateLimited, codes.ResourceExhausted},
		{errcode.KindClientClosed, codes.Canceled},
		{errcode.KindDeadlineExceeded, codes.DeadlineExceeded},
		{errcode.KindUnavailable, codes.Unavailable},
		{errcode.KindNotImplemented, codes.Unimplemented},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(fmt.Sprintf("kind_%d", int(tc.kind)), func(t *testing.T) {
			t.Parallel()
			got := toGRPCCode(tc.kind)
			if got != tc.want {
				t.Errorf("toGRPCCode(%d) = %v, want %v", int(tc.kind), got, tc.want)
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
