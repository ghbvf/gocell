package crypto_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	configcrypto "github.com/ghbvf/gocell/corecells/configcore/internal/crypto"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/crypto"
)

// tenantA / tenantB are canonical test-tenant UUIDs used to assert that the
// AAD is bound to the tenant (different tenants → different AAD).
var (
	tenantA = tenant.TenantID("00000000-0000-0000-0000-000000000001")
	tenantB = tenant.TenantID("00000000-0000-0000-0000-000000000002")
)

func TestAADForConfig_Format(t *testing.T) {
	tests := []struct {
		name      string
		cellID    string
		tenant    tenant.TenantID
		configKey string
		want      string
	}{
		{
			name:      "basic",
			cellID:    "configcore",
			tenant:    tenantA,
			configKey: "api_key",
			want:      "cell:configcore/tenant:00000000-0000-0000-0000-000000000001/key:api_key",
		},
		{
			name:      "empty strings",
			cellID:    "",
			tenant:    tenant.TenantID(""),
			configKey: "",
			want:      "cell:/tenant:/key:",
		},
		{
			name:      "special chars",
			cellID:    "my-cell",
			tenant:    tenantA,
			configKey: "some/complex:key",
			want:      "cell:my-cell/tenant:00000000-0000-0000-0000-000000000001/key:some/complex:key",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(configcrypto.AADForConfig(tc.cellID, tc.tenant, tc.configKey))
			if got != tc.want {
				t.Fatalf("AADForConfig(%q, %q, %q) = %q; want %q", tc.cellID, tc.tenant, tc.configKey, got, tc.want)
			}
		})
	}
}

func TestAADForVersion_Format(t *testing.T) {
	tests := []struct {
		name     string
		cellID   string
		tenant   tenant.TenantID
		configID string
		want     string
	}{
		{
			name:     "basic uuid",
			cellID:   "configcore",
			tenant:   tenantA,
			configID: "550e8400-e29b-41d4-a716-446655440000",
			want:     "cell:configcore/tenant:00000000-0000-0000-0000-000000000001/version:550e8400-e29b-41d4-a716-446655440000",
		},
		{
			name:     "empty strings",
			cellID:   "",
			tenant:   tenant.TenantID(""),
			configID: "",
			want:     "cell:/tenant:/version:",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := string(configcrypto.AADForVersion(tc.cellID, tc.tenant, tc.configID))
			if got != tc.want {
				t.Fatalf("AADForVersion(%q, %q, %q) = %q; want %q", tc.cellID, tc.tenant, tc.configID, got, tc.want)
			}
		})
	}
}

// TestAADForConfig_TenantBinding verifies that two different tenants with the
// same cellID + configKey produce DISTINCT AAD byte sequences. Without tenant
// in the AAD, a ciphertext from tenant A's row could be transplanted into
// tenant B's row with the same key and still decrypt (cross-tenant secret
// leak). This is the static half of the binding guarantee; the dynamic
// fail-closed half is TestAADForConfig_CiphertextSwap_FailsClosed below.
func TestAADForConfig_TenantBinding(t *testing.T) {
	aadA := configcrypto.AADForConfig("configcore", tenantA, "api_key")
	aadB := configcrypto.AADForConfig("configcore", tenantB, "api_key")
	if bytes.Equal(aadA, aadB) {
		t.Fatalf("AADForConfig must differ across tenants for the same key: both returned %q", aadA)
	}
}

// TestAADForVersion_TenantBinding mirrors TestAADForConfig_TenantBinding for the
// version AAD domain.
func TestAADForVersion_TenantBinding(t *testing.T) {
	configID := "550e8400-e29b-41d4-a716-446655440000"
	aadA := configcrypto.AADForVersion("configcore", tenantA, configID)
	aadB := configcrypto.AADForVersion("configcore", tenantB, configID)
	if bytes.Equal(aadA, aadB) {
		t.Fatalf("AADForVersion must differ across tenants for the same configID: both returned %q", aadA)
	}
}

