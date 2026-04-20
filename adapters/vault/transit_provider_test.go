package vault

// Envelope contract tests for adapters/vault.TransitKeyProvider and
// vaultTransitHandle. White-box (same package) so fakeVaultClient can satisfy
// the unexported VaultClient interface without extra indirection.
//
// Contract coverage:
//   TC-1  Encrypt calls local AES-GCM and wraps DEK via Vault
//   TC-2  Decrypt round-trip (Encrypt → Decrypt → original plaintext)
//   TC-3  AAD mismatch fails closed (ErrKeyProviderDecryptFailed)
//   TC-4  Vault server error → transient / permanent classification
//   TC-5  keyID parsed from edk prefix, not from handle.id
//   TC-6  Current reads latest_version from Vault key metadata
//   TC-7  ByID validates prefix; wrong prefix → ErrKeyProviderKeyNotFound
//   TC-8  Rotate calls rotate endpoint and re-reads new version
//   Plus: ResponseError status-code classification, context/net error
//   classification, parseVaultKeyID boundary cases, and concurrent encrypt/
//   rotate race coverage.
//
// ref: kubernetes/kubernetes staging/src/k8s.io/apiserver/pkg/storage/value/encrypt/
//      envelope/kmsv2/envelope_test.go@master

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// fakeVaultClient — injectable double for vaultClient
// ---------------------------------------------------------------------------

// fakeVaultClient simulates the Vault Transit HTTP API for unit tests.
// It supports:
//   - Configurable latest_version for key metadata reads
//   - Deterministic encrypt/decrypt: wraps DEK as base64(xor(dek, masterKey))
//     so the pair is invertible without real AES
//   - Error injection per-call-type (writeErr / readErr)
//   - Call-count tracking for Encrypt / Decrypt / Rotate
//
// mu protects latestVersion, lastWritePath, lastWriteData, lastReadPath so that
// the concurrent race test (FID-010) does not trigger the race detector on the
// fake itself — only real production races should be flagged.
type fakeVaultClient struct {
	mu sync.Mutex

	// Key metadata
	latestVersion int

	// Deterministic wrap key — xor(dek, masterKey) is the "wrapped" form.
	// Must be exactly 32 bytes so xor with a 32B DEK produces a stable output.
	masterKey [32]byte

	// Error injection: if set, Write/Read return this error immediately.
	writeErr error
	readErr  error

	// Call records — store last-seen path + data for assertions.
	lastWritePath string
	lastWriteData map[string]any
	lastReadPath  string

	// Call counters (atomic so tests are race-safe even if future tests
	// happen to run Encrypt concurrently).
	encryptCalls atomic.Int64
	decryptCalls atomic.Int64
	rotateCalls  atomic.Int64
}

// compile-time assertion: fakeVaultClient satisfies the exported VaultClient interface.
var _ VaultClient = (*fakeVaultClient)(nil)

// Write handles transit/encrypt/{key}, transit/decrypt/{key}, and transit/keys/{key}/rotate.
func (f *fakeVaultClient) Write(ctx context.Context, path string, data map[string]any) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.writeErr != nil {
		return nil, f.writeErr
	}

	f.lastWritePath = path
	f.lastWriteData = data

	switch {
	case strings.HasSuffix(path, "/rotate"):
		// Rotation: bump latest_version, return empty map (Vault rotate returns nothing).
		f.rotateCalls.Add(1)
		f.latestVersion++
		return map[string]any{}, nil

	case strings.Contains(path, "/decrypt/"):
		// Vault Transit decrypt is a POST (Write), not GET.
		f.decryptCalls.Add(1)
		cipherStr, ok := data["ciphertext"].(string)
		if !ok {
			return nil, fmt.Errorf("fake vault: decrypt: missing or non-string ciphertext field")
		}
		dek, err := f.unwrapDEK(cipherStr)
		if err != nil {
			return nil, err
		}
		return map[string]any{"plaintext": base64.StdEncoding.EncodeToString(dek)}, nil

	default:
		// Assume transit/encrypt/{key}
		f.encryptCalls.Add(1)

		rawB64, ok := data["plaintext"].(string)
		if !ok {
			return nil, fmt.Errorf("fake vault: encrypt: missing or non-string plaintext field")
		}
		dek, err := base64.StdEncoding.DecodeString(rawB64)
		if err != nil {
			return nil, fmt.Errorf("fake vault: encrypt: base64 decode plaintext: %w", err)
		}

		wrapped := xorBytes(dek, f.masterKey[:len(dek)])
		vaultCipher := fmt.Sprintf("vault:v%d:%s",
			f.latestVersion,
			base64.StdEncoding.EncodeToString(wrapped),
		)
		return map[string]any{"ciphertext": vaultCipher}, nil
	}
}

// Read handles transit/keys/{key} — returns key metadata with latest_version.
func (f *fakeVaultClient) Read(ctx context.Context, path string) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.readErr != nil {
		return nil, f.readErr
	}

	f.lastReadPath = path

	// transit/keys/{key} is a GET (Read) for key metadata.
	// transit/decrypt/{key} is a POST (Write) — should never arrive here.
	if strings.Contains(path, "/decrypt/") {
		return nil, fmt.Errorf("fake vault: unexpected Read on decrypt path %q (decrypt must use Write)", path)
	}

	return map[string]any{
		"latest_version": f.latestVersion,
	}, nil
}

// writeDEKDecrypt is called as a Write (POST) in real Vault; fakeVaultClient
// routes it here when path contains "/decrypt/".
func (f *fakeVaultClient) unwrapDEK(vaultCipher string) ([]byte, error) {
	// vault:vN:<base64>
	parts := strings.SplitN(vaultCipher, ":", 3)
	if len(parts) != 3 || parts[0] != "vault" {
		return nil, errcode.New(errcode.ErrKeyProviderDecryptFailed,
			"fake vault: malformed vault cipher: "+vaultCipher)
	}
	wrapped, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("fake vault: decrypt: base64 decode: %w", err)
	}
	return xorBytes(wrapped, f.masterKey[:len(wrapped)]), nil
}

