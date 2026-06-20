package interceptor

// errcode_mapping.go — centralized errcode.Kind → grpc/codes.Code projection
// for the GoCell gRPC interceptor chain (PR-12 #1155).
//
// pkg/errcode MUST NOT import google.golang.org/grpc (it would pollute kernel/
// transitively). The mapping therefore lives here, in runtime/grpc/interceptor,
// which is the single transport-side concern owner. This mirrors the Kratos
// pattern (ref: go-kratos/kratos errors GRPCStatus) adapted as a standalone
// interceptor because GoCell's *errcode.Error is not a proto-generated type.
//
// # 4xx / 5xx redaction discipline
//
// Client errors (errcode.Kind.IsClient() == true, 4xx) use the errcode message
// directly — it is a const literal (MESSAGE-CONST-LITERAL-01) and therefore
// wire-safe — and carry PublicDetails in a google.rpc.ErrorInfo.Metadata field
// (Domain=errcodeDomain). Server errors (5xx) use msgInternalServerError — the
// same generic message the HTTP 5xx path emits — so internal details never reach
// the wire. PublicDetails are structurally stripped from 5xx by the split-
// constructor design (typed function choice, HARD): clientStatusWithDetails is
// ONLY called for IsClient()==true; the 5xx path calls status.Error directly
// with no ErrorInfo parameter, making 5xx-details carry inexpressible in type.
//
// This mirrors framework/pkg/httputil/response.go 5xx projection.
//
// ref: go-kratos/kratos errors GRPCStatus
// ref: go-kratos/kratos transport/grpc/interceptor.go

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// msgInternalServerError is the generic server-error message emitted on the gRPC
// wire for all 5xx errcode.Errors (and any unknown non-status error). It must be
// a const literal so MESSAGE-CONST-LITERAL-01 is satisfied.
const msgInternalServerError = "internal server error"

// errcodeDomain is the google.rpc.ErrorInfo.Domain value for business 4xx errcode
// details (#2482). It is intentionally distinct from denyReasonDomain
// ("gocell.authz.grpc") so that gRPC clients can distinguish a business
// KindInvalid / KindNotFound / etc. ErrorInfo from an auth/authz denial ErrorInfo
// by keying on (Domain, Reason) — without parsing the English message.
const errcodeDomain = "gocell.errcode"

// toGRPCCode maps an errcode.Kind to the canonical grpc/codes.Code.
//
// Every Kind constant is listed as an EXPLICIT case (GRPC-ERRCODE-MAPPING-01):
// a default clause alone would silently absorb a newly-added Kind. The mapping
// table is fixed (spec-frozen in PR-12 #1155) — changes require updating both
// this switch and the archtest exhaustiveness guard.
//
// cyclop: the function has 14 explicit Kind cases. This is a pure lookup switch
// with no logic branching; the high case count is inherent to the complete Kind
// enumeration and cannot be split without defeating the GRPC-ERRCODE-MAPPING-01
// exhaustiveness requirement (archtest scans this exact switch by AST). The
// project limit (≤ 15) is deliberately exceeded here by exactly one: a 14-case
// switch scores 15 (14 cases + 1 base). This is an intentional carve-out aligned
// with the PRINCIPAL-KIND-EXHAUSTIVE-SWITCH-01 precedent.
//
//nolint:cyclop // 14-case exhaustive Kind switch required by GRPC-ERRCODE-MAPPING-01; complexity is inherent to the enum size
func toGRPCCode(k errcode.Kind) codes.Code {
	switch k {
	case errcode.KindInternal:
		return codes.Internal
	case errcode.KindInvalid:
		return codes.InvalidArgument
	case errcode.KindUnauthenticated:
		return codes.Unauthenticated
	case errcode.KindPermissionDenied:
		return codes.PermissionDenied
	case errcode.KindNotFound:
		return codes.NotFound
	case errcode.KindConflict:
		return codes.Aborted
	case errcode.KindUnprocessable:
		return codes.InvalidArgument
	case errcode.KindGone:
		return codes.NotFound
	case errcode.KindPayloadTooLarge:
		return codes.ResourceExhausted
	case errcode.KindRateLimited:
		return codes.ResourceExhausted
	case errcode.KindClientClosed:
		return codes.Canceled
	case errcode.KindDeadlineExceeded:
		return codes.DeadlineExceeded
	case errcode.KindUnavailable:
		return codes.Unavailable
	case errcode.KindNotImplemented:
		return codes.Unimplemented
	default:
		// Unknown future Kind: fail-closed as Internal.
		return codes.Internal
	}
}

