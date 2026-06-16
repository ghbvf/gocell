package auth

// principal_propagation.go — business principal propagation across the
// service-token sync boundary.
//
// When an accesscore or any other service cell calls configcore (or another
// cell) via a service token on behalf of a JWT-authenticated user, the
// originating cell's ctx carries the user's principal (actor/subject/session)
// in pkg/ctxkeys. This file encodes that identity into the X-Gocell-Principal
// header, folds it into the service-token MAC so it is tamper-evident, and on
// the receiving end rebuilds it back into the handler ctx.
//
// # Trust model
//
//   - X-Gocell-Principal is integrity-bound to the service-token MAC via
//     buildServiceTokenMessage's "x-gocell-principal=<value>" segment.
//     Tamper, inject, or strip the header → MAC mismatch → 401 (same
//     mechanism as X-Tenant-ID).
//   - The receive-side rebuild (rebuildPropagatedPrincipal) clears the four
//     principal ctxkeys BEFORE restoring, so the propagated business principal
//     wins over the CallerCellID-derived service actor set by injectPrincipalCtxKeys.
//   - Tenant is NOT propagated here; it is conveyed separately via X-Tenant-ID
//     and its own MAC segment. rebuildPropagatedPrincipal does not write
//     ctxkeys.WithTenantID.
//
// # CTXKEYS-PRINCIPAL-WRITE-CALLER-01 compliance
//
// All ctxkeys.With{Actor,Subject,Session}ID calls in this package are
// allowlisted here (runtime/auth/principal_propagation.go). WithTenantID is
// deliberately absent — the rebuild does not write tenant.
// The anti-vacuity guard in the archtest verifies these calls are live.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

const (
	// msgSignNilRing is the error message for a nil key ring passed to SignInternalRequest.
	msgSignNilRing = "auth.SignInternalRequest: ring must not be nil"
	// msgSignEmptyCallerCell is the error message for an empty callerCell.
	msgSignEmptyCallerCell = "auth.SignInternalRequest: callerCell must not be empty"
	// msgSignNilReq is the error message for a nil request.
	msgSignNilReq = "auth.SignInternalRequest: req must not be nil"
	// msgSignGenFailed is the error message when service token generation fails.
	msgSignGenFailed = "auth.SignInternalRequest: service token generation failed"
)

