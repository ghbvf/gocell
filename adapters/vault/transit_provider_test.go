package vault

// TDD RED — Phase 1-a envelope contract tests.
//
// All tests in this file MUST FAIL until Phase 2-a implements
// vaultTransitHandle.Encrypt / Decrypt / TransitKeyProvider.Current / ByID / Rotate.
// The bodies in transit_provider.go currently panic("not implemented: R1c Phase 2-a").
//
// Test coverage contract (Phase 2-a will make these green):
//   TC-1  Encrypt calls local AES-GCM and wraps DEK via Vault
//   TC-2  Decrypt round-trip (Encrypt → Decrypt → original plaintext)
//   TC-3  AAD mismatch fails closed (ErrKeyProviderDecryptFailed)
//   TC-4  Vault server error → transient / permanent classification
//   TC-5  keyID parsed from edk prefix, not from handle.id
//   TC-6  Current reads latest_version from Vault key metadata
//   TC-7  ByID validates prefix; wrong prefix → ErrKeyProviderKeyNotFound
//   TC-8  Rotate calls rotate endpoint and re-reads new version
//
// ref: kubernetes/kubernetes staging/src/k8s.io/apiserver/pkg/storage/value/encrypt/
//      envelope/kmsv2/envelope_test.go@master

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

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

// mustCurrent calls Current() on the provider and returns the *vaultTransitHandle.
// If Current() panics (Phase 2-a not yet implemented) or returns an error, the
// test is immediately failed via t.Fatal — this converts a panic into a clean
// FAIL so the test runner can continue to the next test case.
func mustCurrent(t *testing.T, p *TransitKeyProvider) (handle *vaultTransitHandle) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Current() panicked (not implemented): %v", r)
		}
	}()
	h, err := p.Current(context.Background())
	if err != nil {
		t.Fatalf("Current() unexpected error: %v", err)
	}
	var ok bool
	handle, ok = h.(*vaultTransitHandle)
	if !ok {
		t.Fatalf("Current() returned non-*vaultTransitHandle: %T", h)
	}
	return handle
}

// callEncrypt wraps h.Encrypt to convert a panic into t.Fatal (Phase 2-a RED state).
func callEncrypt(t *testing.T, h *vaultTransitHandle, ctx context.Context, plaintext, aad []byte) (ct, nonce, edk []byte, keyID string, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Encrypt() panicked (not implemented): %v", r)
		}
	}()
	return h.Encrypt(ctx, plaintext, aad)
}

// callDecrypt wraps h.Decrypt to convert a panic into t.Fatal (Phase 2-a RED state).
func callDecrypt(t *testing.T, h *vaultTransitHandle, ctx context.Context, ct, nonce, edk, aad []byte) (plaintext []byte, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Decrypt() panicked (not implemented): %v", r)
		}
	}()
	return h.Decrypt(ctx, ct, nonce, edk, aad)
}

// callRotate wraps p.Rotate to convert a panic into t.Fatal (Phase 2-a RED state).
func callRotate(t *testing.T, p *TransitKeyProvider, ctx context.Context) (newID string, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Rotate() panicked (not implemented): %v", r)
		}
	}()
	return p.Rotate(ctx)
}

// callByID wraps p.ByID to convert a panic into t.Fatal (Phase 2-a RED state).
func callByID(t *testing.T, p *TransitKeyProvider, ctx context.Context, id string) (h interface{ ID() string }, err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ByID() panicked (not implemented): %v", r)
		}
	}()
	return p.ByID(ctx, id)
}

// ---------------------------------------------------------------------------
// TC-1: Encrypt calls local AES-GCM and wraps DEK
// ---------------------------------------------------------------------------

func TestVaultTransitHandle_Encrypt_CallsLocalAESAndWrapsDEK(t *testing.T) {
	// Phase 2-a will make this test green.
	// Currently panics: "not implemented: R1c Phase 2-a"
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

	var ec *errcode.Error
	found := false
	for err := decErr; err != nil; {
		if e, ok := err.(*errcode.Error); ok {
			ec = e
			found = true
			break
		}
		if unwrapper, ok := err.(interface{ Unwrap() error }); ok {
			err = unwrapper.Unwrap()
		} else {
			break
		}
	}
	if !found || ec.Code != errcode.ErrKeyProviderDecryptFailed {
		t.Errorf("expected ErrKeyProviderDecryptFailed in error chain, got: %v", decErr)
	}
}

// ---------------------------------------------------------------------------
// TC-4: Vault server errors classified transient vs permanent
// ---------------------------------------------------------------------------

func TestVaultTransitHandle_VaultServerError_ClassifiedTransient(t *testing.T) {
	// Phase 2-a will make this test green.
	ctx := context.Background()

	transientCases := []struct {
		name     string
		vaultErr error
	}{
		{
			name: "503 Service Unavailable",
			vaultErr: errcode.New(errcode.ErrKeyProviderTransient,
				"vault: 503 service unavailable"),
		},
		{
			name: "429 Too Many Requests",
			vaultErr: errcode.New(errcode.ErrKeyProviderTransient,
				"vault: 429 rate limited"),
		},
		{
			name: "408 Request Timeout",
			vaultErr: errcode.New(errcode.ErrKeyProviderTransient,
				"vault: 408 request timeout"),
		},
	}

	for _, tc := range transientCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeVaultClient{latestVersion: 1, writeErr: tc.vaultErr}
			p := newTestProvider(fake)
			h := mustCurrent(t, p)

			_, _, _, _, err := callEncrypt(t, h, ctx, []byte("payload"), []byte("aad"))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errcode.IsTransient(err) {
				t.Errorf("expected IsTransient=true for %s, err=%v", tc.name, err)
			}
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
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeVaultClient{latestVersion: 1, writeErr: tc.vaultErr}
			p := newTestProvider(fake)
			h := mustCurrent(t, p)

			_, _, _, _, err := callEncrypt(t, h, ctx, []byte("payload"), []byte("aad"))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if errcode.IsTransient(err) {
				t.Errorf("expected IsTransient=false for %s, err=%v", tc.name, err)
			}
			// Error chain must contain the permanent errcode.
			var ec *errcode.Error
			found := false
			for e := err; e != nil; {
				if ecErr, ok := e.(*errcode.Error); ok {
					if ecErr.Code == tc.wantCode {
						ec = ecErr
						found = true
						break
					}
				}
				if unwrapper, ok := e.(interface{ Unwrap() error }); ok {
					e = unwrapper.Unwrap()
				} else {
					break
				}
			}
			if !found {
				t.Errorf("error chain must contain %s, err=%v (ec=%v)", tc.wantCode, err, ec)
			}
		})
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
		var ec *errcode.Error
		found := false
		for e := err; e != nil; {
			if ecErr, ok := e.(*errcode.Error); ok {
				if ecErr.Code == errcode.ErrKeyProviderKeyNotFound {
					found = true
					break
				}
			}
			if unwrapper, ok := e.(interface{ Unwrap() error }); ok {
				e = unwrapper.Unwrap()
			} else {
				break
			}
		}
		if !found {
			t.Errorf("expected ErrKeyProviderKeyNotFound in error chain, got: %v (ec=%v)", err, ec)
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
		tc := tc
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
		tc := tc
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
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseVaultKeyID(tc.ciphertext)
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
	for i := 0; i < encryptWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
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
		}()
	}

	// 1 goroutine doing periodic rotations.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rotations; i++ {
			p.Rotate(ctx) //nolint:errcheck
		}
	}()

	wg.Wait()
	// No race detector report = pass. The test itself needs -race to be meaningful.
}
