package crypto

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// vaultClient is the minimal subset of the Vault SDK client interface that
// VaultTransitKeyProvider needs. This allows unit tests to inject a fake
// without importing github.com/hashicorp/vault/api.
//
// The real implementation will satisfy this interface via a thin adapter once
// vault/api is added to go.mod (see backlog S14a).
//
// ref: hashicorp/vault builtin/logical/transit/path_rewrap.go@main
type vaultClient interface {
	// Write sends a PUT/POST to the given Vault path with the provided data
	// and returns the raw secret map or an error.
	Write(ctx context.Context, path string, data map[string]any) (map[string]any, error)
	// Read sends a GET to the given Vault path and returns the data map.
	Read(ctx context.Context, path string) (map[string]any, error)
}

// vaultTransitHandle implements KeyHandle by delegating to Vault Transit.
//
// Because Vault Transit manages encryption server-side, it does not expose
// the raw nonce or DEK. The contract for this backend:
//   - Encrypt returns the vault ciphertext in the ciphertext field.
//   - nonce and edk are nil (Vault embeds key version in ciphertext prefix).
//   - keyID is extracted from the vault ciphertext prefix "vault:vN:...".
//   - Decrypt sends the full vault ciphertext back to transit/decrypt/{name}.
//
// Stored column layout for VaultTransit:
//   - value_cipher: Vault ciphertext string as bytes
//   - value_key_id: "vault-transit:vN" (extracted from ciphertext prefix)
//   - value_edk:    nil
//   - value_nonce:  nil
//
// ref: hashicorp/vault sdk/helper/keysutil/policy.go@main:L127 (version prefix)
type vaultTransitHandle struct {
	id        string
	mountPath string
	keyName   string
	client    vaultClient
}

// ID returns the key version identifier (e.g. "vault-transit:v3").
func (h *vaultTransitHandle) ID() string { return h.id }

// Encrypt delegates to Vault Transit encrypt API.
// Returns (vaultCiphertextBytes, nil, nil, nil) — nonce and edk are unused.
// AAD is intentionally ignored: Vault Transit handles context binding server-side.
func (h *vaultTransitHandle) Encrypt(ctx context.Context, plaintext, _ []byte) (ciphertext, nonce, edk []byte, err error) {
	path := fmt.Sprintf("%s/encrypt/%s", h.mountPath, h.keyName)
	encoded := base64.StdEncoding.EncodeToString(plaintext)

	result, err := h.client.Write(ctx, path, map[string]any{
		"plaintext": encoded,
	})
	if err != nil {
		return nil, nil, nil, errcode.Wrap(errcode.ErrKeyProviderDecryptFailed,
			"vault-transit: encrypt failed", err)
	}

	ct, ok := result["ciphertext"].(string)
	if !ok {
		return nil, nil, nil, errcode.New(errcode.ErrKeyProviderDecryptFailed,
			"vault-transit: unexpected ciphertext format in response")
	}

	return []byte(ct), nil, nil, nil
}

// Decrypt delegates to Vault Transit decrypt API.
// The full vault ciphertext (value_cipher) is passed as ciphertext.
// nonce, edk, and aad are ignored — Vault manages these server-side.
func (h *vaultTransitHandle) Decrypt(ctx context.Context, ciphertext, _, _, _ []byte) (plaintext []byte, err error) {
	path := fmt.Sprintf("%s/decrypt/%s", h.mountPath, h.keyName)

	result, err := h.client.Write(ctx, path, map[string]any{
		"ciphertext": string(ciphertext),
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrKeyProviderDecryptFailed,
			"vault-transit: decrypt failed", err)
	}

	encoded, ok := result["plaintext"].(string)
	if !ok {
		return nil, errcode.New(errcode.ErrKeyProviderDecryptFailed,
			"vault-transit: unexpected plaintext format in response")
	}

	decoded, decodeErr := base64.StdEncoding.DecodeString(encoded)
	if decodeErr != nil {
		return nil, errcode.Wrap(errcode.ErrKeyProviderDecryptFailed,
			"vault-transit: base64 decode plaintext", decodeErr)
	}
	return decoded, nil
}

// ---------------------------------------------------------------------------
// VaultTransitKeyProvider
// ---------------------------------------------------------------------------

// VaultTransitKeyProvider implements KeyProvider using HashiCorp Vault Transit.
//
// Environment variables (standard Vault SDK env vars):
//   - VAULT_ADDR:                  Vault server address
//   - VAULT_TOKEN:                 Vault token (or AppRole via VAULT_ROLE_ID + VAULT_SECRET_ID)
//   - GOCELL_VAULT_TRANSIT_MOUNT:  transit mount path (default: "transit")
//   - GOCELL_VAULT_TRANSIT_KEY:    key name (default: "gocell-config")
//
// NOTE: This implementation is structurally complete and unit-testable via the
// vaultClient interface. Production wiring via NewVaultTransitKeyProviderFromEnv
// requires github.com/hashicorp/vault/api (backlog S14a).
//
// ref: hashicorp/vault builtin/logical/transit/path_rewrap.go@main
type VaultTransitKeyProvider struct {
	mu        sync.RWMutex
	client    vaultClient
	mountPath string
	keyName   string
}

