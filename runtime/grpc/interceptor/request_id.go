package interceptor

import (
	"context"
	"log/slog"
	"regexp"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// requestIDMetadataKey is the lowercase gRPC metadata key carrying a
// client-supplied request id. gRPC metadata keys are always lowercase, so this
// is the metadata analog of the HTTP "X-Request-Id" header.
const requestIDMetadataKey = "x-request-id"

// requestIDPattern matches the same UUID-friendly charset/length as the HTTP
// RequestID middleware so a propagated id round-trips identically across
// transports.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// UnaryRequestID returns the outermost interceptor: it derives a request id
// (reused from incoming metadata when valid, otherwise freshly generated) and
// stores it under both ctxkeys.RequestID and ctxkeys.CorrelationID so that the
// downstream Tracing / Metrics / Recovery interceptors and any errcode emitted
// by the handler carry a stable correlation id. It mirrors
// runtime/http/middleware.RequestID; the ctxkeys helpers are shared and carry
// no funnel restriction.
func UnaryRequestID() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		id := requestIDFromMetadata(ctx)
		if id == "" {
			// NewUUID's error is unreachable on the production path (crypto/rand
			// fatals on entropy failure); on the off chance it surfaces we leave
			// the id empty rather than fabricating a non-unique value.
			if gen, err := idutil.NewUUID(); err == nil {
				id = gen
			}
		}
		if id != "" {
			ctx = ctxkeys.WithRequestID(ctx, id)
			ctx = ctxkeys.WithCorrelationID(ctx, id)
			if err := grpc.SetHeader(ctx, metadata.Pairs(requestIDMetadataKey, id)); err != nil {
				// Best-effort response-header echo for client correlation; a
				// SetHeader error only occurs when the stream is already
				// terminating, at which point the id has served its purpose via
				// ctx. Non-actionable, so logged at Debug only.
				slog.DebugContext(ctx, "grpc: request-id response header not sent", slog.Any("error", err))
			}
		}
		return handler(ctx, req)
	}
}

func requestIDFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get(requestIDMetadataKey)
	if len(vals) == 0 {
		return ""
	}
	if cand := vals[0]; requestIDPattern.MatchString(cand) {
		return cand
	}
	return ""
}