// xorBytes XORs src against key (key must be >= len(src)).
func xorBytes(src, key []byte) []byte {
	out := make([]byte, len(src))
	for i := range src {
		out[i] = src[i] ^ key[i]
	}
	return out
}

// fakeWriteFunc allows per-test Write override without subclassing.
// Tests that need fully custom Write behaviour embed this.
type fakeVaultClientWithWriteOverride struct {
	fakeVaultClient
	writeFn func(ctx context.Context, path string, data map[string]any) (map[string]any, error)
}

func (f *fakeVaultClientWithWriteOverride) Write(ctx context.Context, path string, data map[string]any) (map[string]any, error) {
	return f.writeFn(ctx, path, data)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newTestProvider constructs a TransitKeyProvider backed by the given fake.
func newTestProvider(fake *fakeVaultClient) *TransitKeyProvider {
	return NewTransitKeyProvider(fake, "transit", "gocell-config")
}

// mustCurrent resolves the current handle and type-asserts to the concrete
// *vaultTransitHandle so subsequent assertions can inspect private fields.
func mustCurrent(t *testing.T, p *TransitKeyProvider) *vaultTransitHandle {
	t.Helper()
	h, err := p.Current(context.Background())
	if err != nil {
		t.Fatalf("Current() unexpected error: %v", err)
	}
	handle, ok := h.(*vaultTransitHandle)
	if !ok {
		t.Fatalf("Current() returned non-*vaultTransitHandle: %T", h)
	}
	return handle
}

// callEncrypt is a typed facade over h.Encrypt that returns the five-tuple
// directly — keeps the assertion surface in tests concise.
func callEncrypt(t *testing.T, h *vaultTransitHandle, ctx context.Context, plaintext, aad []byte) (ct, nonce, edk []byte, keyID string, err error) {
	t.Helper()
	return h.Encrypt(ctx, plaintext, aad)
}

// callDecrypt is a typed facade over h.Decrypt.
func callDecrypt(t *testing.T, h *vaultTransitHandle, ctx context.Context, ct, nonce, edk, aad []byte) (plaintext []byte, err error) {
	t.Helper()
	return h.Decrypt(ctx, ct, nonce, edk, aad)
}

// callRotate is a typed facade over p.Rotate.
func callRotate(t *testing.T, p *TransitKeyProvider, ctx context.Context) (newID string, err error) {
	t.Helper()
	return p.Rotate(ctx)
}

// callByID is a typed facade over p.ByID.
func callByID(t *testing.T, p *TransitKeyProvider, ctx context.Context, id string) (h interface{ ID() string }, err error) {
	t.Helper()
	return p.ByID(ctx, id)
}

// errChainHasCode walks err's Unwrap chain and reports whether any entry is
// an *errcode.Error whose Code equals want. Tests use it instead of inlining
// the Unwrap loop, which was the biggest contributor to the cognitive
// complexity of the TC-4, TC-4b, and TC-7 tests (SonarCloud CC>15).
func errChainHasCode(err error, want errcode.Code) bool {
	for e := err; e != nil; {
		if ecErr, ok := e.(*errcode.Error); ok && ecErr.Code == want {
			return true
		}
		unwrapper, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = unwrapper.Unwrap()
	}
	return false
}

// ---------------------------------------------------------------------------
// TC-1: Encrypt calls local AES-GCM and wraps DEK
// ---------------------------------------------------------------------------

func TestVaultTransitHandle_Encrypt_CallsLocalAESAndWrapsDEK(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 3}
	p := newTestProvider(fake)
	h := mustCurrent(t, p)

	ctx := context.Background()
	ct, nonce, edk, keyID, err := callEncrypt(t, h, ctx, []byte("secret"), []byte("row:123"))

	// (a) fake client received exactly one Write call
	if fake.encryptCalls.Load() != 1 {
		t.Errorf("expected 1 Vault encrypt call, got %d", fake.encryptCalls.Load())
	}
	if fake.lastWritePath != "transit/encrypt/gocell-config" {
		t.Errorf("wrong Write path: %q, want %q", fake.lastWritePath, "transit/encrypt/gocell-config")
	}

	// (b) Write data "plaintext" is base64 of 32-byte DEK
	ptField, ok := fake.lastWriteData["plaintext"].(string)
	if !ok {
		t.Fatal("Write data missing string 'plaintext' field")
	}
	decoded, decodeErr := base64.StdEncoding.DecodeString(ptField)
	if decodeErr != nil {
		t.Fatalf("'plaintext' field is not valid base64: %v", decodeErr)
	}
	if len(decoded) != 32 {
		t.Errorf("DEK length = %d, want 32", len(decoded))
	}

	// (c) Write data has NO "context" or "associated_data" fields
	if _, has := fake.lastWriteData["context"]; has {
		t.Error("Write data must NOT contain 'context' field (AAD must not be sent to Vault)")
	}
	if _, has := fake.lastWriteData["associated_data"]; has {
		t.Error("Write data must NOT contain 'associated_data' field")
	}

	// (d) ciphertext is AES-GCM output, different from plaintext
	if string(ct) == "secret" {
		t.Error("ciphertext must not equal plaintext")
	}
	if len(ct) == 0 {
		t.Error("ciphertext must be non-empty")
	}

	// (e) nonce is 12 bytes
	if len(nonce) != 12 {
		t.Errorf("nonce length = %d, want 12", len(nonce))
	}

	// (f) edk is non-nil and starts with "vault:v3:"
	if edk == nil {
		t.Fatal("edk must be non-nil")
	}
	edkStr := string(edk)
	if !strings.HasPrefix(edkStr, "vault:v3:") {
		t.Errorf("edk = %q, want prefix 'vault:v3:'", edkStr)
	}

	// (g) keyID matches version in edk
	if keyID != "vault-transit:v3" {
		t.Errorf("keyID = %q, want %q", keyID, "vault-transit:v3")
	}

	// (h) no error
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TC-2: Encrypt → Decrypt round-trip
// ---------------------------------------------------------------------------

func TestVaultTransitHandle_DecryptRoundTrip(t *testing.T) {
	// Phase 2-a will make this test green.
	fake := &fakeVaultClient{latestVersion: 3}
	p := newTestProvider(fake)
	h := mustCurrent(t, p)

	ctx := context.Background()
	plaintext := []byte("secret payload")
	aad := []byte("row:123")

	ct, nonce, edk, _, encErr := callEncrypt(t, h, ctx, plaintext, aad)
	if encErr != nil {
		t.Fatalf("Encrypt() unexpected error: %v", encErr)
	}

	// Reset call tracking before Decrypt so we can assert Decrypt-specific payload.
	fake.lastWritePath = ""
	fake.lastWriteData = nil

	got, decErr := callDecrypt(t, h, ctx, ct, nonce, edk, aad)
	if decErr != nil {
		t.Fatalf("Decrypt() unexpected error: %v", decErr)
	}

	// Round-trip result must match original plaintext.
	if string(got) != string(plaintext) {
		t.Errorf("Decrypt returned %q, want %q", got, plaintext)
	}

	// Decrypt must send Write to transit/decrypt/gocell-config with "ciphertext" field.
	if fake.lastWritePath != "transit/decrypt/gocell-config" {
		t.Errorf("Decrypt Write path = %q, want %q", fake.lastWritePath, "transit/decrypt/gocell-config")
	}
	ciphertextField, ok := fake.lastWriteData["ciphertext"].(string)
	if !ok {
		t.Fatal("Decrypt Write data missing string 'ciphertext' field")
	}
	if !strings.HasPrefix(ciphertextField, "vault:v") {
		t.Errorf("ciphertext field = %q, want vault:vN:... prefix", ciphertextField)
	}
	// No context/associated_data sent to Vault.
	if _, has := fake.lastWriteData["context"]; has {
		t.Error("Decrypt Write data must NOT contain 'context' field")
	}
	if _, has := fake.lastWriteData["associated_data"]; has {
		t.Error("Decrypt Write data must NOT contain 'associated_data' field")
	}
}

// ---------------------------------------------------------------------------
// TC-3: AAD mismatch fails closed
// ---------------------------------------------------------------------------

func TestVaultTransitHandle_AADMismatch_FailsClosed(t *testing.T) {
	// Phase 2-a will make this test green.
	fake := &fakeVaultClient{latestVersion: 3}
	p := newTestProvider(fake)
	h := mustCurrent(t, p)

	ctx := context.Background()
	ct, nonce, edk, _, encErr := callEncrypt(t, h, ctx, []byte("secret"), []byte("row:123"))
	if encErr != nil {
		t.Fatalf("Encrypt() unexpected error: %v", encErr)
	}

	// Decrypt with wrong AAD — AES-GCM Open must fail.
	_, decErr := callDecrypt(t, h, ctx, ct, nonce, edk, []byte("row:456"))
	if decErr == nil {
		t.Fatal("Decrypt with wrong AAD must return an error (fail-closed)")
	}

	if !errChainHasCode(decErr, errcode.ErrKeyProviderDecryptFailed) {
		t.Errorf("expected ErrKeyProviderDecryptFailed in error chain, got: %v", decErr)
	}
}

// ---------------------------------------------------------------------------
// TC-4: Vault server errors classified transient vs permanent
// ---------------------------------------------------------------------------

// runEncryptWithInjectedErr runs callEncrypt against a fake that returns
// writeErr and asserts the resulting error chain's transient classification
// and (optionally) the presence of wantCode. Used by TC-4 and TC-4b to drop
// per-case boilerplate well below the cognitive-complexity threshold.
func runEncryptWithInjectedErr(t *testing.T, writeErr error, wantTransient bool, wantCode errcode.Code) {
	t.Helper()
	fake := &fakeVaultClient{latestVersion: 1, writeErr: writeErr}
	p := newTestProvider(fake)
	h := mustCurrent(t, p)

	_, _, _, _, err := callEncrypt(t, h, context.Background(), []byte("payload"), []byte("aad"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := errcode.IsTransient(err); got != wantTransient {
		t.Errorf("IsTransient=%v, want %v (err=%v)", got, wantTransient, err)
	}
	if wantCode != "" && !errChainHasCode(err, wantCode) {
		t.Errorf("error chain must contain %s (err=%v)", wantCode, err)
	}
}

func TestVaultTransitHandle_VaultServerError_ClassifiedTransient(t *testing.T) {
	transientCases := []struct {
		name     string
		vaultErr error
	}{
		{
			name:     "503 Service Unavailable",
			vaultErr: errcode.New(errcode.ErrKeyProviderTransient, "vault: 503 service unavailable"),
		},
		{
			name:     "429 Too Many Requests",
			vaultErr: errcode.New(errcode.ErrKeyProviderTransient, "vault: 429 rate limited"),
		},
		{
			name:     "408 Request Timeout",
			vaultErr: errcode.New(errcode.ErrKeyProviderTransient, "vault: 408 request timeout"),
		},
	}
	for _, tc := range transientCases {
		t.Run(tc.name, func(t *testing.T) {
			runEncryptWithInjectedErr(t, tc.vaultErr, true, "")
		})
	}

	permanentCases := []struct {
		name     string
		vaultErr error
		wantCode errcode.Code
	}{
		{
			name:     "400 Bad Request → encrypt failed",
			vaultErr: errcode.New(errcode.ErrKeyProviderEncryptFailed, "vault: 400 bad request"),
			wantCode: errcode.ErrKeyProviderEncryptFailed,
		},
		{
			name:     "403 Forbidden → encrypt failed",
			vaultErr: errcode.New(errcode.ErrKeyProviderEncryptFailed, "vault: 403 forbidden"),
			wantCode: errcode.ErrKeyProviderEncryptFailed,
		},
		{
			name:     "404 Not Found → encrypt failed",
			vaultErr: errcode.New(errcode.ErrKeyProviderEncryptFailed, "vault: 404 not found"),
			wantCode: errcode.ErrKeyProviderEncryptFailed,
		},
	}
	for _, tc := range permanentCases {
		t.Run(tc.name, func(t *testing.T) {
			runEncryptWithInjectedErr(t, tc.vaultErr, false, tc.wantCode)
		})
	}
}

// ---------------------------------------------------------------------------
// TC-4b: real *vaultapi.ResponseError classification chain (C1)
// ---------------------------------------------------------------------------
//
// TC-4 (above) injects pre-built errcode.Error values, bypassing the
// classifyVaultEncryptError → isTransientVaultError → isTransientHTTPStatus path.
// These tests exercise the complete chain from a real *vaultapi.ResponseError.

func TestVaultTransitHandle_RealResponseError_Classification(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		wantTransi bool
		wantCode   errcode.Code
	}{
		{"403 Forbidden → permanent ErrKeyProviderEncryptFailed", 403, false, errcode.ErrKeyProviderEncryptFailed},
		{"400 Bad Request → permanent ErrKeyProviderEncryptFailed", 400, false, errcode.ErrKeyProviderEncryptFailed},
		{"404 Not Found → permanent ErrKeyProviderEncryptFailed", 404, false, errcode.ErrKeyProviderEncryptFailed},
		{"503 Service Unavailable → transient ErrKeyProviderTransient", 503, true, errcode.ErrKeyProviderTransient},
		{"429 Rate Limited → transient ErrKeyProviderTransient", 429, true, errcode.ErrKeyProviderTransient},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runEncryptWithInjectedErr(t,
				&vaultapi.ResponseError{
					StatusCode: tc.statusCode,
					Errors:     []string{"permission denied"},
				},
				tc.wantTransi,
				tc.wantCode,
			)
		})
	}
}

