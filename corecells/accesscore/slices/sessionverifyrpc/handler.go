// Package sessionverifyrpc implements the gRPC server for the
// grpc.auth.session.verify.v1 contract — accesscore's session-token verification
// RPC and the FIRST platform-cell gRPC service (PR-11 #1154). It is the
// service-to-service counterpart of the HTTP auth path: a caller submits an access
// token and the handler introspects it, reusing the same kauth.IntentTokenVerifier
// (the sessionvalidate slice's Service) that the HTTP auth middleware uses — no
// duplicated JWT/session-state logic.
//
// The server contract is buf's generated sessionverifyv1.SessionVerifyServiceServer
// interface — contractgen emits no Go for kind=grpc (#1688). Server embeds
// sessionverifyv1.UnimplementedSessionVerifyServiceServer (by value) for forward
// compatibility, which is also what satisfies the pb interface so that the
// cellgen-generated reg.GRPCService(... pb.RegisterSessionVerifyServiceServer ...)
// call compiles.
package sessionverifyrpc

import (
	"context"
	"errors"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	sessionverifyv1 "github.com/ghbvf/gocell/generated/contracts/grpc/auth/session/verify/v1"
)

// Server implements sessionverifyv1.SessionVerifyServiceServer.
type Server struct {
	sessionverifyv1.UnimplementedSessionVerifyServiceServer
	// verifier introspects the submitted access token. In production it is the
	// accesscore sessionvalidate Service (revocation- and epoch-aware); the
	// handler depends on the kauth.IntentTokenVerifier interface so the verifier
	// is swappable in tests and the slice carries no JWT/session-store deps of its
	// own.
	verifier kauth.IntentTokenVerifier
}

// NewServer constructs the gRPC session-verify server over the shared token
// verifier (in production: the cell's sessionvalidate Service).
//
// Authorization is NOT a handler concern: the runtime gRPC auth interceptor runs
// the ABAC PDP gate for session:verify before the handler is invoked (#2008,
// declared in endpoints.grpc.methods[].permission). The handler therefore holds no
// Authorizer — the per-method permission gate is enforced transport-side, mirroring
// the HTTP RequirePermission route gate.
func NewServer(verifier kauth.IntentTokenVerifier) *Server {
	return &Server{verifier: verifier}
}

// VerifyToken introspects the submitted access token and returns whether it is
// currently valid plus its verified claims projection.
//
// Unlike typical RPCs, this method does NOT return a gRPC error for invalid/expired/revoked
// tokens (uniform valid=false); only an infrastructure outage surfaces as a gRPC error
// (codes.Unavailable).
//
// Specifically: an invalid, expired, or revoked token returns a response with valid=false and
// NO error — uniform for every failure cause so a caller cannot enumerate WHY a token failed
// (the same anti-enumeration posture as sessionvalidate's single errMsgAuthFailed). Only an
// infrastructure outage (session store / key provider unreachable, classified by the verifier
// as errcode.KindUnavailable) surfaces as codes.Unavailable: masking an outage as a credential
// failure would pollute SLO buckets and hide the incident.
//
// Authorization (session:verify) is enforced by the runtime gRPC auth interceptor
// BEFORE this handler runs (#2008). The token introspected here is the request
// body's subject token — distinct from the caller's bearer token the interceptor
// already authenticated.
func (s *Server) VerifyToken(
	ctx context.Context,
	req *sessionverifyv1.VerifyTokenRequest,
) (*sessionverifyv1.VerifyTokenResponse, error) {
	if req.GetToken() == "" {
		return &sessionverifyv1.VerifyTokenResponse{Valid: false}, nil
	}
	claims, err := s.verifier.VerifyIntent(ctx, req.GetToken(), kauth.TokenIntentAccess)
	if err != nil {
		var ec *errcode.Error
		if errors.As(err, &ec) && ec.Kind == errcode.KindUnavailable {
			// Infrastructure outage: propagate the raw *errcode.Error (KindUnavailable)
			// so the chain's UnaryErrcodeMap interceptor (PR-12 #1155) maps it to
			// codes.Unavailable. This keeps the handler free of grpc/status imports
			// while preserving machine-distinguishable outage vs. credential failure
			// semantics at the gRPC wire level.
			return nil, err
		}
		// Invalid / expired / revoked / wrong-intent: uniform valid=false (no
		// reason enumeration). The verifier has already logged the cause server-side.
		return &sessionverifyv1.VerifyTokenResponse{Valid: false}, nil
	}
	// Tenant binding (#1154 review F2): an introspection caller must not learn the
	// session state of a DIFFERENT tenant's token. The interceptor authenticated the
	// caller and put its principal (with the caller's tenant) in ctx; bind the
	// introspected token to that tenant. A cross-tenant introspection (caller tenant
	// != token tenant) or a caller with no principal collapses to the uniform
	// valid=false — same anti-enumeration posture as a bad token, no leak of the
	// other tenant's subject/roles. super-admin cross-tenant introspection (with
	// explicit permission + FR-007 audit) is a deferred enhancement (#2290); it
	// fails closed here.
	caller, ok := auth.FromContext(ctx)
	if !ok || caller.TenantID != claims.TenantID {
		return &sessionverifyv1.VerifyTokenResponse{Valid: false}, nil
	}
	return &sessionverifyv1.VerifyTokenResponse{
		Valid:                 true,
		Subject:               claims.Subject,
		TenantId:              claims.TenantID,
		SessionId:             claims.SessionID,
		Roles:                 claims.Roles,
		ExpiresAtUnixNano:     claims.ExpiresAt.UnixNano(),
		PasswordResetRequired: claims.PasswordResetRequired,
	}, nil
}
