package vault

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	vaultapi "github.com/hashicorp/vault/api"

	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/aeadutil"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// vaultKeyIDPrefix is the prefix for all Vault Transit key IDs returned by
// this provider. Matches the "vault-transit:vN" format parsed from the Vault
// ciphertext prefix "vault:vN:".
const vaultKeyIDPrefix = "vault-transit:"

// VaultClient is the minimal subset of the Vault SDK client that
// TransitKeyProvider requires. Using an exported interface allows external
// packages (e.g. S14a rotation service, integration test helpers) to inject
// a fake or mock without importing github.com/hashicorp/vault/api directly.
//
// Migrated from runtime/crypto.vaultClient (R1c Phase 0-c); exported in R1c
// reviewer FID-005 to unblock S14a key-rotation path.
//
// ref: hashicorp/vault builtin/logical/transit/path_rewrap.go@main
type VaultClient interface {
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
// Envelope encryption layout (对标 k8s KMS v2):
//   - Encrypt: generate local 32B DEK via crypto/rand → AES-GCM(DEK, plaintext, aad)
//     → Vault Transit encrypt(DEK) wrap → return (ct, nonce, wrappedDEK, keyID).
//   - keyID is extracted from the Vault encrypt response ciphertext prefix
//     "vault:vN:" — mirrors k8s KMS v2 EncryptResponse.KeyID.
//   - Decrypt: Vault Transit decrypt(edk) → unwrapped DEK → AES-GCM Open(ct, nonce, DEK, aad).
//   - AAD is bound entirely in the local AES-GCM layer; it is NOT sent to Vault.
//     This fixes the S1 P0 bug where the pre-R1c path sent AAD as the Vault
//     "context" field, which Vault ignores for non-derived aes256-gcm96 keys.
//
// ref: hashicorp/vault sdk/helper/keysutil/policy.go@main:L127 (version prefix)
// ref: kubernetes/kubernetes staging/src/k8s.io/apiserver/pkg/storage/value/encrypt/envelope/kmsv2/envelope.go@master
// ref: hashicorp/vault builtin/logical/transit/path_encrypt.go@main (context ignored for non-derived keys)
type vaultTransitHandle struct {
	id        string
	mountPath string
	keyName   string
	client    VaultClient
}

// ID returns the key version identifier (e.g. "vault-transit:v3").
func (h *vaultTransitHandle) ID() string { return h.id }

// Encrypt encrypts plaintext using envelope encryption.
//
// Envelope flow (对标 k8s KMS v2 kmsv2/envelope.go):
//  1. Generate a fresh 32B DEK locally via crypto/rand. defer clear(dek).
//  2. AES-GCM encrypt plaintext with DEK and aad → (ct, nonce).
//     AAD is bound here at the local AEAD layer; it is NOT sent to Vault.
//  3. Vault Transit encrypt(DEK) → wrappedDEK ciphertext string "vault:vN:...".
//  4. Extract keyID from the Vault response prefix: "vault:vN:" → "vault-transit:vN".
//  5. Return (ct, nonce, []byte(vaultCiphertext), keyID, nil).
//
// ref: kubernetes/kubernetes kmsv2/envelope.go@master (EncryptResponse.KeyID)
// ref: hashicorp/vault builtin/logical/transit/path_encrypt.go@main
func (h *vaultTransitHandle) Encrypt(ctx context.Context, plaintext, aad []byte) (ciphertext, nonce, edk []byte, keyID string, err error) {
	// 1. Generate a fresh 32-byte DEK.
	dek := make([]byte, 32)
	if _, err = io.ReadFull(rand.Reader, dek); err != nil {
		return nil, nil, nil, "", errcode.Wrap(errcode.ErrKeyProviderEncryptFailed,
			"vault-transit: generate DEK", err)
	}
	defer clear(dek)

	// 2. Encrypt plaintext with DEK. AAD bound in local AES-GCM layer.
	ciphertext, nonce, err = aeadutil.EncryptGCM(dek, plaintext, aad)
	if err != nil {
		return nil, nil, nil, "", errcode.Wrap(errcode.ErrKeyProviderEncryptFailed,
			"vault-transit: local AES-GCM encrypt", err)
	}

	// 3 & 4. Vault Transit wrap DEK and extract keyID from response prefix.
	edk, keyID, err = h.wrapDEKWithVault(ctx, dek)
	if err != nil {
		return nil, nil, nil, "", err
	}

	return ciphertext, nonce, edk, keyID, nil
}

// wrapDEKWithVault calls Vault Transit encrypt endpoint to wrap the DEK.
// It sends ONLY the DEK as base64 — no AAD, no context field.
// Returns (wrappedDEK bytes, keyID string, error).
//
// The Vault response ciphertext prefix "vault:vN:" is used to derive the keyID.
//
// ref: hashicorp/vault builtin/logical/transit/path_encrypt.go@main
// ref: kubernetes/kubernetes kmsv2/envelope.go@master (EncryptResponse.KeyID)
func (h *vaultTransitHandle) wrapDEKWithVault(ctx context.Context, dek []byte) (wrappedDEK []byte, keyID string, err error) {
	encPath := h.mountPath + "/encrypt/" + h.keyName
	data := map[string]any{
		"plaintext": base64.StdEncoding.EncodeToString(dek),
		// No "context" or "associated_data" fields — DEK is a random 32B value
		// with no row identity. AAD binding lives in the local AES-GCM layer.
		// ref: hashicorp/vault builtin/logical/transit/path_encrypt.go@main
		// (context is only meaningful for derived keys — ignored for aes256-gcm96).
	}

	result, err := h.client.Write(ctx, encPath, data)
	if err != nil {
		return nil, "", classifyVaultEncryptError(err)
	}

	ciphertextStr, ok := result["ciphertext"].(string)
	if !ok {
		return nil, "", errcode.New(errcode.ErrKeyProviderEncryptFailed,
			"vault-transit: encrypt response missing string 'ciphertext' field")
	}

	keyID, err = parseVaultKeyID(ciphertextStr)
	if err != nil {
		return nil, "", err
	}

	return []byte(ciphertextStr), keyID, nil
}

// Decrypt decrypts ciphertext using envelope decryption.
//
// Envelope flow:
//  0. Verify keyID consistency: h.id must match the version encoded in edk prefix.
//  1. Vault Transit decrypt(edk) → unwrapped DEK. defer clear(dek).
//  2. AES-GCM Open(ct, nonce, DEK, aad) → plaintext.
//     AAD mismatch causes GCM authentication failure → ErrKeyProviderDecryptFailed.
//
// ref: hashicorp/vault builtin/logical/transit/path_encrypt.go@main
// ref: kubernetes/kubernetes kmsv2/envelope.go@master
func (h *vaultTransitHandle) Decrypt(ctx context.Context, ciphertext, nonce, edk, aad []byte) (plaintext []byte, err error) {
	// 0. Verify that the keyID stored in the edk prefix matches this handle's ID.
	// edk is the Vault Transit ciphertext "vault:vN:..." — parse the version prefix
	// and confirm it matches h.id ("vault-transit:vN"). A mismatch indicates that
	// the caller supplied an edk that belongs to a different key version, which is
	// a permanent error (no retry will fix a misrouted keyID).
	edkVersion, parseErr := parseVaultKeyID(string(edk))
	if parseErr != nil {
		return nil, errcode.Wrap(errcode.ErrKeyProviderDecryptFailed,
			"vault-transit: malformed edk prefix", parseErr)
	}
	if h.id != edkVersion {
		return nil, errcode.New(errcode.ErrKeyProviderDecryptFailed,
			fmt.Sprintf("vault-transit: keyID %q does not match edk version %q", h.id, edkVersion))
	}

	// 1. Unwrap DEK via Vault Transit.
	dek, err := h.unwrapDEKWithVault(ctx, edk)
	if err != nil {
		return nil, err
	}
	defer clear(dek)

	// 2. Local AES-GCM Open. AAD is verified here — mismatch → authentication error.
	plaintext, err = aeadutil.DecryptGCM(dek, ciphertext, nonce, aad)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrKeyProviderDecryptFailed,
			"vault-transit: local AES-GCM decrypt (AAD mismatch or tampered ciphertext)", err)
	}

	return plaintext, nil
}

