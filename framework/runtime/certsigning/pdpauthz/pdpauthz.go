// Package pdpauthz provides a PDP-backed implementation of
// [certsigning.Authorizer]. It is the reuse bridge that lets certificate-
// enrollment authorization RE-USE the ABAC PDP via the explicit-subject
// SubjectDescriptor path — no ambient HTTP principal is required.
//
// # Design
//
// The GoCell ABAC PDP ([auth.SubjectAuthorizer]) was designed for HTTP-path
// authorization where an authenticated Principal is already in the request
// context. Cert enrollment is an out-of-band protocol (EST over mTLS) whose
// principal comes from the client certificate, not a JWT — so there is no
// Principal in ctx. SubjectDescriptor provides the explicit-subject bridge:
// the caller forges nothing; only the single sealed constructor
// [auth.NewDeviceSubjectDescriptor] can build a device descriptor, and
// privileged-kind (admin / super-admin) descriptors have no constructor
// (Hard sealed-construction property).
//
// The authorization question is: "May device <id> in tenant <t> execute
// device:enroll on itself?". That maps directly to the PDP ownership rule
// (subject.kind == device AND subject.sub == resource.id) from the
// device:enroll baseline (#1904, same shape as device:read #2351 + #2400 F1).
//
// # Resource canonicalization
//
// The resource identifier passed to AuthorizeAs is the device UUID,
// canonicalized with [httputil.ParseCanonicalUUID] to match the PDP ownership
// rule "subject.sub == resource.id". The subject side is canonicalized at the
// same boundary: [auth.NewDeviceSubjectDescriptor] normalizes a UUID deviceID
// to lowercase canonical form (the same [httputil.ParseCanonicalUUID]) when it
// seals the descriptor, so descriptor.Sub() and the resource are byte-for-byte
// comparable. Both sides mirror the HTTP RequirePermissionForResource path
// (see runtime/auth/middleware.go) — a non-canonical (e.g. uppercase) device
// UUID enrolls correctly instead of failing the ownership equality.
//
// # Obligation fail-closed
//
// If the PDP returns an Allow with a non-zero obligation (RowScope or
// FieldMask), this Authorizer logs a Warn and returns a zero (non-granted)
// SignConstraints — same F5 pattern as runtime/auth.RequirePermission.
// Baseline obligations are zero, so the normal path is unaffected. Future
// obligation types that can be discharged by the cert-signing path can be
// added without changing the interface.
//
// # Device-self identity SAN
//
// A granted SignConstraints allows exactly the device's own identity SAN —
// the SPIFFE-style URI spiffe://<tenant>/device/<deviceID> built by
// [certsigning.DeviceURISAN] — and nothing else (deny-by-default). The issued
// certificate therefore carries a self-describing identity SAN, which the mTLS
// renewal front-end parses to recover (tenant, device) from the presented
// client certificate without any device registry. Any other requested SAN is
// rejected by [certsigning.NewAuthorizedCertRequest]'s subset-of check.
//
// # Consumers
//
// Primary: EST front-end (cellmodules/deviceidentity, PR-8b #1904).
// Future: certlifecycle renewal reconciler (PR-7), any external cell that
// issues device certificates.
//
// ref: cedar-policy Request::new — explicit principal entity, no ambient session.
// ref: cert-manager (Authorize/Sign separation, constraint-then-enforce pattern).
package pdpauthz

import (
	"context"
	"log/slog"
	"net/url"
	"time"

	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/httputil"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/certsigning"
)

// Const-literal messages (MESSAGE-CONST-LITERAL-01).
const (
	errMsgNilPDP         = "pdpauthz: SubjectAuthorizer must not be nil"
	errMsgNonPositiveTTL = "pdpauthz: maxTTL must be positive"
)

// Authorizer is a [certsigning.Authorizer] backed by an ABAC PDP via the
// explicit-subject [auth.SubjectAuthorizer] path. It delegates the enrollment
// authorization decision to the PDP using a device [auth.SubjectDescriptor],
// decoupled from any HTTP context or ambient principal.
//
// Construct with [New].
type Authorizer struct {
	pdp    auth.SubjectAuthorizer
	maxTTL time.Duration
}

// compile-time assertion: *Authorizer satisfies certsigning.Authorizer.
var _ certsigning.Authorizer = (*Authorizer)(nil)

// New constructs a PDP-backed [certsigning.Authorizer].
//
// Parameters:
//   - pdp: the ABAC SubjectAuthorizer (e.g. accesscore authorizationdecide.Service).
//     Must be non-nil; fails-closed with KindInvalid on nil or typed-nil input
//     ([validation.IsNilInterface]).
//   - maxTTL: the issuance ceiling the deployment configures. Must be positive;
//     the granted [certsigning.SignConstraints] always carries this ceiling, so the
//     Signer can never issue a certificate longer-lived than what the operator
//     has configured — even if the device's PDP policy would permit a longer TTL
//     (SAN allowance is a separate policy obligation, not modeled yet).
//
// Returns a non-nil error on any validation failure; the returned *Authorizer is
// nil in that case.
func New(pdp auth.SubjectAuthorizer, maxTTL time.Duration) (*Authorizer, error) {
	if validation.IsNilInterface(pdp) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, errMsgNilPDP)
	}
	if maxTTL <= 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, errMsgNonPositiveTTL)
	}
	return &Authorizer{pdp: pdp, maxTTL: maxTTL}, nil
}

