package interceptor

// resource.go — per-message resource extraction for owner-scoped gRPC PDP authz
// (#2207). This file owns the F3 fail-closed path: if extraction fails for ANY
// reason the gate DENIEs — it NEVER falls back to fullMethod. The denial reason
// (reasonResourceUnresolved) is machine-readable via google.rpc.ErrorInfo but
// contains NO extracted field value (PII-safe by construction: denyMeta only
// accepts fullMethod + permission, both non-PII routing keys).
//
// The streaming gate (resourceGatedStream) defers extraction to the first RecvMsg
// so the resource field value is known before the handler begins processing.
// The unary gate (extractResourceForUnary) runs before forwarding to the handler.

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/ghbvf/gocell/framework/pkg/httputil"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// extractResourceFieldValue extracts the string value of fieldName from msg using
// protoreflect, then applies the SAME canonicalization HTTP RequirePermissionForResource
// uses (ParseCanonicalUUID: canonical form when the value is a UUID, raw value otherwise).
// It returns (resource, true) for ANY value the field holds — including empty and
// non-UUID strings — and ("", false) ONLY on a STRUCTURAL failure:
//
//   - msg is not a proto.Message (req is `any` in gRPC interceptors).
//   - The declared field is not found in the message descriptor (misconfiguration).
//   - The field's kind is not a string (wrong type in the proto schema).
//
// The VALUE is never a gate-level deny: an empty or non-UUID value is FORWARDED to the
// PDP, which decides — a coarse grant (admin/operator) ignores the resource and still
// passes; an owner match needs subject == resource, so an empty/foreign value simply
// fails the ownership rule. This is exact HTTP parity: denying on a non-UUID value here
// would WRONGLY block admin/operator (who never consult the resource) for any
// owner-scoped method whose id is not a UUID. Callers treat ("", false) as a hard denial
// (F3 fail-closed — a structural failure means the resource selector itself is broken)
// and MUST NOT fall back to fullMethod.
func extractResourceFieldValue(msg any, fieldName string) (string, bool) {
	pm, ok := msg.(proto.Message)
	if !ok {
		return "", false
	}
	fd := pm.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(fieldName))
	if fd == nil {
		return "", false
	}
	if fd.Kind() != protoreflect.StringKind {
		return "", false
	}
	raw := pm.ProtoReflect().Get(fd).String()
	if canonical, ok := httputil.ParseCanonicalUUID(raw); ok {
		return canonical, true
	}
	return raw, true
}

// extractResourceForUnary resolves the PDP resource for a unary interceptor call.
// If the method has no resource resolver mapping it returns (fullMethod, nil)
// (coarse behavior). If the method has a resource selector but extraction fails it
// returns ("", denyErr) — the caller must return denyErr immediately (F3 fail-closed).
//
// PII note: the extracted resource (device UUID) is logged only at Warn level
// (operator-facing, rate-limited scenario) without the full token; it MUST NOT
// enter ErrorInfo.Metadata (use denyMeta(fullMethod, perm) with method+permission only).
func extractResourceForUnary(
	ctx context.Context,
	cfg authConfig,
	p *auth.Principal,
	fullMethod string,
	req any,
) (resource string, denyErr error) {
	if cfg.resourceFor == nil {
		return fullMethod, nil
	}
	fieldName, ok := cfg.resourceFor(fullMethod)
	if !ok {
		// Method has no resource selector → coarse behavior.
		return fullMethod, nil
	}
	// Method declares a resource field: extraction MUST succeed or we DENY.
	resource, ok = extractResourceFieldValue(req, fieldName)
	if !ok {
		permStr := ""
		if p != nil {
			// Resolve permission name for the log (non-PII routing key).
			if perm, permOK := resolveMethodPermission(cfg.permissionFor, fullMethod); permOK {
				permStr = perm.String()
			}
		}
		slog.WarnContext(ctx, "grpc authz: resource field extraction failed — denying (F3 fail-closed)",
			slog.String("method", fullMethod),
			slog.String("field", fieldName),
			slog.String("permission", permStr))
		return "", deniedStatus(codes.PermissionDenied, msgGRPCResourceUnresolved,
			reasonResourceUnresolved, denyMeta(fullMethod, permStr))
	}
	return resource, nil
}

// resourceGatedStream wraps a grpc.ServerStream for owner-scoped methods (#2207).
// It defers resource extraction to the FIRST RecvMsg call: at stream open the
// request message has not been received yet, so the resource field value is unknown.
// On the first RecvMsg the wrapper extracts the field, authorizes via the PDP
// (F3 fail-closed on extraction failure), and if allowed, caches the result and
// forwards the message. Subsequent RecvMsg calls pass through without re-checking.
//
// If the PDP denies on the first message the stream is terminated with the denial
// status — the handler never sees the message.
type resourceGatedStream struct {
	grpc.ServerStream
	ctx        context.Context
	cfg        authConfig
	p          *auth.Principal
	fullMethod string
	fieldName  string
	authorized bool // true after the first-message authz check passes
}

// Context returns the stream's context, which may carry the principal.
func (s *resourceGatedStream) Context() context.Context { return s.ctx }

// RecvMsg extracts the resource field from the first received message and gates
// the PDP authz before forwarding. On extraction failure or PDP denial it returns
// a gRPC status error (the stream handler should return this upstream). Subsequent
// messages pass through without re-checking (the resource is pinned to the first
// message).
func (s *resourceGatedStream) RecvMsg(m any) error {
	if err := s.ServerStream.RecvMsg(m); err != nil {
		return err
	}
	if s.authorized {
		return nil
	}
	// First message: extract and gate.
	resource, denyErr := extractResourceForUnary(s.ctx, s.cfg, s.p, s.fullMethod, m)
	if denyErr != nil {
		return denyErr
	}
	if err := authorizePermission(s.ctx, s.cfg, s.p, s.fullMethod, resource); err != nil {
		return err
	}
	s.authorized = true
	return nil
}