// unwrapDEKWithVault calls Vault Transit decrypt endpoint to unwrap the DEK.
// It sends ONLY the edk ciphertext — no AAD, no context field.
// Returns the raw 32-byte DEK on success.
//
// ref: hashicorp/vault builtin/logical/transit/path_encrypt.go@main
func (h *vaultTransitHandle) unwrapDEKWithVault(ctx context.Context, edk []byte) (dek []byte, err error) {
	decPath := h.mountPath + "/decrypt/" + h.keyName
	data := map[string]any{
		"ciphertext": string(edk),
		// No "context" or "associated_data" — see wrapDEKWithVault comment.
	}

	result, err := h.client.Write(ctx, decPath, data)
	if err != nil {
		return nil, classifyVaultDecryptError(err)
	}

	plaintextB64, ok := result["plaintext"].(string)
	if !ok {
		return nil, errcode.New(errcode.ErrKeyProviderDecryptFailed,
			"vault-transit: decrypt response missing string 'plaintext' field")
	}

	dek, err = base64.StdEncoding.DecodeString(plaintextB64)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrKeyProviderDecryptFailed,
			"vault-transit: base64 decode DEK from decrypt response", err)
	}

	return dek, nil
}

// ---------------------------------------------------------------------------
// TransitKeyProvider
// ---------------------------------------------------------------------------

