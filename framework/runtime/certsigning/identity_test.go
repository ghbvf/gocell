package certsigning_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/certsigning"
)

// canonicalTenant is a fixed canonical UUID used across the identity tests.
const canonicalTenant = "550e8400-e29b-41d4-a716-446655440000"

func mustIdentityScope(t *testing.T, tenantID, issuer, device string) certsigning.CertScope {
	t.Helper()
	iss, err := certsigning.NewIssuerID(issuer)
	require.NoError(t, err)
	dev, err := certsigning.NewDeviceID(device)
	require.NoError(t, err)
	scope, err := certsigning.NewCertScope(tenant.TenantID(tenantID), iss, dev)
	require.NoError(t, err)
	return scope
}

func TestDeviceURISAN_RoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		device string
	}{
		{name: "plain id", device: "device-01"},
		{name: "uuid id", device: "11111111-2222-3333-4444-555555555555"},
		{name: "id with reserved chars", device: "edge/site#3 colo"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope := mustIdentityScope(t, canonicalTenant, "gocell-softca", tc.device)

			san := certsigning.DeviceURISAN(scope)
			require.Equal(t, "spiffe", san.Scheme)
			require.Equal(t, canonicalTenant, san.Host)

			// Round-trip THROUGH a String()/Parse cycle (the wire form a presented
			// client certificate's URI SAN goes through), not just the in-memory URL.
			reparsed, err := url.Parse(san.String())
			require.NoError(t, err)

			gotTenant, gotDevice, ok := certsigning.ParseDeviceURISAN(reparsed)
			require.True(t, ok)
			assert.Equal(t, canonicalTenant, gotTenant)
			assert.Equal(t, tc.device, gotDevice)
		})
	}
}

func TestParseDeviceURISAN_RejectsMalformed(t *testing.T) {
	t.Parallel()
	mustURL := func(raw string) *url.URL {
		u, err := url.Parse(raw)
		require.NoError(t, err)
		return u
	}
	cases := []struct {
		name string
		uri  *url.URL
	}{
		{name: "nil", uri: nil},
		{name: "wrong scheme", uri: mustURL("https://" + canonicalTenant + "/device/dev-1")},
		{name: "missing host", uri: mustURL("spiffe:///device/dev-1")},
		{name: "wrong path prefix", uri: mustURL("spiffe://" + canonicalTenant + "/workload/dev-1")},
		{name: "empty device", uri: mustURL("spiffe://" + canonicalTenant + "/device/")},
		{name: "nested device path", uri: mustURL("spiffe://" + canonicalTenant + "/device/a/b")},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, ok := certsigning.ParseDeviceURISAN(tc.uri)
			assert.False(t, ok)
		})
	}
}
