package auth

import (
	"context"
	"slices"
	"time"
)

type PrincipalKind int

const (
	// PrincipalUnknown is the zero value of PrincipalKind; it indicates an
	// uninitialised Principal and must never appear in a fully-constructed
	// Principal returned by an Authenticator.
	PrincipalUnknown   PrincipalKind = iota
	PrincipalUser                    // JWT user
	PrincipalService                 // service token / mTLS machine
	PrincipalAnonymous               // public endpoint
	// PrincipalDevice is a first-class device subject (e.g. an MDM-enrolled
	// device), distinct from a human user — it feeds device-posture attributes
	// to downstream ABAC / zero-trust evaluation. The production issuer is
	// mintDevicePrincipal (deviceprincipal.go), the SOLE sanctioned producer
	// (DEVICE-PRINCIPAL-MINT-CALLER-01): a verified device bearer token
	// (principal_kind=device) mints a PrincipalDevice carrying a sealed
	// Principal.device proof. RowVisibility requires that seal before deriving
	// RowScopeDevice, so a forged Principal{Kind: PrincipalDevice} is type-inert.
	// Adding this kind forces every PrincipalKind switch to gain an explicit
	// branch — guarded by archtest PRINCIPAL-KIND-EXHAUSTIVE-SWITCH-01.
	PrincipalDevice
)

func (k PrincipalKind) String() string {
	switch k {
	case PrincipalUnknown:
		return "unknown"
	case PrincipalUser:
		return "user"
	case PrincipalService:
		return "service"
	case PrincipalAnonymous:
		return "anonymous"
	case PrincipalDevice:
		return "device"
	default:
		return "unknown"
	}
}

// Principal is the unified authn subject injected into request context after
// successful authentication. AuthMiddleware and ServiceTokenMiddleware both
// populate Principal via WithPrincipal; handlers consume it via FromContext.
// Principal is the authoritative authn context for all business routes (F1/F7).
//
// For service principals (Kind==PrincipalService): identity is expressed via
// CallerCellID (the originating cell id extracted from the 4-part service
// token). Subject is always empty; Roles is always nil. Use RequireCallerCell
// to authorize internal endpoints by caller allowlist.
//
// Immutability contract: After an Authenticator returns a Principal, callers
// must not modify any field for the lifetime of the request/connection.
// runtime/websocket.Hub snapshots Subject and ExpiresAt at handshake time and
// treats the principal as immutable; mutating Subject/Roles/ExpiresAt/Claims
// after Authenticate has undefined effects on hub subjectIdx and expiry
// eviction. Slices (Roles) and maps (Claims) are read-only by convention; do
// not append or mutate. This mirrors the existing Claims map convention.
type Principal struct {
	Kind                  PrincipalKind
	Subject               string
	Roles                 []string
	AuthMethod            string
	PasswordResetRequired bool
	// CallerCellID is the originating cell id for service principals. It is
	// extracted from the 4-part service token (ts:nonce:callerCell:mac) and
	// checked against ContractSpec.Clients by RequireCallerCell. Empty for
	// user and anonymous principals.
	CallerCellID string
	// TenantID is the tenant isolation boundary the request belongs to, sourced
	// from the JWT "tenant_id" claim. JWTVerifier.VerifyIntent (the single
	// unbypassable chokepoint for JWT→Claims) has already validated and
	// canonicalized it via pkg/tenant.ParseTenantID (a malformed claim is
	// rejected before any Principal is built), so a non-empty value is a
	// canonical lowercase UUID. Empty for service principals (a service token's
	// callerCell is NOT a tenant), anonymous principals, and single-tenant
	// deployments. injectPrincipalCtxKeys propagates a non-empty value to
	// ctxkeys.TenantID for the outbox principal envelope.
	//
	// Deliberately typed string, not pkg/tenant.TenantID: the ctx/wire principal
	// bridge is type-uniform string/SafeID (ctxkeys.With{Actor,Subject,Tenant,
	// Session}ID all take string; outbox.PrincipalMetadata.TenantID is a frozen
	// idutil.SafeID). tenant.TenantID is the REPO-layer typed parameter (PR-2's
	// "漏传=compile error" Hard funnel); threading it through the wire path would
	// only add string↔TenantID↔SafeID conversions. The repo layer re-types this
	// string via tenant.ParseTenantID at its boundary.
	TenantID string
	// Claims is a read-only snapshot of supplementary JWT claims (e.g. "sid",
	// "iss", "token_use"). Callers must treat Claims as a read-only snapshot;
	// mutating it has no effect on authentication decisions and may corrupt
	// shared state.
	Claims map[string]string
	// ExpiresAt is the absolute expiration time of the credential that
	// produced this Principal. Zero value means "no expiry" (anonymous
	// and service principals never expire). For JWT principals this is
	// copied from Claims.ExpiresAt by jwtClaimsToPrincipal.
	//
	// Consumers (e.g. runtime/websocket.Hub) check ExpiresAt against the
	// current clock to decide eviction; long-lived WebSocket connections
	// are evicted on the next ping tick after token expiry.
	ExpiresAt time.Time
	// device is the sealed proof that a PrincipalDevice was minted by the
	// sanctioned issuer (mintDevicePrincipal). It is unexported and of an
	// unexported type (deviceSeal), so no out-of-package code can set it —
	// making a forged Principal{Kind: PrincipalDevice} type-inert. RowVisibility
	// requires a non-nil device before deriving RowScopeDevice. Nil for every
	// non-device principal. See DEVICE-PRINCIPAL-MINT-CALLER-01.
	device *deviceSeal
}

// HasRole is nil-safe: a nil receiver always returns false.
func (p *Principal) HasRole(role string) bool {
	if p == nil || role == "" {
		return false
	}
	return slices.Contains(p.Roles, role)
}

// principalKey uses a private struct type to prevent collision with other packages.
type principalKey struct{}

func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func FromContext(ctx context.Context) (*Principal, bool) {
	v := ctx.Value(principalKey{})
	if v == nil {
		return nil, false
	}
	p, _ := v.(*Principal)
	if p == nil {
		return nil, false
	}
	if p.Kind == PrincipalUnknown {
		// Zero-value Principal was stored; treat as absent to prevent
		// uninitialised structs from leaking through as valid principals.
		return nil, false
	}
	return p, true
}