// TransitKeyProvider implements kernel/crypto.KeyProvider using HashiCorp Vault
// Transit. It is the adapters/-layer replacement for runtime/crypto
// VaultTransitKeyProvider (R1c layering correction A1).
//
// Envelope model: local AES-GCM with per-value DEK; Vault only wraps the DEK.
// AAD is bound in the local AEAD layer — NOT sent to Vault — fixing the S1 P0
// security bug where AAD was silently ignored by Vault for non-derived keys.
//
// Environment variables (standard Vault SDK env vars):
//   - VAULT_ADDR:                  Vault server address
//   - VAULT_TOKEN:                 Vault token
//   - GOCELL_VAULT_TRANSIT_MOUNT:  transit mount path (default: "transit")
//   - GOCELL_VAULT_TRANSIT_KEY:    key name (default: "gocell-config")
//
// ref: hashicorp/vault builtin/logical/transit/path_rewrap.go@main
// ref: kubernetes/kubernetes kmsv2/envelope.go@master
type TransitKeyProvider struct {
	mu        sync.RWMutex
	client    VaultClient
	mountPath string
	keyName   string
}

// NewTransitKeyProvider creates a TransitKeyProvider with the given VaultClient.
// This is the testable constructor — inject a fake VaultClient in tests.
// The VaultClient interface is exported so external packages can provide
// custom implementations without importing github.com/hashicorp/vault/api.
func NewTransitKeyProvider(client VaultClient, mountPath, keyName string) *TransitKeyProvider {
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
// variables using the real HashiCorp vault/api client. Performs a fail-fast
// Vault key existence check on construction.
//
// Required env vars:
//   - VAULT_ADDR   — Vault server address
//   - VAULT_TOKEN  — Vault token
//
// Optional env vars (default values shown):
//   - GOCELL_VAULT_TRANSIT_MOUNT  (default: "transit")
//   - GOCELL_VAULT_TRANSIT_KEY    (default: "gocell-config")
//
// ref: hashicorp/vault api/client.go@main — DefaultConfig + NewClient
func NewTransitKeyProviderFromEnv() (*TransitKeyProvider, error) {
	cfg := vaultapi.DefaultConfig()
	if addr := os.Getenv("VAULT_ADDR"); addr != "" {
		cfg.Address = addr
	}

	// Fail-fast: construction failure is a configuration error, not an encrypt error.
	// ErrConfigKeyMissing is the canonical infra/config error code for missing or
	// malformed Vault credentials at startup.
	raw, err := vaultapi.NewClient(cfg)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrConfigKeyMissing,
			"vault-transit: create vault api client (check VAULT_ADDR / VAULT_TOKEN)", err)
	}
	token := os.Getenv("VAULT_TOKEN")
	if token == "" {
		return nil, errcode.New(errcode.ErrConfigKeyMissing,
			"vault-transit: VAULT_TOKEN is required")
	}
	raw.SetToken(token)

	mountPath := os.Getenv("GOCELL_VAULT_TRANSIT_MOUNT")
	if mountPath == "" {
		mountPath = "transit"
	}
	keyName := os.Getenv("GOCELL_VAULT_TRANSIT_KEY")
	if keyName == "" {
		keyName = "gocell-config"
	}

	client := NewVaultAPIClient(raw)
	p := NewTransitKeyProvider(client, mountPath, keyName)

	// Fail-fast: verify the key exists at construction time.
	// readLatestVersion already calls classifyVaultReadError, which routes
	// 404/403 → ErrKeyProviderKeyNotFound and 5xx/network → ErrKeyProviderTransient.
	// Return the classified error directly to preserve errcode identity for callers.
	ctx := context.Background()
	if _, err = p.readLatestVersion(ctx); err != nil {
		return nil, err
	}

	return p, nil
}

