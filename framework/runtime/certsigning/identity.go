package certsigning

import (
	"net/url"
	"strings"
)

// deviceURISANScheme is the URI scheme of the device-identity SAN. It follows the
// SPIFFE SVID convention (spiffe://<trust-domain>/<path>) so the device-identity
// URI is recognizable to SPIFFE-aware tooling, with the trust domain bound to the
// tenant and the path naming the device.
//
// ref: SPIFFE SPIFFE-ID spec — spiffe://<trust-domain>/<workload-path>
const deviceURISANScheme = "spiffe"

// deviceURISANPathPrefix is the fixed path segment preceding the device id in a
// device-identity URI SAN. It keeps the device id in a single, parseable position
// ("/device/<id>") so [ParseDeviceURISAN] can recover it unambiguously.
const deviceURISANPathPrefix = "/device/"

// DeviceURISAN builds the canonical device-identity URI SAN for a scope:
//
//	spiffe://<tenant>/device/<deviceID>
//
// It is the SINGLE source of the device-identity SAN spelling, shared by every
// party that must agree on it byte-for-byte: the enrollment Authorizer (which
// allows exactly this SAN in its SignConstraints), the enroll front-end (which
// requests it on the CSR so the issued certificate carries it), and the renewal
// front-end (which recovers the device identity from a presented client
// certificate's URI SAN via [ParseDeviceURISAN] — no device registry required).
//
// The device id is path-escaped so an id containing reserved characters round-
// trips through [ParseDeviceURISAN]. The tenant is the scope's canonical tenant
// UUID (already validated by [NewCertScope]); it is the URI host (the SPIFFE
// trust domain).
func DeviceURISAN(scope CertScope) *url.URL {
	return &url.URL{
		Scheme: deviceURISANScheme,
		Host:   scope.Tenant().String(),
		Path:   deviceURISANPathPrefix + url.PathEscape(scope.Device().String()),
	}
}

// ParseDeviceURISAN recovers the (tenant, device) identity encoded by
// [DeviceURISAN] from a URI SAN. ok is false for any URI that is not a
// well-formed device-identity SAN (wrong scheme, missing tenant host, or a path
// outside the "/device/<id>" shape) — so a renewal front-end iterating a client
// certificate's URI SANs can skip unrelated URIs and fail-closed when none match.
//
// The returned tenant is the raw host string; the caller validates it as a
// canonical tenant (e.g. via [tenant.ParseTenantID], the untrusted-input
// boundary that normalizes case and rejects nil UUID). The device id is
// path-unescaped to invert [DeviceURISAN]'s escaping.
func ParseDeviceURISAN(u *url.URL) (tenantID string, deviceID string, ok bool) {
	if u == nil || u.Scheme != deviceURISANScheme || u.Host == "" {
		return "", "", false
	}
	if !strings.HasPrefix(u.Path, deviceURISANPathPrefix) {
		return "", "", false
	}
	rawDevice := strings.TrimPrefix(u.Path, deviceURISANPathPrefix)
	if rawDevice == "" || strings.Contains(rawDevice, "/") {
		return "", "", false
	}
	device, err := url.PathUnescape(rawDevice)
	if err != nil || device == "" {
		return "", "", false
	}
	return u.Host, device, true
}
