package auth

// subject_descriptor.go — explicit-subject authorization value type and
// SubjectAuthorizer interface.
//
// ref: cedar-policy Request::new(principal: EntityUid, ...) — explicit principal
//      entity, no ambient session.
// ref: kubernetes/api authorization/v1 SubjectAccessReviewSpec.User — "the user
//      you're testing for".
//
// This file is the reuse bridge for non-HTTP authorization paths (cert signing,
// background reconcile) where there is NO ambient HTTP request and therefore no
// authenticated Principal in the context. The caller explicitly supplies the
// subject to evaluate, decoupled from ctx.

import (
	"context"

	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/httputil"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// SubjectDescriptor is a sealed explicit-subject identity value for
// non-HTTP authorization paths.
//
// # Sealed construction (security property)
//
// All three fields are unexported. The only public constructor is
// NewDeviceSubjectDescriptor — there is intentionally NO constructor for
// privileged kinds (admin, super-admin). A privileged subject therefore
// CANNOT be minted via SubjectDescriptor: the sealed unexported fields make
// SubjectDescriptor{kind: "admin", ...} a compile error outside this package,
// and the only constructor restricts kind to "device".
//
// This is the Hard security property: to grant admin-level access, the
// caller must go through the full JWT principal path (mintDevicePrincipal or
// its admin analog), which requires cryptographic credential verification.
//
// # Zero value
//
// SubjectDescriptor{} has empty kind, sub, and tenant. Any AuthorizeAs call
// with a zero SubjectDescriptor fails-closed: the empty kind and empty tenant
// produce an invalid tenant parse error, so no policy evaluation is reached.
type SubjectDescriptor struct {
	kind   string // principal kind string, e.g. PrincipalDevice.String()
	sub    string // subject identifier (canonical lowercase UUID for a UUID sub)
	tenant string // canonical (lowercase) tenant UUID string
}

// Kind returns the principal kind string (e.g. "device").
// Returns an empty string for the zero SubjectDescriptor.
func (d SubjectDescriptor) Kind() string { return d.kind }

// Sub returns the subject identifier. For a UUID sub it is the canonical
// lowercase form (normalized by NewDeviceSubjectDescriptor) so it matches the
// resource side of the PDP ownership rule (subject.sub == resource.id).
// Returns an empty string for the zero SubjectDescriptor.
func (d SubjectDescriptor) Sub() string { return d.sub }

// Tenant returns the canonical (lowercase) tenant UUID string.
// Returns an empty string for the zero SubjectDescriptor.
func (d SubjectDescriptor) Tenant() string { return d.tenant }

// errMsgDeviceIDEmpty is the const error message for a missing deviceID.
// MESSAGE-CONST-LITERAL-01: errcode messages must be const literals.
const errMsgDeviceIDEmpty = "subject-descriptor: deviceID must not be empty"

// errMsgTenantInvalidForDescriptor is the const message for an invalid tenant.
const errMsgTenantInvalidForDescriptor = "subject-descriptor: tenantID is invalid or not canonical"

// NewDeviceSubjectDescriptor constructs a sealed SubjectDescriptor for a
// device subject. It is the SOLE constructor for SubjectDescriptor; no
// privileged-kind (admin / super-admin) constructor exists by design.
//
// Parameters:
//   - tenantID: canonical UUID string identifying the tenant. Must be
//     non-empty, non-nil-UUID, and parseable as a UUID. Normalized to
//     lowercase canonical form by tenant.ParseTenantID. Fails-closed on
//     invalid input.
//   - deviceID: non-empty device identifier (sub). Must be non-empty.
//     Fails-closed with KindInvalid on empty input. A UUID device id is
//     normalized to lowercase canonical form (httputil.ParseCanonicalUUID) so
//     subject.sub matches the resource side of the PDP ownership rule; a
//     non-UUID id passes through unchanged.
//
// Returns a zero SubjectDescriptor and a non-nil error on any validation
// failure. The caller must check the error before using the descriptor.
func NewDeviceSubjectDescriptor(tenantID, deviceID string) (SubjectDescriptor, error) {
	if deviceID == "" {
		return SubjectDescriptor{}, errcode.New(
			errcode.KindInvalid,
			errcode.ErrValidationFailed,
			errMsgDeviceIDEmpty,
		)
	}
	tid, err := tenant.ParseTenantID(tenantID)
	if err != nil {
		return SubjectDescriptor{}, errcode.Wrap(
			errcode.KindInvalid,
			errcode.ErrValidationFailed,
			errMsgTenantInvalidForDescriptor,
			err,
		)
	}
	// Canonicalize a UUID device sub to lowercase canonical form so the sealed
	// descriptor's Sub() matches the resource.id the PDP ownership rule compares
	// it against: pdpauthz canonicalizes the resource side with the same
	// httputil.ParseCanonicalUUID, mirroring the HTTP RequirePermissionForResource
	// path. Doing it at this single sealed mint point makes "descriptor.Sub() is
	// canonical" true by construction — descriptorSubjectSource needs no defensive
	// re-canonicalization. A non-UUID sub passes through unchanged.
	sub := deviceID
	if canonical, ok := httputil.ParseCanonicalUUID(sub); ok {
		sub = canonical
	}
	return SubjectDescriptor{
		kind:   PrincipalDevice.String(),
		sub:    sub,
		tenant: tid.String(),
	}, nil
}

// SubjectAuthorizer authorizes an explicitly-supplied subject, decoupled from
// the ambient ctx Principal. This is the reuse seam for non-HTTP authorization
// paths (cert signing, background reconcile).
//
// Callers are restricted by AUTHZ-AUTHORIZE-AS-CALLER-FUNNEL-01 (added in a
// later batch).
//
// # Segregated interface
//
// SubjectAuthorizer is intentionally a SEPARATE interface from Authorizer.
// Authorizer has ~10 implementers; adding AuthorizeAs to it would require all
// implementers to be updated. Interface segregation (ISP) keeps the blast
// radius zero: only the ABAC engine (accesscore authorizationdecide.Service)
// implements SubjectAuthorizer in this batch.
//
// # Fail-closed contract
//
// When err != nil the returned Decision is ALWAYS non-Allow (the zero
// authz.Decision{} has IsAllow()==false), so a caller may treat any error as a
// deny and never needs to inspect the Decision on the error path. The error's
// errcode Kind classifies the failure (KindPermissionDenied for invalid tenant,
// KindUnavailable when the policy store is unreachable).
type SubjectAuthorizer interface {
	AuthorizeAs(ctx context.Context, subject SubjectDescriptor, resource, action string) (authz.Decision, error)
}