// ---------------------------------------------------------------------------
// TC-3b: Decrypt keyID / edk version mismatch → permanent error
// ---------------------------------------------------------------------------

func TestVaultTransitHandle_Decrypt_KeyIDEdkMismatch_PermanentError(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 3}
	p := newTestProvider(fake)
	ctx := context.Background()

	// Encrypt with v3 handle.
	h := mustCurrent(t, p)
	_, nonce, edk, _, err := callEncrypt(t, h, ctx, []byte("secret"), []byte("aad"))
	if err != nil {
		t.Fatalf("Encrypt() unexpected error: %v", err)
	}

	// Build a handle for v2 (different version) — edk prefix says v3, handle says v2.
	h2, err := p.ByID(ctx, "vault-transit:v2")
	if err != nil {
		t.Fatalf("ByID(v2) unexpected error: %v", err)
	}
	vh2 := h2.(*vaultTransitHandle)

	_, decErr := callDecrypt(t, vh2, ctx, []byte("ct"), nonce, edk, []byte("aad"))
	if decErr == nil {
		t.Fatal("expected error for keyID/edk mismatch, got nil")
	}
	if errcode.IsTransient(decErr) {
		t.Errorf("keyID/edk mismatch must be permanent, not transient; err=%v", decErr)
	}
	if !errChainHasCode(decErr, errcode.ErrKeyProviderDecryptFailed) {
		t.Errorf("error chain must contain ErrKeyProviderDecryptFailed; err=%v", decErr)
	}
}

