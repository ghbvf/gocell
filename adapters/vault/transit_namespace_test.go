package vault

// VAULT_NAMESPACE env var propagation tests for applyNamespaceFromEnv.
//
// The helper is invoked from NewTransitKeyProviderFromEnv before any Vault I/O
// so that Login + datakey + decrypt + key reads + rotate all carry the
// X-Vault-Namespace header. We exercise the helper directly with a real
// vaultapi.Client (no Vault server needed) and assert via Client.Namespace().

import (
	"testing"

	vaultapi "github.com/hashicorp/vault/api"
)

func TestApplyNamespaceFromEnv_Set(t *testing.T) {
	t.Setenv("VAULT_NAMESPACE", "tenant-a")
	raw, err := vaultapi.NewClient(vaultapi.DefaultConfig())
	if err != nil {
		t.Fatalf("vaultapi.NewClient: %v", err)
	}
	ns := applyNamespaceFromEnv(raw)
	if ns != "tenant-a" {
		t.Errorf("applyNamespaceFromEnv returned %q, want %q", ns, "tenant-a")
	}
	if got := raw.Namespace(); got != "tenant-a" {
		t.Errorf("raw.Namespace() = %q, want %q", got, "tenant-a")
	}
}

func TestApplyNamespaceFromEnv_Unset(t *testing.T) {
	t.Setenv("VAULT_NAMESPACE", "")
	raw, err := vaultapi.NewClient(vaultapi.DefaultConfig())
	if err != nil {
		t.Fatalf("vaultapi.NewClient: %v", err)
	}
	ns := applyNamespaceFromEnv(raw)
	if ns != "" {
		t.Errorf("applyNamespaceFromEnv returned %q, want empty", ns)
	}
	if got := raw.Namespace(); got != "" {
		t.Errorf("raw.Namespace() = %q, want empty", got)
	}
}
