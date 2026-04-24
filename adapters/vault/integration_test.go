//go:build integration

package vault_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	vaultapi "github.com/hashicorp/vault/api"
	vaultcontainer "github.com/testcontainers/testcontainers-go/modules/vault"

	vaultadapter "github.com/ghbvf/gocell/adapters/vault"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startVaultContainer starts a Vault dev-mode container for integration tests.
// The transit secret engine is enabled and the key "gocell-config" is created
// during init. Returns (addr, token, teardown). Skips the test if the container
// cannot start (CI without Docker daemon).
func startVaultContainer(t *testing.T) (addr, token string, teardown func()) {
	t.Helper()
	ctx := context.Background()

	container, err := vaultcontainer.Run(ctx,
		"hashicorp/vault:1.17",
		vaultcontainer.WithToken("root-test-token"),
		vaultcontainer.WithInitCommand(
			"secrets enable transit",
			"write -f transit/keys/gocell-config",
		),
	)
	if err != nil {
		t.Skipf("vault container unavailable: %v", err)
	}

	vaultAddr, err := container.HttpHostAddress(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Skipf("vault container address unavailable: %v", err)
	}

	teardown = func() {
		_ = container.Terminate(ctx)
	}
	// HttpHostAddress already returns the full URL including scheme.
	if !strings.HasPrefix(vaultAddr, "http://") && !strings.HasPrefix(vaultAddr, "https://") {
		vaultAddr = "http://" + vaultAddr
	}
	return vaultAddr, "root-test-token", teardown
}

// newProviderFromEnv is a test helper that sets the standard Vault env vars and
// calls NewTransitKeyProviderFromEnv(realMode=false). Relies on t.Setenv for cleanup.
// Uses VAULT_AUTH_METHOD=token for integration tests (dev/CI style).
func newProviderFromEnv(t *testing.T, addr, token string) *vaultadapter.TransitKeyProvider {
	t.Helper()
	t.Setenv("VAULT_ADDR", addr)
	t.Setenv("VAULT_AUTH_METHOD", "token")
	t.Setenv("VAULT_TOKEN", token)
	t.Setenv("GOCELL_VAULT_TRANSIT_MOUNT", "transit")
	t.Setenv("GOCELL_VAULT_TRANSIT_KEY", "gocell-config")

	p, err := vaultadapter.NewTransitKeyProviderFromEnv(false /* realMode */)
	require.NoError(t, err, "NewTransitKeyProviderFromEnv should succeed with running vault")
	return p
}

// ---------------------------------------------------------------------------
// TC-INT-1: Round-trip — encrypt → decrypt with matching AAD.
// Verifies the three envelope elements (ct, nonce, edk) are all present.
// ---------------------------------------------------------------------------

// TestTransitEnvelope_RoundTrip verifies the full envelope encryption round-trip
// using a real Vault dev container. Asserts that all three envelope elements
// (ciphertext, nonce, edk) are non-nil, keyID carries the "vault-transit:v1"
// prefix, and the recovered plaintext matches the original.
//
// ref: hashicorp/vault builtin/logical/transit/path_encrypt.go@main
// ref: kubernetes/kubernetes kmsv2/envelope_test.go@master
func TestTransitEnvelope_RoundTrip(t *testing.T) {
	ctx := context.Background()
	addr, token, teardown := startVaultContainer(t)
	defer teardown()

	p := newProviderFromEnv(t, addr, token)

	handle, err := p.Current(ctx)
	require.NoError(t, err)
	assert.Contains(t, handle.ID(), "vault-transit:v")

	plaintext := []byte("prod-api-secret")
	aad := []byte("cell:configcore/key:api_key")

	ct, nonce, edk, keyID, err := handle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)
	assert.NotEmpty(t, ct, "ciphertext must be non-empty")
	assert.NotNil(t, nonce, "nonce must be present for envelope AES-GCM")
	assert.NotEmpty(t, nonce, "nonce must be non-empty")
	assert.NotNil(t, edk, "edk (wrapped DEK) must be present for envelope mode")
	assert.NotEmpty(t, edk, "edk must be non-empty")
	assert.Contains(t, keyID, "vault-transit:v", "keyID must reflect vault key version")

	recovered, err := handle.Decrypt(ctx, ct, nonce, edk, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, recovered, "round-trip must recover original plaintext")
}

// ---------------------------------------------------------------------------
// TC-INT-A: AppRole auth — full encrypt/decrypt round-trip using AppRole auth.
// Verifies that NewTransitKeyProviderFromEnv works with VAULT_AUTH_METHOD=approle.
// ---------------------------------------------------------------------------