// encodePrincipalHeader serializes pm as compact JSON and returns a base64url
// (no padding) string suitable for use as the X-Gocell-Principal header value.
// Returns "" when pm.IsZero() — callers must skip setting the header in that case.
//
// TenantID is NOT encoded: tenant is conveyed via X-Tenant-ID and its own MAC
// segment; encoding it here would double-count it and create a divergence if
// pm.TenantID were set by accident.
func encodePrincipalHeader(pm outbox.PrincipalMetadata) string {
	// Clear TenantID before encoding: tenant travels via X-Tenant-ID / its
	// own MAC segment, not the principal header.
	pm.TenantID = ""
	if pm.IsZero() {
		return ""
	}
	b, err := json.Marshal(pm)
	if err != nil {
		// json.Marshal on a struct with only string-like fields never errors in
		// practice; treat it as a programming error and return empty.
		slog.Error("auth: encodePrincipalHeader: json.Marshal failed",
			slog.Any("error", err))
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// rebuildPropagatedPrincipal decodes a base64url-encoded PrincipalMetadata
// from header and writes actor/subject/session into ctx. It is the receive-side
// complement of encodePrincipalHeader.
//
// # Safety
//
//   - Invalid base64 or JSON → returns ctx unchanged (safe degradation). The
//     service-token MAC has already validated the header's integrity at this
//     point, so an unmarshal failure indicates a programming error on the
//     send side, not a security issue; degrading to the CallerCellID-derived
//     service actor is the correct fail-safe.
//   - The function clears all four principal ctxkeys FIRST, then restores only
//     actor/subject/session from the decoded payload, so the propagated
//     business principal wins over whatever injectPrincipalCtxKeys stamped.
//   - Tenant is deliberately NOT written: tenant comes from X-Tenant-ID
//     and the auth middleware's ctxkeys.WithTenantID write (allowlisted under
//     runtime/auth/middleware.go). Writing it here would duplicate that path
//     and violate the single-source invariant.
//
// # CTXKEYS-PRINCIPAL-WRITE-CALLER-01
//
// ctxkeys.With{Actor,Subject,Session}ID are called here; WithTenantID is not.
// This file (runtime/auth/principal_propagation.go) is allowlisted for the
// three write calls in the archtest.
func rebuildPropagatedPrincipal(ctx context.Context, header string) context.Context {
	if header == "" {
		return ctx
	}
	raw, err := base64.RawURLEncoding.DecodeString(header)
	if err != nil {
		// MAC-validated upstream; decode failure is a send-side bug.
		slog.Debug("auth: rebuildPropagatedPrincipal: base64 decode failed",
			slog.Any("error", err))
		return ctx
	}
	var pm outbox.PrincipalMetadata
	if err := json.Unmarshal(raw, &pm); err != nil {
		slog.Debug("auth: rebuildPropagatedPrincipal: json unmarshal failed",
			slog.Any("error", err))
		return ctx
	}
	// Clear the four principal keys so the propagated identity wins over
	// the CallerCellID-derived actor set by injectPrincipalCtxKeys.
	ctx = ctxkeys.WithActorID(ctx, "")
	ctx = ctxkeys.WithSubjectID(ctx, "")
	ctx = ctxkeys.WithSessionID(ctx, "")
	// Restore actor / subject / session (non-empty only).
	if pm.ActorID != "" {
		ctx = ctxkeys.WithActorID(ctx, string(pm.ActorID))
	}
	if pm.SubjectID != "" {
		ctx = ctxkeys.WithSubjectID(ctx, string(pm.SubjectID))
	}
	if pm.SessionID != "" {
		ctx = ctxkeys.WithSessionID(ctx, string(pm.SessionID))
	}
	// WithTenantID is intentionally absent: tenant comes from X-Tenant-ID.
	return ctx
}

// SignInternalRequest is the single sanctioned entry point for signing an
// outbound internal service-token request. It:
//
//  1. Reads the caller's business principal (actor/subject/session) from ctx
//     via outbox.ContextPrincipal.
//  2. Clears TenantID from the principal metadata (tenant travels via X-Tenant-ID,
//     not the principal header, to keep the two authority sources distinct).
//  3. Encodes the stripped metadata as base64url compact JSON and sets
//     X-Gocell-Principal on req (only when non-empty).
//  4. Sets X-Tenant-ID to tn.String().
//  5. Calls GenerateServiceToken with the principal header folded into the MAC,
//     so tamper/inject/strip of either signed header causes 401 at verification.
//  6. Sets Authorization: ServiceToken <token>.
//
// # Fail-fast
//
//   - ring == nil → ErrInternal
//   - callerCell == "" → ErrInternal
//   - req == nil → ErrInternal
//   - token generation returns "" → ErrInternal
//
// clock.MustHaveClock enforces that clk is non-nil (programmer error = panic).
//
// # Funnel semantics
//
// This is the ONLY production path that should call GenerateServiceToken on
// outbound requests. Raw calls to GenerateServiceToken from business adapters
// are guarded by SVCTOKEN-CALLER-CELL-REQUIRED-01 and the raw-call ban in
// svctoken_caller_cell.go; this function is the sanctioned caller.
func SignInternalRequest(
	ctx context.Context,
	ring *HMACKeyRing,
	callerCell string,
	req *http.Request,
	tn tenant.TenantID,
	clk clock.Clock,
) error {
	clock.MustHaveClock(clk, "auth.SignInternalRequest")
	if ring == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrInternal, msgSignNilRing)
	}
	if callerCell == "" {
		return errcode.New(errcode.KindInternal, errcode.ErrInternal, msgSignEmptyCallerCell)
	}
	if validation.IsNilInterface(req) || req == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrInternal, msgSignNilReq)
	}

	// Read the business principal from ctx; clear TenantID — it travels via
	// X-Tenant-ID, not the principal header.
	pm := outbox.ContextPrincipal(ctx)
	pm.TenantID = ""

	ph := encodePrincipalHeader(pm)
	if ph != "" {
		req.Header.Set(HeaderPrincipal, ph)
	}

	if tn.String() != "" {
		req.Header.Set(HeaderTenantID, tn.String())
	}

	token := GenerateServiceToken(ring, callerCell, req.Method, req.URL.Path, req.URL.RawQuery, tn, ph, clk.Now())
	if token == "" {
		return errcode.New(errcode.KindInternal, errcode.ErrInternal, msgSignGenFailed)
	}
	req.Header.Set("Authorization", "ServiceToken "+token)
	return nil
}
