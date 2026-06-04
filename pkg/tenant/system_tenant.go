package tenant

// SystemTenantID is the reserved platform/global config scope used by the
// tenant-less internal control-plane read path (configcore's configreadinternal
// slice). It is the canonical nil UUID.
//
// It is NOT a real tenant: GoCell never issues this value to a tenant. It exists
// solely so that the mandatory typed TenantID repo parameter (PR-2; "漏传" =
// compile error) can be satisfied by reads that have no tenant in context — the
// internal /internal/v1 path is authenticated by a service token whose
// callerCell claim is not a tenant (see runtime/auth/middleware.go: service
// principals get no ctxkeys.TenantID).
//
// The nil UUID is a canonical 36-char dashed lowercase UUID.
// TenantID.Validate() accepts it (repo param guards call Validate; the sentinel
// is a valid canonical TenantID value for internal use).
// ParseTenantID() rejects it — that function is the untrusted-input boundary
// (JWT claim, X-Tenant-ID header, UnmarshalJSON); a public caller must not be
// able to alias the system-tier config by submitting the nil-UUID string.
// Internal code must reference this constant directly, never parse it from a string.
//
// Strict-equality model (no OR-merge overlay): rows written under a real tenant
// are invisible to SystemTenantID reads and vice versa. Platform/global config
// keys must be written under SystemTenantID to be readable on the internal path.
//
// SECURITY — sole sanctioned reference: this constant is a tenant-isolation
// bypass token. Passing it to a repo method reads the global config tier,
// crossing the per-tenant boundary. Its only sanctioned production reference is
// cells/configcore/slices/configreadinternal/handler.go. That single-call-site
// invariant is enforced by archtest SYSTEM-TENANT-SENTINEL-CALLER-01
// (tools/archtest/system_tenant_sentinel_caller_test.go): downstream Medium
// caller-allowlist (go/types object-identity match, import-alias-proof); upstream
// is a Go-language ceiling (the sentinel must be exported for the cross-package
// configreadinternal reference, so it cannot be unexported into a type-system
// Hard gate — same permanent ceiling as #851/#1282). The Hard-path (unexport +
// sanctioned typed accessor) won't-do tracker gh issue is named in that
// archtest's package godoc (single source).
const SystemTenantID TenantID = "00000000-0000-0000-0000-000000000000"