// TestTransitEnvelope_AppRoleAuth_RoundTrip verifies that when Vault AppRole is
// configured (role_id + secret_id), NewTransitKeyProviderFromEnv constructs a
// working provider that can encrypt and decrypt.
//
// The test enables AppRole on the Vault dev container, creates a role with the
// required transit policy, and generates a secret_id for the role.
func TestTransitEnvelope_AppRoleAuth_RoundTrip(t *testing.T) {
	ctx := context.Background()
	addr, rootToken, teardown := startVaultContainer(t)
	defer teardown()

	// Build an admin client to configure AppRole.
	adminCfg := vaultapi.DefaultConfig()
	adminCfg.Address = addr
	adminClient, err := vaultapi.NewClient(adminCfg)
	require.NoError(t, err)
	adminClient.SetToken(rootToken)

	// Enable AppRole auth engine.
	_, err = adminClient.Logical().Write("sys/auth/approle", map[string]any{
		"type": "approle",
	})
	require.NoError(t, err, "enable approle auth engine")

	// Create a policy granting transit datakey/decrypt/read access. PR-A18 routes
	// Encrypt through transit/datakey/plaintext (server-side DEK generation), so
	// the policy must grant capabilities on that path; the legacy transit/encrypt
	// path is no longer invoked.
	_, err = adminClient.Logical().Write("sys/policies/acl/gocell-transit", map[string]any{
		"policy": `
path "transit/datakey/plaintext/gocell-config" { capabilities = ["create","update"] }
path "transit/decrypt/gocell-config"           { capabilities = ["create","update"] }
path "transit/keys/gocell-config"              { capabilities = ["read"] }
path "transit/keys/gocell-config/rotate"       { capabilities = ["create","update"] }
`,
	})
	require.NoError(t, err, "create transit policy")

	// Create an AppRole that uses the policy.
	_, err = adminClient.Logical().Write("auth/approle/role/gocell", map[string]any{
		"token_policies": "gocell-transit",
		"token_ttl":      "5m",
		"token_max_ttl":  "10m",
	})
	require.NoError(t, err, "create approle role")

	// Read role_id and generate a secret_id.
	roleIDSecret, err := adminClient.Logical().Read("auth/approle/role/gocell/role-id")
	require.NoError(t, err, "read role_id")
	roleID, ok := roleIDSecret.Data["role_id"].(string)
	require.True(t, ok, "role_id must be a string")

	secretIDSecret, err := adminClient.Logical().Write("auth/approle/role/gocell/secret-id", map[string]any{})
	require.NoError(t, err, "generate secret_id")
	secretID, ok := secretIDSecret.Data["secret_id"].(string)
	require.True(t, ok, "secret_id must be a string")

	// Configure env for NewTransitKeyProviderFromEnv with AppRole.
	t.Setenv("VAULT_ADDR", addr)
	t.Setenv("VAULT_AUTH_METHOD", "approle")
	t.Setenv("VAULT_ROLE_ID", roleID)
	t.Setenv("VAULT_SECRET_ID_TYPE", "direct")
	t.Setenv("VAULT_SECRET_ID", secretID)
	t.Setenv("GOCELL_VAULT_TRANSIT_MOUNT", "transit")
	t.Setenv("GOCELL_VAULT_TRANSIT_KEY", "gocell-config")

	p, err := vaultadapter.NewTransitKeyProviderFromEnv(false /* realMode */)
	require.NoError(t, err, "NewTransitKeyProviderFromEnv with AppRole must succeed")

	handle, err := p.Current(ctx)
	require.NoError(t, err)
	assert.Contains(t, handle.ID(), "vault-transit:v")

	plaintext := []byte("approle-encrypted-secret")
	aad := []byte("cell:configcore/key:approle_test")

	ct, nonce, edk, _, err := handle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	recovered, err := handle.Decrypt(ctx, ct, nonce, edk, aad)
	require.NoError(t, err)
	assert.Equal(t, plaintext, recovered, "AppRole auth: round-trip must recover original plaintext")
}

// ---------------------------------------------------------------------------
// TC-INT-B: Real-mode static token rejection.
// Verifies that GOCELL_ADAPTER_MODE=real + VAULT_AUTH_METHOD=token fails fast.
// ---------------------------------------------------------------------------