// errToStatus converts a handler error into a gRPC status error:
//
//   - nil → nil (pass-through).
//   - Already-status errors whose code is not Unknown → pass through unchanged.
//     Auth and Recovery interceptors already produce a grpc status; double-mapping
//     would overwrite e.g. codes.Unauthenticated with codes.Internal.
//   - *errcode.Error: map via toGRPCCode. 4xx (IsClient) call clientStatusWithDetails
//     which carries PublicDetails in google.rpc.ErrorInfo.Metadata (Domain=errcodeDomain);
//     5xx call status.Error directly (no ErrorInfo parameter — split-constructor HARD
//     guarantee: 5xx-details carry is structurally inexpressible).
//   - Any other error → codes.Internal with msgInternalServerError (fail-closed).
//
// # 4xx PublicDetails wire parity (#2482)
//
// 4xx errcode.Errors with PublicDetails are now forwarded in a google.rpc.ErrorInfo
// detail (Domain=errcodeDomain, Reason=string(ec.Code), Metadata=key→rendered-value).
// Metadata value rendering is aligned with HTTP marshalJSONValue semantics (see
// publicDetailsToMetadata). 5xx-no-details invariant is guaranteed by the split-
// constructor pattern (typed function choice, HARD): clientStatusWithDetails only
// accepts a client error; the 5xx path has no ErrorInfo parameter at the call site.
//
// Do NOT implement GRPCStatus() on *errcode.Error: that would bypass this
// interceptor's 5xx redaction (5xx messages would reach the wire unredacted).
func errToStatus(err error) error {
	if err == nil {
		return nil
	}
	// Pass through already-status errors whose code is not Unknown.
	// status.FromError returns ok=true for any status-carrying error; Unknown is
	// the synthetic code grpc-go assigns to non-status errors, which is exactly
	// the *errcode.Error case we want to remap.
	if s, ok := status.FromError(err); ok {
		if s.Code() != codes.Unknown {
			return err
		}
	}
	// *errcode.Error: apply Kind→codes mapping with 4xx/5xx split-constructor.
	var ec *errcode.Error
	if errors.As(err, &ec) {
		code := toGRPCCode(ec.Kind)
		if ec.Kind.IsClient() {
			return clientStatusWithDetails(code, ec) // 4xx: carry PublicDetails in ErrorInfo
		}
		// 5xx: message-only, no ErrorInfo parameter (HARD: inexpressible to attach details here).
		return status.Error(code, msgInternalServerError)
	}
	// Context cancellation / deadline are NORMAL outcomes (client cancel, graceful
	// drain via StreamDrain, per-RPC timeout) — NOT server failures. Map them to the
	// canonical codes.Canceled / codes.DeadlineExceeded instead of the generic
	// Internal fallback, so the outer Metrics/Tracing/CircuitBreaker interceptors do
	// not count a cancellation as a server-side failure (neither code is in
	// isServerFailureCode). status.FromContextError is the gRPC-idiomatic mapper;
	// guard with errors.Is so non-context errors still fall through to the fail-closed
	// Internal below (FromContextError would otherwise map them to Unknown).
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	// Unknown non-errcode error: fail-closed to Internal with generic message.
	return status.Error(codes.Internal, msgInternalServerError)
}

// clientStatusWithDetails builds a gRPC status for a 4xx *errcode.Error, forwarding
// PublicDetails in a google.rpc.ErrorInfo.Metadata field (#2482). This constructor
// is ONLY called when ec.Kind.IsClient() == true (enforced at the errToStatus call
// site) — the split-constructor design (typed function choice, HARD) makes 5xx-details
// carry structurally inexpressible: the 5xx path calls status.Error directly with no
// ErrorInfo parameter.
//
// ErrorInfo fields:
//   - Reason: string(ec.Code) — the machine-readable errcode, stable across releases.
//   - Domain: errcodeDomain ("gocell.errcode") — distinct from denyReasonDomain
//     ("gocell.authz.grpc") so clients can key on (Domain, Reason) to separate
//     business errors from auth/authz denials.
//   - Metadata: key→rendered-value from ec.Details (nil when no details — guard
//     prevents attaching an empty ErrorInfo, per §no-empty-detail rule).
//
// Metadata value rendering: publicDetailsToMetadata (see its godoc for semantics).
// If attaching the ErrorInfo detail fails (not expected — ErrorInfo is a static proto),
// the bare status is returned: a denial is never downgraded to a lower-level error.
func clientStatusWithDetails(code codes.Code, ec *errcode.Error) error {
	st := status.New(code, ec.Message)
	md := publicDetailsToMetadata(ec)
	if len(md) == 0 {
		// No PublicDetails — do not attach an empty ErrorInfo.
		return st.Err()
	}
	enriched, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   string(ec.Code),
		Domain:   errcodeDomain,
		Metadata: md,
	})
	if err != nil {
		// WithDetails failure is unexpected (ErrorInfo is a well-known proto type).
		// Fall back to the bare status so a 4xx denial is never silently promoted
		// to a lower-level error.
		return st.Err()
	}
	return enriched.Err()
}