// Current returns the active KeyHandle for encrypting new values.
// Reads transit/keys/{keyName} to get latest_version and returns a
// vaultTransitHandle with id "vault-transit:vN".
func (p *TransitKeyProvider) Current(ctx context.Context) (kcrypto.KeyHandle, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	version, err := p.readLatestVersion(ctx)
	if err != nil {
		return nil, err
	}

	return &vaultTransitHandle{
		id:        vaultKeyIDPrefix + fmt.Sprintf("v%d", version),
		mountPath: p.mountPath,
		keyName:   p.keyName,
		client:    p.client,
	}, nil
}

// ByID returns the KeyHandle identified by keyID.
// Validates the "vault-transit:" prefix; wrong prefix → ErrKeyProviderKeyNotFound.
func (p *TransitKeyProvider) ByID(_ context.Context, keyID string) (kcrypto.KeyHandle, error) {
	if !strings.HasPrefix(keyID, vaultKeyIDPrefix) {
		return nil, errcode.New(errcode.ErrKeyProviderKeyNotFound,
			fmt.Sprintf("vault-transit: key ID %q does not have expected prefix %q", keyID, vaultKeyIDPrefix))
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	return &vaultTransitHandle{
		id:        keyID,
		mountPath: p.mountPath,
		keyName:   p.keyName,
		client:    p.client,
	}, nil
}

// Rotate generates a new key version via Vault Transit rotate API.
// Calls POST transit/keys/{keyName}/rotate, re-reads latest_version, and
// returns the new "vault-transit:vN" ID.
//
// Note: Rotate holds the write lock for the duration of two Vault round-trips
// (POST .../rotate + GET .../keys/{name}); call during low-traffic windows to
// avoid blocking concurrent Current/ByID reads.
//
// ref: hashicorp/vault builtin/logical/transit/path_keys.go@main
func (p *TransitKeyProvider) Rotate(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	rotatePath := p.mountPath + "/keys/" + p.keyName + "/rotate"
	if _, err := p.client.Write(ctx, rotatePath, nil); err != nil {
		return "", errcode.Wrap(errcode.ErrKeyProviderRotateFailed,
			"vault-transit: rotate key", err)
	}

	version, err := p.readLatestVersion(ctx)
	if err != nil {
		return "", errcode.Wrap(errcode.ErrKeyProviderRotateFailed,
			"vault-transit: read key version after rotate", err)
	}

	return vaultKeyIDPrefix + fmt.Sprintf("v%d", version), nil
}

// readLatestVersion reads the Vault key metadata and returns the latest_version integer.
// Caller must hold the appropriate lock.
func (p *TransitKeyProvider) readLatestVersion(ctx context.Context) (int, error) {
	keyPath := p.mountPath + "/keys/" + p.keyName
	data, err := p.client.Read(ctx, keyPath)
	if err != nil {
		// Differentiate permanent (404/403 → key missing or no permission) from
		// transient (5xx / network) via classifyVaultReadError — prevents startup
		// diagnostics from collapsing every failure into "key not found".
		return 0, classifyVaultReadError(err)
	}

	versionRaw, ok := data["latest_version"]
	if !ok {
		return 0, errcode.New(errcode.ErrKeyProviderKeyNotFound,
			"vault-transit: key metadata missing 'latest_version' field")
	}

	// Vault returns JSON numbers as json.Number (vault/api uses UseNumber decoder)
	// or float64 / int via in-memory fakes; all variants must be handled.
	switch v := versionRaw.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		return int(v), nil
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, errcode.New(errcode.ErrKeyProviderKeyNotFound,
				fmt.Sprintf("vault-transit: latest_version json.Number parse error: %v", err))
		}
		return int(n), nil
	default:
		return 0, errcode.New(errcode.ErrKeyProviderKeyNotFound,
			fmt.Sprintf("vault-transit: unexpected latest_version type %T", versionRaw))
	}
}