// TestNewTransitKeyProviderFromEnv_RealMode_RejectsStaticToken verifies that
// when realMode=true and VAULT_AUTH_METHOD=token, construction fails with
// ErrVaultAuthFailed (S4b security fix). No Vault container is needed — the
// error is returned before any network call.
func TestNewTransitKeyProviderFromEnv_RealMode_RejectsStaticToken(t *testing.T) {
	t.Setenv("VAULT_ADDR", "http://127.0.0.1:8200") // irrelevant — fails before connecting
	t.Setenv("VAULT_AUTH_METHOD", "token")
	t.Setenv("VAULT_TOKEN", "dev-root-token")
	t.Setenv("GOCELL_VAULT_TRANSIT_MOUNT", "transit")
	t.Setenv("GOCELL_VAULT_TRANSIT_KEY", "gocell-config")

	_, err := vaultadapter.NewTransitKeyProviderFromEnv(true /* realMode */)
	require.Error(t, err, "NewTransitKeyProviderFromEnv in real mode with static token must fail")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec),
		"error must be errcode.Error, got: %T %v", err, err)
	assert.Equal(t, errcode.ErrVaultAuthFailed, ec.Code,
		"real-mode static token rejection must surface as ErrVaultAuthFailed")
}

// ---------------------------------------------------------------------------
// TC-INT-2: AAD mismatch — cross-row replay attack must fail-closed.
// This is the root-cure evidence for the S1 P0 security bug: after the
// envelope rewrite, AAD is bound via local AES-GCM Open, not a Vault context
// hint that was ignored for non-derived keys.
// ---------------------------------------------------------------------------

// TestTransitEnvelope_AADMismatch_FailsClosed verifies that decrypting with a
// different AAD than used during encryption returns ErrKeyProviderDecryptFailed.
//
// This test is the root-cure evidence for the S1 P0 security bug: the old
// VaultTransit implementation passed AAD as the Vault `context` field, which
// Vault ignores for non-derived aes256-gcm96 keys. Envelope mode enforces AAD
// via local AES-GCM cipher.AEAD.Open, so a cross-row copy attack is blocked at
// the local layer.
func TestTransitEnvelope_AADMismatch_FailsClosed(t *testing.T) {
	ctx := context.Background()
	addr, token, teardown := startVaultContainer(t)
	defer teardown()

	p := newProviderFromEnv(t, addr, token)

	handle, err := p.Current(ctx)
	require.NoError(t, err)

	plaintext := []byte("secret-value")
	encryptAAD := []byte("cell:configcore/key:row_a")

	ct, nonce, edk, _, err := handle.Encrypt(ctx, plaintext, encryptAAD)
	require.NoError(t, err)

	// Attempt cross-row replay: decrypt with a different AAD.
	wrongAAD := []byte("cell:configcore/key:row_b")
	_, err = handle.Decrypt(ctx, ct, nonce, edk, wrongAAD)
	require.Error(t, err, "decrypting with mismatched AAD must fail-closed (cross-row replay blocked)")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be errcode.Error, got: %T %v", err, err)
	assert.Equal(t, errcode.ErrKeyProviderDecryptFailed, ec.Code,
		"AAD mismatch must surface as ErrKeyProviderDecryptFailed, not a generic error")
}

// ---------------------------------------------------------------------------
// TC-INT-3: Key rotation — old ciphertext remains decryptable after rotation.
// Vault Transit keeps previous key versions; the edk prefix "vault:vN:" routes
// unwrap to the correct version automatically.
// ---------------------------------------------------------------------------

// TestTransitEnvelope_RotateThenDecryptOldCiphertext verifies that after a key
// rotation the previous ciphertext (encrypted with v1) can still be decrypted.
// Vault Transit retains all historical key versions; the wrapped DEK ciphertext
// carries the version prefix and Vault routes the unwrap accordingly.
func TestTransitEnvelope_RotateThenDecryptOldCiphertext(t *testing.T) {
	ctx := context.Background()
	addr, token, teardown := startVaultContainer(t)
	defer teardown()

	p := newProviderFromEnv(t, addr, token)

	// Encrypt with the current (v1) key.
	handle1, err := p.Current(ctx)
	require.NoError(t, err)
	assert.Contains(t, handle1.ID(), "vault-transit:v1", "initial key must be v1")

	plaintext := []byte("pre-rotation-value")
	aad := []byte("cell:configcore/key:old_key")

	ct1, nonce1, edk1, keyID1, err := handle1.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)
	assert.Contains(t, keyID1, "vault-transit:v1", "keyID must reflect v1 at encrypt time")

	// Rotate to v2.
	newID, err := p.Rotate(ctx)
	require.NoError(t, err)
	assert.Contains(t, newID, "vault-transit:v2", "Rotate must return new key ID")

	// Current() now returns v2.
	handle2, err := p.Current(ctx)
	require.NoError(t, err)
	assert.Contains(t, handle2.ID(), "vault-transit:v2", "current after rotation must be v2")

	// Retrieve the v1 handle via ByID and decrypt the old ciphertext.
	handle1b, err := p.ByID(ctx, handle1.ID())
	require.NoError(t, err)

	recovered, err := handle1b.Decrypt(ctx, ct1, nonce1, edk1, aad)
	require.NoError(t, err, "historical ciphertext encrypted with v1 must still be decryptable")
	assert.Equal(t, plaintext, recovered)
}