// NewVaultTransitKeyProvider creates a VaultTransitKeyProvider with the given
// vaultClient. This is the testable constructor — inject a fake client in tests.
func NewVaultTransitKeyProvider(client vaultClient, mountPath, keyName string) *VaultTransitKeyProvider {
	if mountPath == "" {
		mountPath = "transit"
	}
	if keyName == "" {
		keyName = "gocell-config"
	}
	return &VaultTransitKeyProvider{
		client:    client,
		mountPath: mountPath,
		keyName:   keyName,
	}
}

// NewVaultTransitKeyProviderFromEnv constructs a VaultTransitKeyProvider from
// environment variables.
//
// TODO(S14a): Replace the placeholder with real vault.Client construction
// once github.com/hashicorp/vault/api is added to go.mod.
func NewVaultTransitKeyProviderFromEnv() (*VaultTransitKeyProvider, error) {
	addr := os.Getenv("VAULT_ADDR")
	if addr == "" {
		return nil, errcode.New(errcode.ErrConfigKeyMissing,
			"vault-transit: VAULT_ADDR is required")
	}
	// TODO(S14a): construct vault.Client and wrap in a real vaultClientAdapter.
	return nil, errcode.New(errcode.ErrNotImplemented,
		"vault-transit: real client construction requires github.com/hashicorp/vault/api (backlog S14a)")
}

// Current returns a VaultTransitHandle for the latest key version by reading
// transit/keys/{keyName} and extracting the latest_version field.
func (p *VaultTransitKeyProvider) Current(ctx context.Context) (KeyHandle, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	data, err := p.client.Read(ctx, fmt.Sprintf("%s/keys/%s", p.mountPath, p.keyName))
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrKeyProviderKeyNotFound,
			"vault-transit: read key info failed", err)
	}

	version, err := latestVersionFromKeyData(data)
	if err != nil {
		return nil, err
	}

	return &vaultTransitHandle{
		id:        fmt.Sprintf("vault-transit:v%d", version),
		mountPath: p.mountPath,
		keyName:   p.keyName,
		client:    p.client,
	}, nil
}

// ByID returns a VaultTransitHandle for the given key ID. The ID format is
// "vault-transit:vN". Vault routes decryption to the correct version
// automatically via the ciphertext prefix; we create a handle with the
// requested ID for staleness comparison purposes.
func (p *VaultTransitKeyProvider) ByID(_ context.Context, keyID string) (KeyHandle, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if !strings.HasPrefix(keyID, "vault-transit:") {
		return nil, errcode.New(errcode.ErrKeyProviderKeyNotFound,
			"vault-transit: keyID must have prefix 'vault-transit:', got: "+keyID)
	}

	return &vaultTransitHandle{
		id:        keyID,
		mountPath: p.mountPath,
		keyName:   p.keyName,
		client:    p.client,
	}, nil
}

// Rotate calls the Vault Transit key rotation API and returns the new key
// version ID.
//
// ref: hashicorp/vault builtin/logical/transit/path_rewrap.go@main
func (p *VaultTransitKeyProvider) Rotate(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	_, err := p.client.Write(ctx, fmt.Sprintf("%s/keys/%s/rotate", p.mountPath, p.keyName), nil)
	if err != nil {
		return "", errcode.Wrap(errcode.ErrKeyProviderKeyNotFound,
			"vault-transit: rotate key failed", err)
	}

	// Re-read to discover the new latest version.
	data, err := p.client.Read(ctx, fmt.Sprintf("%s/keys/%s", p.mountPath, p.keyName))
	if err != nil {
		return "", errcode.Wrap(errcode.ErrKeyProviderKeyNotFound,
			"vault-transit: read new key version after rotate failed", err)
	}

	version, err := latestVersionFromKeyData(data)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("vault-transit:v%d", version), nil
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

// latestVersionFromKeyData extracts the latest_version int from a Vault
// transit/keys/{name} response map.
func latestVersionFromKeyData(data map[string]any) (int, error) {
	raw, ok := data["latest_version"]
	if !ok {
		return 0, errcode.New(errcode.ErrKeyProviderKeyNotFound,
			"vault-transit: latest_version field missing from key info response")
	}
	switch v := raw.(type) {
	case float64:
		return int(v), nil
	case int:
		return v, nil
	case int64:
		return int(v), nil
	default:
		return 0, errcode.New(errcode.ErrKeyProviderKeyNotFound,
			fmt.Sprintf("vault-transit: unexpected latest_version type %T", raw))
	}
}
