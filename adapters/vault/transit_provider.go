package vault

import (
	"context"
	"sync"

	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
)

// vaultClient is the minimal subset of the Vault SDK client that
// TransitKeyProvider requires. Using an interface allows unit tests to inject
// a fake without importing github.com/hashicorp/vault/api.
//
// Migrated from runtime/crypto.vaultClient (R1c Phase 0-c).
//
// ref: hashicorp/vault builtin/logical/transit/path_rewrap.go@main
type vaultClient interface {
	// Write sends a PUT/POST to the given Vault path with the provided data
	// and returns the raw secret map or an error.
	Write(ctx context.Context, path string, data map[string]any) (map[string]any, error)
	// Read sends a GET to the given Vault path and returns the data map.
	Read(ctx context.Context, path string) (map[string]any, error)
}

// Compile-time interface assertions — fail at build time if the scaffold drifts
// from the kernel/crypto contracts.
var _ kcrypto.KeyProvider = (*TransitKeyProvider)(nil)
var _ kcrypto.KeyHandle = (*vaultTransitHandle)(nil)

// ---------------------------------------------------------------------------
// vaultTransitHandle
// ---------------------------------------------------------------------------

// vaultTransitHandle implements kernel/crypto.KeyHandle using envelope
// encryption via HashiCorp Vault Transit.
//
// Envelope encryption layout (Phase 2-a will fill in the bodies):
//   - Encrypt: generate local 32B DEK → AES-GCM(DEK, plaintext, aad) → Vault
//     encrypt(DEK) wrap → return (ct, nonce, wrappedDEK, keyID).
//   - keyID is extracted from the Vault encrypt response ciphertext prefix
//     "vault:vN:" (mirrors k8s KMS v2 EncryptResponse.KeyID).
//   - Decrypt: Vault decrypt(edk) → unwrapped DEK → AES-GCM decrypt.
//
// ref: hashicorp/vault sdk/helper/keysutil/policy.go@main:L127 (version prefix)
// ref: kubernetes/kubernetes staging/src/k8s.io/apiserver/pkg/storage/value/encrypt/envelope/kmsv2/envelope.go@master
type vaultTransitHandle struct {
	id        string
	mountPath string      //nolint:unused // used in Phase 2-a Encrypt/Decrypt bodies
	keyName   string      //nolint:unused // used in Phase 2-a Encrypt/Decrypt bodies
	client    vaultClient //nolint:unused // used in Phase 2-a Encrypt/Decrypt bodies
}

// ID returns the key version identifier (e.g. "vault-transit:v3").
func (h *vaultTransitHandle) ID() string { return h.id }

// Encrypt encrypts plaintext using envelope encryption.
//
// Phase 2-a will implement:
//  1. Generate a fresh 32B DEK locally.
//  2. AES-GCM encrypt plaintext with DEK and aad.
//  3. Vault Transit encrypt(DEK) → wrappedDEK.
//  4. Extract keyID from the Vault response ciphertext prefix "vault:vN:".
//  5. Return (ct, nonce, wrappedDEK, keyID, nil).
//
// ref: kubernetes/kubernetes kmsv2/envelope.go@master (EncryptResponse.KeyID)
func (h *vaultTransitHandle) Encrypt(_ context.Context, _, _ []byte) (ciphertext, nonce, edk []byte, keyID string, err error) {
	panic("not implemented: R1c Phase 2-a")
}

// Decrypt decrypts ciphertext using envelope decryption.
//
// Phase 2-a will implement:
//  1. Vault Transit decrypt(edk) → unwrapped DEK.
//  2. AES-GCM decrypt(DEK, ciphertext, nonce, aad) → plaintext.
func (h *vaultTransitHandle) Decrypt(_ context.Context, _, _, _, _ []byte) (plaintext []byte, err error) {
	panic("not implemented: R1c Phase 2-a")
}

// ---------------------------------------------------------------------------
// TransitKeyProvider
// ---------------------------------------------------------------------------

// TransitKeyProvider implements kernel/crypto.KeyProvider using HashiCorp Vault
// Transit. It is the adapters/-layer replacement for runtime/crypto
// VaultTransitKeyProvider (R1c layering correction A1).
//
// Phase 2-a will fill in the method bodies. The compile-time assertions above
// ensure this scaffold stays in sync with the kernel/crypto.KeyProvider contract.
//
// Environment variables (standard Vault SDK env vars):
//   - VAULT_ADDR:                  Vault server address
//   - VAULT_TOKEN:                 Vault token
//   - GOCELL_VAULT_TRANSIT_MOUNT:  transit mount path (default: "transit")
//   - GOCELL_VAULT_TRANSIT_KEY:    key name (default: "gocell-config")
//
// ref: hashicorp/vault builtin/logical/transit/path_rewrap.go@main
type TransitKeyProvider struct {
	mu        sync.RWMutex //nolint:unused // used in Phase 2-a method bodies
	client    vaultClient
	mountPath string
	keyName   string
}

// NewTransitKeyProvider creates a TransitKeyProvider with the given vaultClient.
// This is the testable constructor — inject a fake client in tests.
func NewTransitKeyProvider(client vaultClient, mountPath, keyName string) *TransitKeyProvider {
	if mountPath == "" {
		mountPath = "transit"
	}
	if keyName == "" {
		keyName = "gocell-config"
	}
	return &TransitKeyProvider{
		client:    client,
		mountPath: mountPath,
		keyName:   keyName,
	}
}

// NewTransitKeyProviderFromEnv constructs a TransitKeyProvider from environment
// variables using the real HashiCorp vault/api client.
//
// Phase 2-a will implement this method with fail-fast Vault key existence check.
func NewTransitKeyProviderFromEnv() (*TransitKeyProvider, error) {
	panic("not implemented: R1c Phase 2-a")
}

// Current returns the active KeyHandle for encrypting new values.
//
// Phase 2-a will implement: read transit/keys/{keyName}, extract latest_version,
// return vaultTransitHandle with id "vault-transit:vN".
func (p *TransitKeyProvider) Current(_ context.Context) (kcrypto.KeyHandle, error) {
	panic("not implemented: R1c Phase 2-a")
}

// ByID returns the KeyHandle identified by keyID.
//
// Phase 2-a will implement: validate "vault-transit:" prefix, return a
// vaultTransitHandle bound to the requested keyID.
func (p *TransitKeyProvider) ByID(_ context.Context, _ string) (kcrypto.KeyHandle, error) {
	panic("not implemented: R1c Phase 2-a")
}

// Rotate generates a new key version via Vault Transit rotate API.
//
// Phase 2-a will implement: POST transit/keys/{keyName}/rotate, re-read key
// metadata, return new "vault-transit:vN" ID.
//
// ref: hashicorp/vault builtin/logical/transit/path_rewrap.go@main
func (p *TransitKeyProvider) Rotate(_ context.Context) (string, error) {
	panic("not implemented: R1c Phase 2-a")
}