// ---------------------------------------------------------------------------
// TC-INT-4: Vault never sees business plaintext — envelope security evidence.
//
// Implementation strategy: an httptest.Server acts as a recording reverse
// proxy in front of the real Vault container. The proxy captures all
// POST /v1/transit/datakey/plaintext/* request bodies without modifying them.
// After Encrypt() returns we inspect the captured payload.
//
// Key assertions (PR-A18 datakey envelope):
//   1. The request body contains {"bits": 256} and NO "plaintext" field —
//      Vault generates the DEK server-side; the client never sends business
//      data on the encrypt path.
//   2. The raw request body does not contain the business plaintext string.
//   3. The request body has no "context" or "associated_data" fields
//      (AAD is never sent to Vault — it is bound locally in AES-GCM).
// ---------------------------------------------------------------------------

// recordingProxy is a reverse proxy that forwards every request to the real
// Vault backend and records the raw body of any POST/PUT to
// /v1/transit/datakey/plaintext/* — the PR-A18 envelope encryption entrypoint.
type recordingProxy struct {
	mu          sync.Mutex
	encryptBody []byte // last captured datakey request body
	backendURL  string
}

func (rp *recordingProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Buffer the body so we can both record it and forward it.
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "proxy: read body failed", http.StatusBadGateway)
		return
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	// Vault SDK sends transit writes as PUT (api/logical.go WriteWithContext);
	// accept POST as well for defence-in-depth.
	if (r.Method == http.MethodPut || r.Method == http.MethodPost) &&
		strings.HasPrefix(r.URL.Path, "/v1/transit/datakey/plaintext/") {
		rp.mu.Lock()
		rp.encryptBody = make([]byte, len(bodyBytes))
		copy(rp.encryptBody, bodyBytes)
		rp.mu.Unlock()
	}

	// Forward to the real Vault.
	targetURL := rp.backendURL + r.URL.RequestURI()
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		http.Error(w, "proxy: build request failed", http.StatusBadGateway)
		return
	}
	req.Header = r.Header.Clone()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "proxy: forward failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "proxy: read response failed", http.StatusBadGateway)
		return
	}

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}

// TestTransitEnvelope_VaultNeverSeesBusinessPlaintext is the key security
// evidence test for the envelope encryption model. PR-A18 routes Encrypt
// through transit/datakey/plaintext (server-side DEK), so the body sent to
// Vault is even narrower than the previous local-DEK + transit/encrypt path.
//
// Assertions:
//
//  1. The request body contains "bits": 256 and NO "plaintext" field at all —
//     the DEK is generated by Vault server-side; the client never serialises
//     business data to the encrypt endpoint.
//  2. The raw request body does not contain the business plaintext string.
//  3. The request body has neither a "context" field nor an "associated_data"
//     field (AAD is bound locally via AES-GCM, not passed to Vault).
//
// ref: hashicorp/vault api-docs/secret/transit POST /datakey/plaintext/:name
// ref: kubernetes/kubernetes kmsv2/envelope_test.go@master (plaintext isolation)
func TestTransitEnvelope_VaultNeverSeesBusinessPlaintext(t *testing.T) {
	ctx := context.Background()
	addr, token, teardown := startVaultContainer(t)
	defer teardown()

	// Stand up a recording proxy in front of the real Vault.
	proxy := &recordingProxy{backendURL: addr}
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()

	// Build a Vault API client pointed at the proxy.
	cfg := vaultapi.DefaultConfig()
	cfg.Address = proxyServer.URL
	rawClient, err := vaultapi.NewClient(cfg)
	require.NoError(t, err)
	rawClient.SetToken(token)

	auth := vaultadapter.NewStaticTokenAuth(rawClient, token)
	client := vaultadapter.NewVaultAPIClient(rawClient)
	p, err := vaultadapter.NewTransitKeyProvider(ctx, client, "transit", "gocell-config", auth)
	require.NoError(t, err)

	businessSecret := "very-sensitive-password-123"
	plaintext := []byte(businessSecret)
	aad := []byte("cell:configcore/key:api_key")

	handle, err := p.Current(ctx)
	require.NoError(t, err)

	_, _, _, _, err = handle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	// Inspect the captured encrypt request.
	proxy.mu.Lock()
	capturedBody := proxy.encryptBody
	proxy.mu.Unlock()

	require.NotEmpty(t, capturedBody, "recording proxy must have captured a datakey request body")

	// Parse the JSON payload.
	var payload map[string]any
	require.NoError(t, json.Unmarshal(capturedBody, &payload), "datakey request body must be valid JSON")

	// Assertion 1: body contains bits:256 and NO plaintext field. PR-A18 datakey
	// envelope generates the DEK server-side, so the client never sends business
	// data on the encrypt path — strictly narrower than the legacy transit/encrypt
	// flow which had to b64-encode the DEK on the wire.
	bitsField, ok := payload["bits"].(float64) // JSON numbers decode as float64
	require.True(t, ok, `request body must contain a "bits" number field`)
	assert.Equal(t, float64(256), bitsField, "datakey bits must be 256 for AES-256 DEK")
	_, hasPlaintext := payload["plaintext"]
	assert.False(t, hasPlaintext,
		`"plaintext" field must NOT be sent to Vault on the encrypt path — DEK is generated server-side`)

	// Assertion 2: raw body must not contain the business secret string.
	assert.NotContains(t, string(capturedBody), businessSecret,
		"Vault request body must not contain business plaintext (envelope boundary violated)")

	// Assertion 3: no "context" or "associated_data" fields — AAD stays local.
	_, hasContext := payload["context"]
	assert.False(t, hasContext,
		`"context" field must not be sent to Vault — AAD is bound locally via AES-GCM, not as Vault context`)
	_, hasAAD := payload["associated_data"]
	assert.False(t, hasAAD,
		`"associated_data" field must not be sent to Vault — AAD stays in the local AES-GCM layer`)
}