// ---------------------------------------------------------------------------
// TC-5: keyID parsed from edk prefix, not from handle.id
// ---------------------------------------------------------------------------

func TestVaultTransitHandle_KeyIDFromEdkPrefix(t *testing.T) {
	// Phase 2-a will make this test green.
	//
	// Scenario: fake returns ciphertext "vault:v7:..." even though the handle
	// was constructed with latest_version=3 (simulate a rotate race between
	// Current() and the actual Encrypt call).
	ctx := context.Background()

	// We need Write to return v7 regardless of latestVersion on the fake.
	override := &fakeVaultClientWithWriteOverride{
		fakeVaultClient: fakeVaultClient{latestVersion: 3},
	}
	override.writeFn = func(_ context.Context, path string, data map[string]any) (map[string]any, error) {
		override.lastWritePath = path
		override.lastWriteData = data
		override.encryptCalls.Add(1)
		// Return v7 prefix regardless of latestVersion.
		rawB64, _ := data["plaintext"].(string)
		dek, _ := base64.StdEncoding.DecodeString(rawB64)
		wrapped := xorBytes(dek, override.masterKey[:len(dek)])
		return map[string]any{
			"ciphertext": "vault:v7:" + base64.StdEncoding.EncodeToString(wrapped),
		}, nil
	}

	p := NewTransitKeyProvider(override, "transit", "gocell-config")
	h := mustCurrent(t, p)

	_, _, _, keyID, err := callEncrypt(t, h, ctx, []byte("payload"), []byte("aad"))
	if err != nil {
		t.Fatalf("Encrypt() unexpected error: %v", err)
	}

	// keyID must come from edk prefix "vault:v7:", not from handle.id ("vault-transit:v3").
	if keyID != "vault-transit:v7" {
		t.Errorf("keyID = %q, want %q (must be parsed from edk prefix, not pre-read handle.id)",
			keyID, "vault-transit:v7")
	}
}