// publicDetailsToMetadata converts the PublicDetails of a 4xx *errcode.Error into a
// map[string]string suitable for google.rpc.ErrorInfo.Metadata. Returns nil when
// ec.Details is empty (caller must not attach an empty-Metadata ErrorInfo).
//
// Value rendering is aligned with HTTP marshalJSONValue semantics so clients that
// consume both HTTP and gRPC surfaces receive equivalent structured values:
//
//   - PublicString: raw string value (no JSON quoting — Metadata values are already
//     strings; quoting would double-encode them, unlike the HTTP JSON body context).
//   - PublicInt: decimal integer string (json.Marshal(int64) → "42").
//   - PublicBool: "true" / "false" (json.Marshal(bool)).
//   - PublicDuration: nanosecond integer string (json.Marshal(int64(d)) matches
//     publicDuration.marshalJSONValue which uses json.Marshal(int64(p.v))).
//   - PublicTime: RFC3339Nano string, UNquoted (e.g. "2024-01-02T03:04:05Z") — the
//     same instant publicTime.marshalJSONValue encodes, minus the structural JSON quotes.
//
// String and time values are intentionally NOT JSON-marshaled: marshalJSONValue adds
// surrounding JSON quotes (appropriate for the HTTP response body JSON context), but in
// a map[string]string the value IS the string — quoting would produce "\"device_id\""
// instead of "device_id", breaking symmetric decoding. Numeric/bool details have no such
// quotes, so json.Marshal is used directly for them.
func publicDetailsToMetadata(ec *errcode.Error) map[string]string {
	if len(ec.Details) == 0 {
		return nil
	}
	md := make(map[string]string, len(ec.Details))
	for _, d := range ec.Details {
		v := renderDetailValue(d)
		md[d.Key()] = v
	}
	return md
}

// renderDetailValue converts a PublicDetail value to its Metadata string
// representation, aligned with HTTP marshalJSONValue semantics (see
// publicDetailsToMetadata godoc for the full mapping rationale).
func renderDetailValue(d errcode.PublicDetail) string {
	raw := d.Value()
	switch v := raw.(type) {
	case string:
		// Raw string — no JSON quoting (Metadata values are strings, not JSON bodies).
		return v
	case time.Time:
		// RFC3339Nano, UNquoted — same instant json.Marshal(time.Time) encodes, but
		// without the surrounding JSON quotes. In a map[string]string the value IS the
		// string; embedding json.Marshal's "\"...\"" would put literal quote chars in
		// the Metadata value, inconsistent with the string case above (which strips
		// them). The instant matches HTTP's publicTime.marshalJSONValue; only the
		// structural JSON quotes differ (they belong to the HTTP body, not a flat map).
		return v.Format(time.RFC3339Nano)
	default:
		// int64, bool, time.Duration (int64 underlying): json.Marshal produces a
		// decimal number or boolean literal string — "42", "true", "5000000000" — none
		// of which carry surrounding quotes, so they map cleanly to a string value.
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// UnaryErrcodeMap returns a unary server interceptor that maps the handler's
// returned error to a grpc status via errToStatus. It is the gRPC analog of the
// HTTP path's errcode→HTTP status projection (httputil/response.go). Place it
// just OUTSIDE UnaryRecovery (Recovery is innermost; ErrcodeMap sits one layer
// out) so that Recovery's codes.Internal result passes through this interceptor
// as an already-status error (codes != Unknown) and is not double-mapped.
//
// ref: go-kratos/kratos errors GRPCStatus
func UnaryErrcodeMap() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		return resp, errToStatus(err)
	}
}

// StreamErrcodeMap returns a streaming server interceptor that maps the handler's
// returned error to a grpc status via errToStatus — the streaming analog of
// UnaryErrcodeMap. Placed just outside StreamRecovery in the chain.
func StreamErrcodeMap() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return errToStatus(handler(srv, ss))
	}
}