// ---------------------------------------------------------------------------
// TC-INT-5: KeyID extracted from Vault encrypt response ciphertext prefix.
// Verifies that the keyID returned by handle.Encrypt() carries the "vault:vN:"
// prefix from the Vault response, not a stale cached value.
// ---------------------------------------------------------------------------

// TestTransitEnvelope_KeyIDFromEncryptResponse verifies that the keyID returned
// from handle.Encrypt() is derived from the Vault encrypt response ciphertext
// prefix "vault:vN:" and is surfaced as "vault-transit:vN".
//
// This mirrors k8s KMS v2 EncryptResponse.KeyID semantics: the keyID is
// authoritative at encrypt-time, eliminating the race between a Current() call
// and a concurrent key rotation.
//
// ref: hashicorp/vault sdk/helper/keysutil/policy.go@main:L127 (version prefix)
// ref: kubernetes/kubernetes kmsv2/envelope.go@master (EncryptResponse.KeyID)
func TestTransitEnvelope_KeyIDFromEncryptResponse(t *testing.T) {
	ctx := context.Background()
	addr, token, teardown := startVaultContainer(t)
	defer teardown()

	p := newProviderFromEnv(t, addr, token)

	handle, err := p.Current(ctx)
	require.NoError(t, err)
	handleID := handle.ID()

	plaintext := []byte("key-id-check-value")
	aad := []byte("cell:configcore/key:key_id_test")

	_, _, edk, keyID, err := handle.Encrypt(ctx, plaintext, aad)
	require.NoError(t, err)

	// keyID must have the "vault-transit:v" prefix.
	assert.True(t, strings.HasPrefix(keyID, "vault-transit:v"),
		"keyID from Encrypt must start with 'vault-transit:v', got: %q", keyID)

	// keyID from Encrypt must match handle.ID() (no rotation happened).
	assert.Equal(t, handleID, keyID,
		"keyID from Encrypt() must equal handle.ID() when no rotation occurred")

	// The edk must start with the Vault ciphertext prefix "vault:v".
	// This is the raw wrapped DEK ciphertext; the "vault:vN:" prefix is how
	// we extract the key version.
	edkStr := string(edk)
	assert.True(t, strings.HasPrefix(edkStr, "vault:v"),
		"edk (wrapped DEK) must start with Vault ciphertext prefix 'vault:v', got: %q", edkStr)

	// Extract the version number from the edk prefix and verify it matches keyID.
	// edk format: "vault:v1:base64..." → version part is "v1"
	edkParts := strings.SplitN(edkStr, ":", 3)
	require.Len(t, edkParts, 3, "edk must have format 'vault:vN:...'")
	versionFromEDK := edkParts[1] // e.g. "v1"
	assert.Equal(t, "vault-transit:"+versionFromEDK, keyID,
		"keyID must be 'vault-transit:' + version extracted from edk prefix")
}