// ---------------------------------------------------------------------------
// TC-6: Current reads latest_version from Vault key metadata
// ---------------------------------------------------------------------------

func TestTransitKeyProvider_Current_ReadsLatestVersion(t *testing.T) {
	// Phase 2-a will make this test green.
	fake := &fakeVaultClient{latestVersion: 5}
	p := newTestProvider(fake)

	h := mustCurrent(t, p)
	if h.ID() != "vault-transit:v5" {
		t.Errorf("handle.ID() = %q, want %q", h.ID(), "vault-transit:v5")
	}
}

// ---------------------------------------------------------------------------
// TC-7: ByID validates prefix; wrong prefix → ErrKeyProviderKeyNotFound
// ---------------------------------------------------------------------------

func TestTransitKeyProvider_ByID(t *testing.T) {
	// Phase 2-a will make this test green.
	ctx := context.Background()
	fake := &fakeVaultClient{latestVersion: 3}
	p := newTestProvider(fake)

	t.Run("valid vault-transit prefix", func(t *testing.T) {
		h, err := callByID(t, p, ctx, "vault-transit:v2")
		if err != nil {
			t.Fatalf("ByID() unexpected error: %v", err)
		}
		if h.ID() != "vault-transit:v2" {
			t.Errorf("handle.ID() = %q, want %q", h.ID(), "vault-transit:v2")
		}
	})

	t.Run("wrong prefix → ErrKeyProviderKeyNotFound", func(t *testing.T) {
		_, err := callByID(t, p, ctx, "local-aes:v2")
		if err == nil {
			t.Fatal("expected error for wrong prefix, got nil")
		}
		if !errChainHasCode(err, errcode.ErrKeyProviderKeyNotFound) {
			t.Errorf("expected ErrKeyProviderKeyNotFound in error chain, got: %v", err)
		}
	})

	t.Run("empty id → ErrKeyProviderKeyNotFound", func(t *testing.T) {
		_, err := callByID(t, p, ctx, "")
		if err == nil {
			t.Fatal("expected error for empty id, got nil")
		}
	})
}

// ---------------------------------------------------------------------------
// TC-8: Rotate calls rotate endpoint and re-reads new version
// ---------------------------------------------------------------------------

func TestTransitKeyProvider_Rotate_CallsRotateAndRereads(t *testing.T) {
	// Phase 2-a will make this test green.
	fake := &fakeVaultClient{latestVersion: 3}
	p := newTestProvider(fake)

	newID, err := callRotate(t, p, context.Background())
	if err != nil {
		t.Fatalf("Rotate() unexpected error: %v", err)
	}

	// After rotate, fakeVaultClient bumps latestVersion to 4.
	if newID != "vault-transit:v4" {
		t.Errorf("Rotate() newID = %q, want %q", newID, "vault-transit:v4")
	}

	// Rotate must have issued a Write to the rotate path.
	if fake.rotateCalls.Load() != 1 {
		t.Errorf("expected 1 rotate call, got %d", fake.rotateCalls.Load())
	}
	if fake.lastWritePath != "transit/keys/gocell-config/rotate" {
		t.Errorf("rotate Write path = %q, want %q",
			fake.lastWritePath, "transit/keys/gocell-config/rotate")
	}
}

// ---------------------------------------------------------------------------
// FID-001 + FID-003: TestIsTransientVaultError_ResponseError
// Tests that real *vaultapi.ResponseError objects are correctly classified.
// ---------------------------------------------------------------------------