// AuthorizeEnroll evaluates whether the device in claim is permitted to enroll
// a certificate (action: device:enroll, resource: device's own ID). It consults
// the ABAC PDP via [auth.SubjectAuthorizer.AuthorizeAs] using an explicit device
// [auth.SubjectDescriptor] — no ambient context principal is involved.
//
// Outcomes:
//   - PDP returns Allow with zero obligations → return a granted [certsigning.SignConstraints]
//     with maxTTL and the device-self identity SAN allowed (deny-by-default for any other SAN).
//   - PDP returns Deny → return zero (non-granted) SignConstraints, nil error.
//     The caller's [certsigning.NewAuthorizedCertRequest] will reject this as
//     "authorization not granted" (→ HTTP 403).
//   - PDP returns error → propagate the error. The caller should map this to 503.
//   - PDP returns Allow with non-zero obligations → log Warn + return zero
//     SignConstraints, nil error (fail-closed: cert path cannot discharge obligations).
func (a *Authorizer) AuthorizeEnroll(ctx context.Context, claim certsigning.EnrollmentClaim) (certsigning.SignConstraints, error) {
	scope := claim.Scope()
	tenantID := scope.Tenant()
	deviceID := scope.Device()

	// Build an explicit device SubjectDescriptor. This is the only allowed
	// constructor — no admin/super-admin descriptor can be minted here (sealed).
	desc, err := auth.NewDeviceSubjectDescriptor(tenantID.String(), deviceID.String())
	if err != nil {
		// Malformed claim (tenant or device invalid) — fail-closed.
		return certsigning.SignConstraints{}, err
	}

	// Canonicalize the resource ID to mirror RequirePermissionForResource:
	// the PDP ownership rule is "subject.sub == resource.id"; subject.sub is
	// already canonical (NewDeviceSubjectDescriptor normalizes it), so the
	// resource must be canonical too for the equality to fire.
	//
	// ref: cedar-policy Request::new(principal, action, resource) — explicit
	//      resource entity, canonicalized at the call-site boundary.
	resource := deviceID.String()
	if canonical, ok := httputil.ParseCanonicalUUID(resource); ok {
		resource = canonical
	}

	// Consult the PDP. On error (infra / store unavailable) propagate — the
	// caller maps KindUnavailable to 503. A non-nil error from AuthorizeAs
	// guarantees IsAllow()==false (SubjectAuthorizer contract), so no extra
	// guard needed.
	dec, err := a.pdp.AuthorizeAs(ctx, desc, resource, authz.PermDeviceEnroll().String())
	if err != nil {
		return certsigning.SignConstraints{}, err
	}

	if !dec.IsAllow() {
		// Clean policy DENY: return zero SignConstraints (non-granted), no error.
		// The caller's NewAuthorizedCertRequest will reject with 403.
		return certsigning.SignConstraints{}, nil
	}

	// Allow — check for non-zero obligations. The cert-signing path is a
	// coarse PEP: it cannot enforce RowScope (no DB query) or FieldMask (no
	// response columns). An Allow carrying any obligation must be treated as
	// a deny to avoid silently discarding a mandatory PEP duty.
	// Mirrors runtime/auth.RequirePermission F5 (obligation fail-closed).
	//
	// Baseline obligations are zero, so the normal device:enroll path is
	// unaffected.
	if obl := dec.Obligations(); !obl.IsZero() {
		slog.WarnContext(
			ctx, "pdpauthz: device:enroll Allow carries non-zero obligation; denying cert (cert PEP cannot enforce obligation)",
			slog.String("device_id", deviceID.String()),
			slog.String("tenant_id", tenantID.String()),
		)
		return certsigning.SignConstraints{}, nil
	}

	// Grant: allow exactly the device's own identity SAN (deny-by-default for
	// everything else). The issued certificate carries the SPIFFE-style URI SAN
	// spiffe://<tenant>/device/<deviceID> ([certsigning.DeviceURISAN]) so the
	// renewal front-end can recover the device identity from the presented client
	// certificate's URI SAN — no device registry required. Any OTHER requested SAN
	// is rejected by [certsigning.NewAuthorizedCertRequest]'s subset-of check. The
	// enroll/renew front-end requests this same SAN built from the same scope, so
	// the grant and the request agree byte-for-byte (DeviceURISAN is the single
	// source of the spelling).
	//
	// ref: SPIFFE SVID URI SAN; cert-manager/step-ca (issued identity encoded in SAN).
	identitySAN, err := certsigning.NewSubjectAltNames(nil, nil, []*url.URL{certsigning.DeviceURISAN(scope)})
	if err != nil {
		return certsigning.SignConstraints{}, err
	}
	return certsigning.NewSignConstraints(a.maxTTL, identitySAN)
}