// ---------------------------------------------------------------------------
// Error classification helpers
// ---------------------------------------------------------------------------

// classifyVaultError routes a Vault client error to transient (retriable) or
// permanent (caller-specified) classification. Vault HTTP 429/408/500/502/503/504
// and network/context errors map to ErrKeyProviderTransient (CategoryInfra);
// everything else maps to permanentCode (supplied by the caller so the semantics
// match the call site — encrypt/decrypt/read/rotate all surface distinct permanent
// codes).
//
// ref: aws/aws-encryption-sdk-python exceptions.py (transient/permanent split)
// ref: hashicorp/vault api/logical.go — *vaultapi.ResponseError status codes
func classifyVaultError(err error, permanentCode errcode.Code, permanentMsg string) error {
	if isTransientVaultError(err) {
		return errcode.WrapInfra(errcode.ErrKeyProviderTransient,
			"vault-transit: transient "+permanentMsg, err)
	}
	return errcode.Wrap(permanentCode,
		"vault-transit: "+permanentMsg, err)
}

// classifyVaultEncryptError classifies an encrypt path error.
func classifyVaultEncryptError(err error) error {
	return classifyVaultError(err, errcode.ErrKeyProviderEncryptFailed, "encrypt failed")
}

// classifyVaultDecryptError classifies a decrypt path error.
func classifyVaultDecryptError(err error) error {
	return classifyVaultError(err, errcode.ErrKeyProviderDecryptFailed, "decrypt failed")
}

// classifyVaultReadError classifies a metadata read path error
// (transit/keys/{name}). Permanent 4xx — especially 404 (missing key) and 403
// (permission denied) — surface as ErrKeyProviderKeyNotFound; transient 5xx or
// network failures surface as ErrKeyProviderTransient so startup retry logic
// and EventBus DispositionRequeue routing can distinguish the two.
func classifyVaultReadError(err error) error {
	return classifyVaultError(err, errcode.ErrKeyProviderKeyNotFound, "read key metadata")
}

// isTransientVaultError reports whether err indicates a transient Vault failure.
//
// Classification order:
//  1. If err chain contains ErrKeyProviderTransient → transient.
//  2. If err chain contains any other errcode.Error (permanent code like
//     ErrKeyProviderEncryptFailed / ErrKeyProviderDecryptFailed) → permanent.
//  3. If err is a *vaultapi.ResponseError → classify by HTTP status code.
//  4. Pure network/context errors (no errcode, no ResponseError) → transient.
//
// This ordering ensures injected permanent errcode errors (e.g. in unit tests)
// are not accidentally re-classified as transient by the network-fallback case.
// errors.As is used throughout to support errors.Join / multi-Unwrap chains.
func isTransientVaultError(err error) bool {
	// 1. Explicit transient code in chain → transient.
	if errcode.IsTransient(err) {
		return true
	}

	// 2. Any other errcode.Error in chain → permanent (caller already classified it).
	var ec *errcode.Error
	if errors.As(err, &ec) {
		return false
	}

	// 3. Vault SDK ResponseError with HTTP status code.
	var respErr *vaultapi.ResponseError
	if errors.As(err, &respErr) {
		return isTransientHTTPStatus(respErr.StatusCode)
	}

	// 4. Pure network/context error (no errcode, no ResponseError) → transient.
	return true
}