func TestIsTransientVaultError_ResponseError(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		wantTrans  bool
	}{
		{"503 Service Unavailable → transient", 503, true},
		{"429 Too Many Requests → transient", 429, true},
		{"408 Request Timeout → transient", 408, true},
		{"502 Bad Gateway → transient", 502, true},
		{"504 Gateway Timeout → transient", 504, true},
		// 500 is transient: Vault returns 500 during sealed / leader-election
		// windows where retrying after back-off may succeed.
		{"500 Internal Server Error → transient (vault sealed/leader-election)", 500, true},
		{"400 Bad Request → permanent", 400, false},
		{"403 Forbidden → permanent", 403, false},
		{"404 Not Found → permanent", 404, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			respErr := &vaultapi.ResponseError{StatusCode: tc.statusCode}
			got := isTransientVaultError(respErr)
			if got != tc.wantTrans {
				t.Errorf("isTransientVaultError(&ResponseError{%d}) = %v, want %v",
					tc.statusCode, got, tc.wantTrans)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FID-001 + FID-003: TestIsTransientVaultError_ContextError
// Tests that pure network/context errors are classified as transient.
// ---------------------------------------------------------------------------

func TestIsTransientVaultError_ContextError(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantTrans bool
	}{
		{
			name:      "context.DeadlineExceeded → transient",
			err:       context.DeadlineExceeded,
			wantTrans: true,
		},
		{
			name:      "context.Canceled → transient",
			err:       context.Canceled,
			wantTrans: true,
		},
		{
			name: "net.OpError → transient",
			err: &net.OpError{
				Op:  "dial",
				Net: "tcp",
				Err: fmt.Errorf("connection refused"),
			},
			wantTrans: true,
		},
		{
			name: "errcode.ErrKeyProviderEncryptFailed → permanent",
			err: errcode.New(errcode.ErrKeyProviderEncryptFailed,
				"permanent encrypt error"),
			wantTrans: false,
		},
		{
			name: "errcode.ErrKeyProviderDecryptFailed → permanent",
			err: errcode.New(errcode.ErrKeyProviderDecryptFailed,
				"permanent decrypt error"),
			wantTrans: false,
		},
		{
			name:      "errcode.ErrKeyProviderTransient → transient",
			err:       errcode.New(errcode.ErrKeyProviderTransient, "rate limited"),
			wantTrans: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isTransientVaultError(tc.err)
			if got != tc.wantTrans {
				t.Errorf("isTransientVaultError(%v) = %v, want %v",
					tc.err, got, tc.wantTrans)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FID-009: TestParseVaultKeyID — boundary case coverage for parseVaultKeyID
// ---------------------------------------------------------------------------

func TestParseVaultKeyID(t *testing.T) {
	cases := []struct {
		name       string
		ciphertext string
		wantKeyID  string
		wantErr    bool
	}{
		{
			name:       "valid v3 → vault-transit:v3",
			ciphertext: "vault:v3:SGVsbG9Xb3JsZA==",
			wantKeyID:  "vault-transit:v3",
		},
		{
			name:       "valid v1 → vault-transit:v1",
			ciphertext: "vault:v1:AAAA",
			wantKeyID:  "vault-transit:v1",
		},
		{
			name:       "empty string → error",
			ciphertext: "",
			wantErr:    true,
		},
		{
			name:       "only two segments → error",
			ciphertext: "vault:v1",
			wantErr:    true,
		},
		{
			name:       "bad prefix → error",
			ciphertext: "badprefix:v1:data",
			wantErr:    true,
		},
		{
			name:       "version without v prefix → error",
			ciphertext: "vault:notv:data",
			wantErr:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseVaultKeyID(tc.ciphertext, errcode.ErrKeyProviderEncryptFailed)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseVaultKeyID(%q) expected error, got nil (keyID=%q)", tc.ciphertext, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVaultKeyID(%q) unexpected error: %v", tc.ciphertext, err)
			}
			if got != tc.wantKeyID {
				t.Errorf("parseVaultKeyID(%q) = %q, want %q", tc.ciphertext, got, tc.wantKeyID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// A13: Token LifetimeWatcher tests
// ---------------------------------------------------------------------------

// fakeTokenWatcher is a controllable fake for the tokenWatcher interface.
// Channels are buffered to avoid goroutine leaks in tests.
type fakeTokenWatcher struct {
	doneCh    chan error
	renewCh   chan *vaultapi.RenewOutput
	startedCh chan struct{} // closed when Start() is called
	stopped   atomic.Bool
}

func newFakeTokenWatcher() *fakeTokenWatcher {
	return &fakeTokenWatcher{
		doneCh:    make(chan error, 1),
		renewCh:   make(chan *vaultapi.RenewOutput, 4),
		startedCh: make(chan struct{}),
	}
}

func (f *fakeTokenWatcher) Start() { close(f.startedCh) }
func (f *fakeTokenWatcher) Stop()  { f.stopped.Store(true) }

func (f *fakeTokenWatcher) DoneCh() <-chan error {
	return f.doneCh
}

func (f *fakeTokenWatcher) RenewCh() <-chan *vaultapi.RenewOutput {
	return f.renewCh
}

// TestTransitKeyProvider_Worker_NilWhenNoRenewal verifies that the default
// constructor (NewTransitKeyProvider) returns nil Worker — no background
// goroutine needed when no TokenRenewer is available.
func TestTransitKeyProvider_Worker_NilWhenNoRenewal(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 1}
	p := NewTransitKeyProvider(fake, "transit", "gocell-config")

	if p.Worker() != nil {
		t.Error("Worker() must return nil when no renewal worker is configured")
	}
}

// TestTransitKeyProvider_Worker_NonNilWhenRenewalConfigured verifies that
// Worker() returns the configured renewal worker when one is set.
func TestTransitKeyProvider_Worker_NonNilWhenRenewalConfigured(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 1}
	p := NewTransitKeyProvider(fake, "transit", "gocell-config")

	fw := newFakeTokenWatcher()
	p.renewalWorker = &tokenRenewalWorker{watcher: fw}

	if p.Worker() == nil {
		t.Error("Worker() must return non-nil when renewalWorker is set")
	}
}

// TestTransitKeyProvider_RenewalMetrics_NilWhenNoRenewal verifies that
// RenewalMetrics returns nil when no renewal worker is configured.
func TestTransitKeyProvider_RenewalMetrics_NilWhenNoRenewal(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 1}
	p := NewTransitKeyProvider(fake, "transit", "gocell-config")

	if got := p.RenewalMetrics(); got != nil {
		t.Errorf("RenewalMetrics() = %v, want nil when no renewal worker configured", got)
	}
}

// TestTransitKeyProvider_RenewalMetrics_ReturnsTwoCollectors verifies that
// RenewalMetrics returns exactly two collectors (success, failure) when a
// renewal worker with counters is configured.
func TestTransitKeyProvider_RenewalMetrics_ReturnsTwoCollectors(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 1}
	p := NewTransitKeyProvider(fake, "transit", "gocell-config")

	successCtr, failureCtr := newRenewalCounters()
	fw := newFakeTokenWatcher()
	p.renewalWorker = &tokenRenewalWorker{
		watcher:      fw,
		renewSuccess: successCtr,
		renewFailure: failureCtr,
	}

	got := p.RenewalMetrics()
	if len(got) != 2 {
		t.Errorf("RenewalMetrics() returned %d collectors, want 2", len(got))
	}
}

// TestTokenRenewalWorker_Start_StopsOnContextCancel verifies that Start()
// returns nil when its context is cancelled (graceful shutdown path).
func TestTokenRenewalWorker_Start_StopsOnContextCancel(t *testing.T) {
	fw := newFakeTokenWatcher()
	w := &tokenRenewalWorker{
		watcher: fw,
		logger:  slog.Default(),
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Wait until the watcher's Start goroutine has been scheduled before
	// cancelling — ensures the assertion below is race-free.
	select {
	case <-fw.startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher.Start() was not called within 2s")
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() returned error on ctx cancel, want nil: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not return after context cancel")
	}

	if !fw.stopped.Load() {
		t.Error("Start() must call watcher.Stop() on exit")
	}
}

// TestTokenRenewalWorker_Start_HandlesRenewalNotification verifies that
// renewal notifications are consumed and logged without blocking Start.
// Also verifies that a nil renewal is handled gracefully (no panic, no stop).
func TestTokenRenewalWorker_Start_HandlesRenewalNotification(t *testing.T) {
	t.Run("valid renewal consumed without error", func(t *testing.T) {
		fw := newFakeTokenWatcher()
		w := &tokenRenewalWorker{
			watcher: fw,
			logger:  slog.Default(),
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan error, 1)
		go func() {
			done <- w.Start(ctx)
		}()

		select {
		case <-fw.startedCh:
		case <-time.After(2 * time.Second):
			t.Fatal("watcher.Start() was not called within 2s")
		}

		fw.renewCh <- &vaultapi.RenewOutput{
			Secret: &vaultapi.Secret{
				Auth: &vaultapi.SecretAuth{
					LeaseDuration: 3600,
				},
			},
		}

		cancel()

		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Start() returned error on renewal+cancel, want nil: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Start() did not return after context cancel")
		}
	})

	t.Run("nil renewal handled gracefully (F7)", func(t *testing.T) {
		fw := newFakeTokenWatcher()
		w := &tokenRenewalWorker{
			watcher: fw,
			logger:  slog.Default(),
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan error, 1)
		go func() {
			done <- w.Start(ctx)
		}()

		select {
		case <-fw.startedCh:
		case <-time.After(2 * time.Second):
			t.Fatal("watcher.Start() was not called within 2s")
		}

		// Send nil renewal — must not panic, must not stop Start.
		fw.renewCh <- nil

		cancel()

		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Start() returned error after nil renewal+cancel, want nil: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Start() did not return after context cancel")
		}
	})
}

// TestTokenRenewalWorker_Start_ChannelClosed verifies that Start returns nil
// when DoneCh is closed externally (channel closed = watcher stopped externally).
func TestTokenRenewalWorker_Start_ChannelClosed(t *testing.T) {
	fw := newFakeTokenWatcher()
	w := &tokenRenewalWorker{
		watcher: fw,
		logger:  slog.Default(),
	}

	ctx := context.Background()

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Close the channel (not send on it) to simulate external watcher stop.
	close(fw.doneCh)

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() on closed DoneCh must return nil, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not return after DoneCh closed")
	}
}

// TestTokenRenewalWorker_Start_HandlesDone verifies that Start returns a
// non-nil ErrKeyProviderAuthFailed when the watcher signals the token is
// no longer renewable (nil error on DoneCh = operational alert, not graceful).
func TestTokenRenewalWorker_Start_HandlesDone(t *testing.T) {
	fw := newFakeTokenWatcher()
	w := &tokenRenewalWorker{
		watcher: fw,
		logger:  slog.Default(),
	}

	ctx := context.Background()

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Signal watcher done with nil error (token no longer renewable).
	// F3: this must return a non-nil ErrKeyProviderAuthFailed, not nil.
	fw.doneCh <- nil

	select {
	case err := <-done:
		if err == nil {
			t.Error("Start() on nil DoneCh must return non-nil error (token no longer renewable)")
		}
		if !errChainHasCode(err, errcode.ErrKeyProviderAuthFailed) {
			t.Errorf("expected ErrKeyProviderAuthFailed in error chain, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not return after DoneCh fired")
	}

	if !fw.stopped.Load() {
		t.Error("Start() must call watcher.Stop() on DoneCh exit")
	}
}

// TestTokenRenewalWorker_Start_HandlesDoneWithError verifies that Start
// propagates a non-nil error from the done channel.
func TestTokenRenewalWorker_Start_HandlesDoneWithError(t *testing.T) {
	fw := newFakeTokenWatcher()
	w := &tokenRenewalWorker{
		watcher: fw,
		logger:  slog.Default(),
	}

	ctx := context.Background()

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	injectedErr := errcode.New(errcode.ErrKeyProviderTransient, "vault: token renewal failed")
	fw.doneCh <- injectedErr

	select {
	case err := <-done:
		if err == nil {
			t.Error("Start() must propagate non-nil DoneCh error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not return after DoneCh fired with error")
	}
}

// TestTokenRenewalWorker_Stop verifies that Stop calls watcher.Stop().
func TestTokenRenewalWorker_Stop(t *testing.T) {
	fw := newFakeTokenWatcher()
	w := &tokenRenewalWorker{
		watcher: fw,
		logger:  slog.Default(),
	}

	if err := w.Stop(context.Background()); err != nil {
		t.Errorf("Stop() returned unexpected error: %v", err)
	}
	if !fw.stopped.Load() {
		t.Error("Stop() must call watcher.Stop()")
	}
}

// TestTokenRenewalWorker_Stop_Idempotent verifies that calling Stop twice does
// not panic or produce errors (F2: double-stop during shutdown).
func TestTokenRenewalWorker_Stop_Idempotent(t *testing.T) {
	fw := newFakeTokenWatcher()
	w := &tokenRenewalWorker{
		watcher: fw,
		logger:  slog.Default(),
	}

	ctx := context.Background()
	if err := w.Stop(ctx); err != nil {
		t.Errorf("Stop() first call returned unexpected error: %v", err)
	}
	if err := w.Stop(ctx); err != nil {
		t.Errorf("Stop() second call returned unexpected error: %v", err)
	}
	if !fw.stopped.Load() {
		t.Error("Stop() must have called watcher.Stop()")
	}
}

// TestTransitKeyProvider_Close_StopsRenewalWorker verifies that Close()
// stops the renewal worker when one is configured.
func TestTransitKeyProvider_Close_StopsRenewalWorker(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 1}
	p := NewTransitKeyProvider(fake, "transit", "gocell-config")

	fw := newFakeTokenWatcher()
	p.renewalWorker = &tokenRenewalWorker{watcher: fw}

	if err := p.Close(context.Background()); err != nil {
		t.Errorf("Close() returned unexpected error: %v", err)
	}
	if !fw.stopped.Load() {
		t.Error("Close() must call watcher.Stop() when renewalWorker is set")
	}
}

// TestVaultAPIClient_ImplementsTokenRenewer is a compile-time assertion that
// vaultAPIClient satisfies the TokenRenewer interface.
// The actual interface is in transit_provider.go; this test fails to compile
// if the implementation is missing or mismatched.
func TestVaultAPIClient_ImplementsTokenRenewer(t *testing.T) {
	// This is a compile-time check only; no runtime assertions needed.
	var _ TokenRenewer = (*vaultAPIClient)(nil)
}

// ---------------------------------------------------------------------------
// FID-010: TestTransitKeyProvider_ConcurrentEncryptRotate — race detector test
// ---------------------------------------------------------------------------

func TestTransitKeyProvider_ConcurrentEncryptRotate(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 1}
	p := newTestProvider(fake)

	ctx := context.Background()
	const encryptWorkers = 8
	const rotations = 5

	var wg sync.WaitGroup

	// 8 goroutines concurrently encrypting.
	for range encryptWorkers {
		wg.Go(func() {
			for range 20 {
				h, err := p.Current(ctx)
				if err != nil {
					return
				}
				vh, ok := h.(*vaultTransitHandle)
				if !ok {
					return
				}
				// Encrypt may fail transiently during rotation — that is fine.
				vh.Encrypt(ctx, []byte("payload"), []byte("aad")) //nolint:errcheck
			}
		})
	}

	// 1 goroutine doing periodic rotations.
	wg.Go(func() {
		for range rotations {
			p.Rotate(ctx) //nolint:errcheck
		}
	})

	wg.Wait()
	// No race detector report = pass. The test itself needs -race to be meaningful.
}

// ---------------------------------------------------------------------------
// F4+F5: initTokenRenewal error code tests
// ---------------------------------------------------------------------------

// fakeTokenRenewer implements both VaultClient and TokenRenewer for testing
// initTokenRenewal. LookupSelfToken and NewLifetimeWatcher errors are injectable.
type fakeTokenRenewer struct {
	fakeVaultClient
	lookupErr     error
	newWatcherErr error
	lookupSecret  *vaultapi.Secret
}

var _ TokenRenewer = (*fakeTokenRenewer)(nil)

func (f *fakeTokenRenewer) LookupSelfToken(_ context.Context) (*vaultapi.Secret, error) {
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	if f.lookupSecret != nil {
		return f.lookupSecret, nil
	}
	return &vaultapi.Secret{}, nil
}

func (f *fakeTokenRenewer) NewLifetimeWatcher(_ *vaultapi.LifetimeWatcherInput) (*vaultapi.LifetimeWatcher, error) {
	if f.newWatcherErr != nil {
		return nil, f.newWatcherErr
	}
	// Return nil watcher — caller (initTokenRenewal) will panic if it dereferences it.
	// Tests that exercise the happy path must not call this.
	return nil, nil
}

// TestInitTokenRenewal_LookupFails_ReturnsAuthError verifies that a
// LookupSelfToken failure results in ErrKeyProviderAuthFailed, not
// ErrKeyProviderTransient (F4: wrong error code at startup).
func TestInitTokenRenewal_LookupFails_ReturnsAuthError(t *testing.T) {
	injectedErr := fmt.Errorf("vault: 403 forbidden")
	fake := &fakeTokenRenewer{
		fakeVaultClient: fakeVaultClient{latestVersion: 1},
		lookupErr:       injectedErr,
	}

	p := NewTransitKeyProvider(fake, "transit", "gocell-config")
	err := p.initTokenRenewal(context.Background())

	if err == nil {
		t.Fatal("initTokenRenewal must return error when LookupSelfToken fails")
	}
	if !errChainHasCode(err, errcode.ErrKeyProviderAuthFailed) {
		t.Errorf("expected ErrKeyProviderAuthFailed in error chain, got: %v", err)
	}
	if errChainHasCode(err, errcode.ErrKeyProviderTransient) {
		t.Errorf("must NOT return ErrKeyProviderTransient for LookupSelf failure; got: %v", err)
	}
}

// TestInitTokenRenewal_NewWatcherFails_ReturnsAuthError verifies that a
// NewLifetimeWatcher failure also results in ErrKeyProviderAuthFailed (F4).
func TestInitTokenRenewal_NewWatcherFails_ReturnsAuthError(t *testing.T) {
	injectedErr := fmt.Errorf("vault: create watcher: invalid secret")
	fake := &fakeTokenRenewer{
		fakeVaultClient: fakeVaultClient{latestVersion: 1},
		newWatcherErr:   injectedErr,
	}

	p := NewTransitKeyProvider(fake, "transit", "gocell-config")
	err := p.initTokenRenewal(context.Background())

	if err == nil {
		t.Fatal("initTokenRenewal must return error when NewLifetimeWatcher fails")
	}
	if !errChainHasCode(err, errcode.ErrKeyProviderAuthFailed) {
		t.Errorf("expected ErrKeyProviderAuthFailed in error chain, got: %v", err)
	}
}
