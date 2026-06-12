package auth

// INVARIANT: DEVICE-PRINCIPAL-MINT-CALLER-01
//
// # DEVICE-PRINCIPAL-MINT-CALLER-01 — sealed device-principal construction (Hard)
//
// mintDevicePrincipal is the SOLE sanctioned producer of a PrincipalDevice in
// the production tree. It is the framework's only path from a trusted device
// credential to a device subject (subject = device id, tenant from the signed
// claim), closing the #1811 gap ("develop has no production PrincipalDevice
// issuer"). PR-8's mTLS client-cert path (cert → PrincipalDevice, the unified
// terminal) is designed to route through this same minter; the device-token
// path here and the cert path there coexist.
//
// # Why a seal, not just the Kind enum (the Hard mechanism)
//
// Principal.Kind is a public enum field, so any package could write a literal
// Principal{Kind: PrincipalDevice}. RowVisibility therefore does NOT trust the
// Kind enum alone: it requires the unexported Principal.device seal, which only
// this package can set and only newDeviceSeal can construct (deviceSeal is an
// unexported type, so no out-of-package code can even name it, let alone forge
// it). A forged Principal{Kind: PrincipalDevice} from a sibling cell is thus
// type-inert — RowVisibility fails closed and never derives RowScopeDevice from
// it. The device subject (which rows are visible) is set here from the verified
// claim, so once the seal is present the subject is trustworthy too.
//
// This is the "sealed construction" Hard 范本 (ai-robust charter §Hard 范本):
// out-of-package forgery of a working device principal is not expressible. The
// archtest backstop DEVICE-PRINCIPAL-MINT-CALLER-01 narrows the in-package
// discipline to this single file (allowlist) and proves the scanner with an
// anti-vacuity red fixture.
//
// # Concept isolation (#1898 FR-003)
//
// A device principal must NOT carry privileged human roles (admin/super-admin):
// a device is a machine subject, not a privileged operator. The service-token
// path is physically separate (NewServiceTokenAuthenticator never reads
// principal_kind), so a service token can never become a device and a
// callerCellID is never a device subject.
//
// # Fail-closed rating / blind spots
//
//   - Downstream (RowVisibility consuming the seal): Hard — type system; an
//     unsealed device principal cannot derive RowScopeDevice.
//   - Upstream (only this file mints): Hard at the package boundary (unexported
//     type + field); Medium within runtime/auth, backstopped by the archtest
//     funnel. Blind spot: in-package code could still construct a seal — a new
//     in-package mint site must be added to the archtest allowlist or CI goes red.

import (
	"github.com/ghbvf/gocell/pkg/errcode"
)

// deviceSeal is the unforgeable proof that a Principal was minted by the
// sanctioned device-principal issuer (mintDevicePrincipal). The type is
// unexported, so no code outside runtime/auth can name it or set
// Principal.device; newDeviceSeal is its only constructor. See
// DEVICE-PRINCIPAL-MINT-CALLER-01.
type deviceSeal struct{}

// newDeviceSeal is the sole constructor of the device seal. It exists only in
// this file; the archtest funnel (DEVICE-PRINCIPAL-MINT-CALLER-01) rejects any
// other in-package call site.
func newDeviceSeal() *deviceSeal { return &deviceSeal{} }

const (
	msgDeviceSubjectMissing = "device token subject missing"
	msgDeviceTenantMissing  = "device token tenant missing"
	msgDeviceRoleForbidden  = "device token must not carry privileged roles"
)

// mintDevicePrincipal builds a sealed PrincipalDevice from verified device-token
// claims. It is the SOLE sanctioned PrincipalDevice producer
// (DEVICE-PRINCIPAL-MINT-CALLER-01) and fails closed when:
//
//   - Subject (the device id) is empty,
//   - TenantID is empty — a device principal derives RowScopeDevice and MUST be
//     tenant-scoped for multi-tenant isolation,
//   - the claims carry a privileged human role (admin / super-admin) — concept
//     isolation: a device is never a privileged operator.
//
// The caller (jwtClaimsToPrincipal) invokes this only when the verified,
// fail-closed-validated principal_kind claim equals PrincipalKindClaimDevice.
// The minted principal carries no session/password-reset baggage.
func mintDevicePrincipal(c Claims) (*Principal, error) {
	if c.Subject == "" {
		return nil, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgDeviceSubjectMissing)
	}
	if c.TenantID == "" {
		return nil, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgDeviceTenantMissing)
	}
	if hasPrivilegedRole(c.Roles) {
		return nil, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, msgDeviceRoleForbidden)
	}
	return &Principal{
		Kind:       PrincipalDevice,
		Subject:    c.Subject,
		AuthMethod: "device_token",
		TenantID:   c.TenantID,
		ExpiresAt:  c.ExpiresAt,
		device:     newDeviceSeal(),
	}, nil
}

// hasPrivilegedRole reports whether roles contains a privileged human role that
// a device principal must never carry (admin or super-admin).
func hasPrivilegedRole(roles []string) bool {
	for _, r := range roles {
		if r == RoleAdmin || r == RoleSuperAdmin {
			return true
		}
	}
	return false
}