// TestAADForConfig_vs_AADForVersion_NoCollision verifies that AADForConfig and
// AADForVersion produce distinct byte sequences even when their identity inputs
// look similar. This prevents cross-field AAD domain collisions.
func TestAADForConfig_vs_AADForVersion_NoCollision(t *testing.T) {
	cellID := "configcore"
	id := "abc123"

	configAAD := configcrypto.AADForConfig(cellID, tenantA, id)
	versionAAD := configcrypto.AADForVersion(cellID, tenantA, id)

	if bytes.Equal(configAAD, versionAAD) {
		t.Fatalf("AADForConfig and AADForVersion must not be equal for the same inputs: both returned %q", configAAD)
	}
}

// TestAADForConfig_CiphertextSwap_FailsClosed is the fail-closed proof for the
// tenant binding: a value encrypted under (cellID, tenantA, key) MUST NOT
// decrypt when read back under (cellID, tenantB, key). Real AES-GCM treats the
// AAD as authenticated data, so a tenant-segment mismatch surfaces as an
// authentication (tag) error. We use the production runtime transformer
// (LocalAES) so the test exercises the real GCM auth path, not a fake.
//
// ref: kubernetes/apiserver pkg/storage/value — the etcd3 store passes the
// storage key as authenticated data so a ciphertext cannot be replayed under a
// different key.
func TestAADForConfig_CiphertextSwap_FailsClosed(t *testing.T) {
	ctx := context.Background()
	tr := newLocalAESTransformer(t)

	const cellID = "configcore"
	const key = "db_password"
	const secret = "tenant-A-secret"

	aadA := configcrypto.AADForConfig(cellID, tenantA, key)
	enc, err := tr.Encrypt(ctx, []byte(secret), aadA)
	if err != nil {
		t.Fatalf("Encrypt under tenantA failed: %v", err)
	}

	// Sanity: same-tenant AAD decrypts correctly.
	pt, err := tr.Decrypt(ctx, enc.Ciphertext, enc.KeyID, enc.Nonce, enc.EDK, aadA)
	if err != nil {
		t.Fatalf("same-tenant decrypt must succeed: %v", err)
	}
	if string(pt) != secret {
		t.Fatalf("same-tenant decrypt = %q, want %q", pt, secret)
	}

	// Transplant tenantA's ciphertext into a read under tenantB (same key) →
	// AAD mismatch → AES-GCM auth failure. Fail-closed.
	aadB := configcrypto.AADForConfig(cellID, tenantB, key)
	if _, err := tr.Decrypt(ctx, enc.Ciphertext, enc.KeyID, enc.Nonce, enc.EDK, aadB); err == nil {
		t.Fatal("cross-tenant decrypt must FAIL (AES-GCM auth error); ciphertext was transplanted from tenantA to tenantB")
	}
}

// TestAADForVersion_CiphertextSwap_FailsClosed mirrors the config-entry
// fail-closed proof for the version AAD domain.
func TestAADForVersion_CiphertextSwap_FailsClosed(t *testing.T) {
	ctx := context.Background()
	tr := newLocalAESTransformer(t)

	const cellID = "configcore"
	const configID = "550e8400-e29b-41d4-a716-446655440000"
	const secret = "tenant-A-version-secret"

	aadA := configcrypto.AADForVersion(cellID, tenantA, configID)
	enc, err := tr.Encrypt(ctx, []byte(secret), aadA)
	if err != nil {
		t.Fatalf("Encrypt under tenantA failed: %v", err)
	}

	aadB := configcrypto.AADForVersion(cellID, tenantB, configID)
	if _, err := tr.Decrypt(ctx, enc.Ciphertext, enc.KeyID, enc.Nonce, enc.EDK, aadB); err == nil {
		t.Fatal("cross-tenant version decrypt must FAIL (AES-GCM auth error)")
	}
}

// newLocalAESTransformer builds the production runtime LocalAES transformer
// backed by a deterministic in-test key so the swap tests exercise real
// AES-GCM authentication. If the runtime package surface differs, the test
// fails loudly at construction rather than silently degrading to a fake.
func newLocalAESTransformer(t *testing.T) crypto.ValueTransformer {
	t.Helper()
	// 32-byte AES-256 key, hex-encoded (64 hex chars), all 0x2a bytes.
	keyHex := hex.EncodeToString(bytes.Repeat([]byte{0x2a}, 32))
	provider, err := crypto.NewLocalAESKeyProviderFromKeys(keyHex, "")
	if err != nil {
		t.Fatalf("NewLocalAESKeyProviderFromKeys: %v", err)
	}
	return crypto.NewValueTransformer(provider)
}