// isTransientHTTPStatus reports whether an HTTP status code indicates a
// condition safe to retry after back-off. Transient codes: 429, 408, 500, 502, 503, 504.
func isTransientHTTPStatus(code int) bool {
	switch code {
	case 429, 408, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// keyID parsing helper
// ---------------------------------------------------------------------------

// parseVaultKeyID extracts the key version identifier from a Vault Transit
// ciphertext string. The ciphertext format is "vault:vN:base64..." where N is
// the key version integer.
//
// Returns "vault-transit:vN" on success, or ErrKeyProviderEncryptFailed if the
// prefix is not in the expected "vault:vN:" format.
//
// ref: hashicorp/vault sdk/helper/keysutil/policy.go@main:L127 (version prefix)
// ref: kubernetes/kubernetes kmsv2/envelope.go@master (EncryptResponse.KeyID)
func parseVaultKeyID(ciphertext string) (string, error) {
	// Expected format: "vault:vN:base64payload"
	parts := strings.SplitN(ciphertext, ":", 3)
	if len(parts) != 3 || parts[0] != "vault" || !strings.HasPrefix(parts[1], "v") {
		return "", errcode.New(errcode.ErrKeyProviderEncryptFailed,
			fmt.Sprintf("vault-transit: unexpected ciphertext prefix (want 'vault:vN:...'): %q", ciphertext))
	}
	return vaultKeyIDPrefix + parts[1], nil
}

// ---------------------------------------------------------------------------
// lifecycle.ManagedResource implementation
// ---------------------------------------------------------------------------

// Compile-time assertion: TransitKeyProvider must implement lifecycle.ManagedResource.
//
// ref: uber-go/fx lifecycle.go — resource lifecycle bundle
// ref: external-secrets/external-secrets pkg/provider/vault ValidateStore —
//
//	uses token/lookup + business-path probe (not sys/health, vault#28846)
var _ lifecycle.ManagedResource = (*TransitKeyProvider)(nil)

// transitReadinessTimeout is the per-probe context deadline for vault_transit_ready.
// 3 seconds is sufficient for LAN Vault; adjust via GOCELL_VAULT_READINESS_TIMEOUT_SEC
// if needed in future iterations.
const transitReadinessTimeout = 3 * time.Second

// Checkers returns a map of readiness probe functions for TransitKeyProvider.
// The single probe "vault_transit_ready" reads transit/keys/{keyName} metadata
// (the same path used by readLatestVersion) to verify that:
//   - The Vault token is valid and not revoked.
//   - The transit mount is enabled.
//   - The named key exists.
//
// This is intentionally NOT sys/health — sys/health only reports whether the
// Vault process is running and unsealed; it does NOT verify that the transit
// mount or the specific key are accessible. (vault#28846)
//
// ref: external-secrets/external-secrets pkg/provider/vault — ValidateStore
//
//	uses auth/token/lookup-self + business-path probe, not sys/health
func (p *TransitKeyProvider) Checkers() map[string]func() error {
	return map[string]func() error{
		"vault_transit_ready": func() error {
			ctx, cancel := context.WithTimeout(context.Background(), transitReadinessTimeout)
			defer cancel()
			_, err := p.readLatestVersion(ctx)
			return err
		},
	}
}

// Worker returns nil because TransitKeyProvider has no background goroutine.
// The ManagedResource interface documents that returning nil means no background
// goroutine is needed; bootstrap skips WithWorkers registration for this resource.
//
// ref: kernel/lifecycle.ManagedResource — "Returning nil means no background
//
//	goroutine is needed"
func (p *TransitKeyProvider) Worker() worker.Worker { return nil }

// Close is a no-op for TransitKeyProvider: the underlying VaultClient manages
// HTTP connection pooling via the standard net/http.Client (which requires no
// explicit close). Close satisfies the lifecycle.ManagedResource contract and
// allows future implementations to flush pending work or drain connections
// if the VaultClient abstraction changes.
func (p *TransitKeyProvider) Close(_ context.Context) error { return nil }
