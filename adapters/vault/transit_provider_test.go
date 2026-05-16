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
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	vaultapi "github.com/hashicorp/vault/api"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

const transitProviderD45s = 45 * time.Second

// testReauthCredential is a fixture client credential value used in fakeAuthMethod test doubles.
const testReauthCredential = "re-auth-fixture"

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
	//
	// encryptCalls counts hits on the legacy /transit/encrypt/{key} path.
	// Production Encrypt routes through /datakey/plaintext, so this counter
	// doubles as a regression guard: tests assert it stays 0 unless they
	// intentionally exercise the legacy ResponseError classification path.
	// datakeyCalls counts hits on /transit/datakey/plaintext/{key}.
	// readCalls counts hits on the Read method (transit/keys/{key} metadata).
	encryptCalls atomic.Int64
	datakeyCalls atomic.Int64
	decryptCalls atomic.Int64
	rotateCalls  atomic.Int64
	readCalls    atomic.Int64
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

	case strings.Contains(path, "/datakey/plaintext/"):
		// Vault Transit datakey/plaintext: server generates a fresh DEK, returns
		// both the raw plaintext (for immediate use) and the wrapped ciphertext
		// (for storage). Deterministic DEK keyed by the requested bit size so
		// round-trip tests can decrypt without a separate channel.
		f.datakeyCalls.Add(1)
		bits := 32 // default
		if b, ok := data["bits"].(int); ok {
			bits = b / 8
		}
		dek := make([]byte, bits)
		for i := range dek {
			dek[i] = byte((0x77 ^ i) & 0xff)
		}
		wrapped := xorBytes(dek, f.masterKey[:len(dek)])
		vaultCipher := fmt.Sprintf("vault:v%d:%s",
			f.latestVersion,
			base64.StdEncoding.EncodeToString(wrapped),
		)
		return map[string]any{
			"plaintext":  base64.StdEncoding.EncodeToString(dek),
			"ciphertext": vaultCipher,
		}, nil

	default:
		// Assume transit/encrypt/{key} — legacy path, no longer reachable from
		// production Encrypt (which routes through datakey/plaintext) but kept
		// so legacy ResponseError tests can still exercise the fake.
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

	f.readCalls.Add(1)
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
		return nil, errcode.New(errcode.KindInternal, errcode.ErrKeyProviderDecryptFailed,
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
// Tests that need fully custom Write behavior embed this.
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
// Uses NewStaticTokenAuth(nil, "test-token") as the auth method — no real Vault
// required since fakeVaultClient does not implement TokenRenewer and the token
// is non-renewable, so no renewal worker is started.
func newTestProvider(t *testing.T, fake *fakeVaultClient) *TransitKeyProvider {
	t.Helper()
	p, err := NewTransitKeyProvider(context.Background(), fake,
		"transit", "gocell-config", NewStaticTokenAuth(nil, "test-token"), clock.Real())
	if err != nil {
		t.Fatalf("NewTransitKeyProvider: %v", err)
	}
	return p
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

// callEncrypt is a typed facade over h.Encrypt that unpacks EncryptResult so
// older assertion-heavy tests stay concise.
func callEncrypt(
	t *testing.T, h *vaultTransitHandle, ctx context.Context, plaintext, aad []byte,
) (ct, nonce, edk []byte, keyID string, err error) {
	t.Helper()
	result, err := h.Encrypt(ctx, plaintext, aad)
	return result.Ciphertext, result.Nonce, result.EDK, result.KeyID, err
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
		var ecErr *errcode.Error
		if errors.As(e, &ecErr) && ecErr.Code == want {
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
	p := newTestProvider(t, fake)
	h := mustCurrent(t, p)

	ctx := context.Background()
	ct, nonce, edk, keyID, err := callEncrypt(t, h, ctx, []byte("secret"), []byte("row:123"))

	// (a) fake client received exactly one Write call on the datakey path;
	//     the legacy /transit/encrypt path must remain untouched.
	if fake.datakeyCalls.Load() != 1 {
		t.Errorf("expected 1 datakey call, got %d", fake.datakeyCalls.Load())
	}
	if fake.encryptCalls.Load() != 0 {
		t.Errorf("legacy /encrypt path must not be called by production Encrypt, got %d hits", fake.encryptCalls.Load())
	}
	if fake.lastWritePath != "transit/datakey/plaintext/gocell-config" {
		t.Errorf("wrong Write path: %q, want %q", fake.lastWritePath, "transit/datakey/plaintext/gocell-config")
	}

	// (b) Write body has bits=256 (server-side DEK generation via datakey/plaintext)
	if bits := fake.lastWriteData["bits"]; bits != 256 {
		t.Errorf("Write data 'bits' = %v, want 256", bits)
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
	p := newTestProvider(t, fake)
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
	if !bytes.Equal(got, plaintext) {
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
	p := newTestProvider(t, fake)
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
	p := newTestProvider(t, fake)
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
			vaultErr: errcode.WrapInfra(errcode.ErrKeyProviderTransient, "vault: 503 service unavailable", nil),
		},
		{
			name:     "429 Too Many Requests",
			vaultErr: errcode.WrapInfra(errcode.ErrKeyProviderTransient, "vault: 429 rate limited", nil),
		},
		{
			name:     "408 Request Timeout",
			vaultErr: errcode.WrapInfra(errcode.ErrKeyProviderTransient, "vault: 408 request timeout", nil),
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
			vaultErr: errcode.New(errcode.KindInternal, errcode.ErrKeyProviderEncryptFailed, "vault: 400 bad request"),
			wantCode: errcode.ErrKeyProviderEncryptFailed,
		},
		{
			name:     "403 Forbidden → encrypt failed",
			vaultErr: errcode.New(errcode.KindInternal, errcode.ErrKeyProviderEncryptFailed, "vault: 403 forbidden"),
			wantCode: errcode.ErrKeyProviderEncryptFailed,
		},
		{
			name:     "404 Not Found → encrypt failed",
			vaultErr: errcode.New(errcode.KindInternal, errcode.ErrKeyProviderEncryptFailed, "vault: 404 not found"),
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
	p := newTestProvider(t, fake)
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
		// Production Encrypt routes through /datakey/plaintext, so this override
		// bumps datakeyCalls (not the legacy encryptCalls regression-guard
		// counter). Returns v7 prefix regardless of latestVersion to simulate a
		// rotate race between Current() and the Encrypt round-trip.
		override.datakeyCalls.Add(1)
		dek := make([]byte, 32)
		for i := range dek {
			dek[i] = byte((0x77 ^ i) & 0xff)
		}
		wrapped := xorBytes(dek, override.masterKey[:len(dek)])
		return map[string]any{
			"plaintext":  base64.StdEncoding.EncodeToString(dek),
			"ciphertext": "vault:v7:" + base64.StdEncoding.EncodeToString(wrapped),
		}, nil
	}

	p, err2 := NewTransitKeyProvider(context.Background(), override,
		"transit", "gocell-config", NewStaticTokenAuth(nil, "test-token"), clock.Real())
	if err2 != nil {
		t.Fatalf("NewTransitKeyProvider: %v", err2)
	}
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
	p := newTestProvider(t, fake)

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
	p := newTestProvider(t, fake)

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
	p := newTestProvider(t, fake)

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
			// Post-206 unified semantic: context.Canceled is NOT transient
			// (the caller gave up; retrying is pointless) — consistent with
			// errcode.IsTransient and grpc-ecosystem retry defaults. Falls
			// through step-4 (not a net error) → permanent.
			name:      "context.Canceled → NOT transient (caller gave up)",
			err:       context.Canceled,
			wantTrans: false,
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
			err: errcode.New(errcode.KindInternal, errcode.ErrKeyProviderEncryptFailed,
				"permanent encrypt error"),
			wantTrans: false,
		},
		{
			name: "errcode.ErrKeyProviderDecryptFailed → permanent",
			err: errcode.New(errcode.KindInternal, errcode.ErrKeyProviderDecryptFailed,
				"permanent decrypt error"),
			wantTrans: false,
		},
		{
			name:      "errcode.ErrKeyProviderTransient → transient",
			err:       errcode.WrapInfra(errcode.ErrKeyProviderTransient, "rate limited", nil),
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
		{
			name:       "empty version after v → error",
			ciphertext: "vault:v:data",
			wantErr:    true,
		},
		{
			name:       "non-numeric version → error",
			ciphertext: "vault:vx:data",
			wantErr:    true,
		},
		{
			name:       "signed version → error",
			ciphertext: "vault:v-1:data",
			wantErr:    true,
		},
		{
			name:       "zero version → error",
			ciphertext: "vault:v0:data",
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
	p, err := NewTransitKeyProvider(context.Background(), fake,
		"transit", "gocell-config", NewStaticTokenAuth(nil, "test-token"), clock.Real())
	if err != nil {
		t.Fatalf("NewTransitKeyProvider: %v", err)
	}

	if p.Worker() != nil {
		t.Error("Worker() must return nil when no renewal worker is configured")
	}
}

// TestTransitKeyProvider_Worker_NonNilWhenRenewalConfigured verifies that
// Worker() returns the configured renewal worker when one is set.
func TestTransitKeyProvider_Worker_NonNilWhenRenewalConfigured(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 1}
	p, err := NewTransitKeyProvider(context.Background(), fake,
		"transit", "gocell-config", NewStaticTokenAuth(nil, "test-token"), clock.Real())
	if err != nil {
		t.Fatalf("NewTransitKeyProvider: %v", err)
	}

	fw := newFakeTokenWatcher()
	p.renewalWorker = &tokenRenewalWorker{currentWatcher: fw, clock: clock.Real()}

	if p.Worker() == nil {
		t.Error("Worker() must return non-nil when renewalWorker is set")
	}
}

// TestTransitKeyProvider_RenewalMetrics_NilWhenNoRenewal verifies that
// RenewalMetrics returns nil when no renewal worker is configured.
func TestTransitKeyProvider_RenewalMetrics_NilWhenNoRenewal(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 1}
	p, err := NewTransitKeyProvider(context.Background(), fake,
		"transit", "gocell-config", NewStaticTokenAuth(nil, "test-token"), clock.Real())
	if err != nil {
		t.Fatalf("NewTransitKeyProvider: %v", err)
	}

	if got := p.RenewalMetrics(); got != nil {
		t.Errorf("RenewalMetrics() = %v, want nil when no renewal worker configured", got)
	}
}

// TestTransitKeyProvider_RenewalMetrics_ReturnsTwoCollectors verifies that
// RenewalMetrics returns at least two collectors (success, failure) when a
// renewal worker with counters is configured.
func TestTransitKeyProvider_RenewalMetrics_ReturnsTwoCollectors(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 1}
	p, err := NewTransitKeyProvider(context.Background(), fake,
		"transit", "gocell-config", NewStaticTokenAuth(nil, "test-token"), clock.Real())
	if err != nil {
		t.Fatalf("NewTransitKeyProvider: %v", err)
	}

	successCtr, failureCtr := newRenewalCounters()
	fw := newFakeTokenWatcher()
	p.renewalWorker = &tokenRenewalWorker{
		currentWatcher: fw,
		renewSuccess:   successCtr,
		renewFailure:   failureCtr,
		clock:          clock.Real(),
	}

	got := p.RenewalMetrics()
	if len(got) < 2 {
		t.Errorf("RenewalMetrics() returned %d collectors, want >= 2", len(got))
	}
}

// TestTokenRenewalWorker_Start_StopsOnContextCancel verifies that Start()
// returns nil when its context is canceled (graceful shutdown path).
func TestTokenRenewalWorker_Start_StopsOnContextCancel(t *testing.T) {
	fw := newFakeTokenWatcher()
	// fakeAuthMethod that always succeeds — needed if reauthenticate is triggered.
	fakeAuth := &fakeAuthMethod{method: MethodAppRole, results: []AuthResult{
		{ClientToken: testReauthCredential, Renewable: true},
	}}
	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		clock:          clock.Real(),
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Wait until the watcher's Start goroutine has been scheduled before
	// canceling — ensures the assertion below is race-free.
	select {
	case <-fw.startedCh:
	case <-time.After(testtime.D2s):
		t.Fatal("watcher.Start() was not called within 2s")
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() returned error on ctx cancel, want nil: %v", err)
		}
	case <-time.After(testtime.D2s):
		t.Fatal("Start() did not return after context cancel")
	}
}

// TestTokenRenewalWorker_Start_HandlesRenewalNotification verifies that
// renewal notifications are consumed and logged without blocking Start.
// Also verifies that a nil renewal is handled gracefully (no panic, no stop).
func TestTokenRenewalWorker_Start_HandlesRenewalNotification(t *testing.T) {
	t.Run("valid renewal consumed without error", func(t *testing.T) {
		fw := newFakeTokenWatcher()
		fakeAuth := &fakeAuthMethod{method: MethodAppRole, results: []AuthResult{
			{ClientToken: testReauthCredential, Renewable: true},
		}}
		w := &tokenRenewalWorker{
			currentWatcher: fw,
			authMethod:     fakeAuth,
			logger:         slog.Default(),
			clock:          clock.Real(),
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan error, 1)
		go func() {
			done <- w.Start(ctx)
		}()

		select {
		case <-fw.startedCh:
		case <-time.After(testtime.D2s):
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
		case <-time.After(testtime.D2s):
			t.Fatal("Start() did not return after context cancel")
		}
	})

	t.Run("nil renewal handled gracefully (F7)", func(t *testing.T) {
		fw := newFakeTokenWatcher()
		fakeAuth := &fakeAuthMethod{method: MethodAppRole, results: []AuthResult{
			{ClientToken: testReauthCredential, Renewable: true},
		}}
		w := &tokenRenewalWorker{
			currentWatcher: fw,
			authMethod:     fakeAuth,
			logger:         slog.Default(),
			clock:          clock.Real(),
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		done := make(chan error, 1)
		go func() {
			done <- w.Start(ctx)
		}()

		select {
		case <-fw.startedCh:
		case <-time.After(testtime.D2s):
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
		case <-time.After(testtime.D2s):
			t.Fatal("Start() did not return after context cancel")
		}
	})
}

// TestTokenRenewalWorker_Start_ChannelClosed verifies that Start returns nil
// when DoneCh is closed externally (channel closed = watcher stopped externally).
func TestTokenRenewalWorker_Start_ChannelClosed(t *testing.T) {
	fw := newFakeTokenWatcher()
	fakeAuth := &fakeAuthMethod{method: MethodAppRole, results: []AuthResult{
		{ClientToken: testReauthCredential, Renewable: true},
	}}
	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		clock:          clock.Real(),
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
	case <-time.After(testtime.D2s):
		t.Fatal("Start() did not return after DoneCh closed")
	}
}

// TestTokenRenewalWorker_Start_HandlesDone verifies that when DoneCh fires with
// nil error, the worker attempts re-auth. With a ctx that is canceled immediately
// after DoneCh, Start must return nil (ctx cancel exits cleanly).
//
// NOTE: In the new re-auth design, DoneCh does NOT cause Start to return an error
// immediately. Instead, it triggers reauthenticate() with exponential backoff.
// ctx cancellation during re-auth causes Start to return nil.
func TestTokenRenewalWorker_Start_HandlesDone(t *testing.T) {
	fw := newFakeTokenWatcher()
	// fakeAuth returns a cancellable error — blocked on ctx.
	fakeAuth := &fakeAuthMethod{
		method: MethodAppRole,
		errs:   []error{errcode.New(errcode.KindUnavailable, errcode.ErrVaultAuthFailed, "test auth failure")},
	}
	ctx, cancel := context.WithCancel(context.Background())

	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		clock:          clock.Real(),
	}

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Send nil on DoneCh (token no longer renewable) — triggers re-auth.
	fw.doneCh <- nil

	// Give re-auth a moment to start, then cancel ctx to exit.
	time.Sleep(testtime.MediumPoll) //archtest:allow:test-sleep wait for goroutine to enter blocking re-auth; no started observable
	cancel()

	select {
	case err := <-done:
		// ctx canceled during re-auth → Start returns nil.
		if err != nil {
			t.Errorf("Start() after ctx cancel must return nil, got: %v", err)
		}
	case <-time.After(testtime.D2s):
		t.Fatal("Start() did not return after ctx cancel")
	}
}

// TestTokenRenewalWorker_Start_HandlesDoneWithError verifies that when DoneCh
// fires with a non-nil error, the worker attempts re-auth. ctx cancel exits cleanly.
func TestTokenRenewalWorker_Start_HandlesDoneWithError(t *testing.T) {
	fw := newFakeTokenWatcher()
	fakeAuth := &fakeAuthMethod{
		method: MethodAppRole,
		errs:   []error{errcode.New(errcode.KindUnavailable, errcode.ErrVaultAuthFailed, "test auth failure")},
	}
	ctx, cancel := context.WithCancel(context.Background())

	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		clock:          clock.Real(),
	}

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	injectedErr := errcode.WrapInfra(errcode.ErrKeyProviderTransient, "vault: token renewal failed", nil)
	fw.doneCh <- injectedErr

	time.Sleep(testtime.MediumPoll) //archtest:allow:test-sleep wait for goroutine to enter blocking re-auth; no started observable
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() after ctx cancel must return nil, got: %v", err)
		}
	case <-time.After(testtime.D2s):
		t.Fatal("Start() did not return after DoneCh fired with error")
	}
}

// TestTokenRenewalWorker_Stop verifies that Stop calls watcher.Stop().
func TestTokenRenewalWorker_Stop(t *testing.T) {
	fw := newFakeTokenWatcher()
	w := &tokenRenewalWorker{
		currentWatcher: fw,
		logger:         slog.Default(),
		clock:          clock.Real(),
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
		currentWatcher: fw,
		logger:         slog.Default(),
		clock:          clock.Real(),
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
	p, err := NewTransitKeyProvider(context.Background(), fake,
		"transit", "gocell-config", NewStaticTokenAuth(nil, "test-token"), clock.Real())
	if err != nil {
		t.Fatalf("NewTransitKeyProvider: %v", err)
	}

	fw := newFakeTokenWatcher()
	p.renewalWorker = &tokenRenewalWorker{currentWatcher: fw, clock: clock.Real()}

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
	p := newTestProvider(t, fake)

	ctx := context.Background()
	const encryptWorkers = 8
	const encryptIterations = 20
	const rotations = 5

	var wg sync.WaitGroup
	errCh := make(chan error, encryptWorkers+1)
	var encryptSuccesses atomic.Int64
	var rotateSuccesses atomic.Int64

	// 8 goroutines concurrently encrypting.
	for range encryptWorkers {
		wg.Go(func() {
			successes, err := runConcurrentEncryptLoop(ctx, p, encryptIterations)
			encryptSuccesses.Add(int64(successes))
			if err != nil {
				errCh <- err
			}
		})
	}

	// 1 goroutine doing periodic rotations.
	wg.Go(func() {
		successes, err := runConcurrentRotateLoop(ctx, p, rotations)
		rotateSuccesses.Add(int64(successes))
		if err != nil {
			errCh <- err
		}
	})

	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		t.Fatalf("concurrent encrypt/rotate produced errors: %v", errors.Join(errs...))
	}

	wantEncrypts := int64(encryptWorkers * encryptIterations)
	if got := encryptSuccesses.Load(); got != wantEncrypts {
		t.Fatalf("encrypt successes = %d, want %d", got, wantEncrypts)
	}
	if got := rotateSuccesses.Load(); got != rotations {
		t.Fatalf("rotate successes = %d, want %d", got, rotations)
	}

	if got := p.cachedLatestVersion.Load(); got != int64(1+rotations) {
		t.Fatalf("cached latest version = %d, want %d", got, 1+rotations)
	}
}

// runConcurrentEncryptLoop is the per-worker body of
// TestTransitKeyProvider_ConcurrentEncryptRotate. Pulled out so the test
// itself stays under the cognitive-complexity threshold.
func runConcurrentEncryptLoop(ctx context.Context, p *TransitKeyProvider, iterations int) (int, error) {
	successes := 0
	for i := range iterations {
		h, err := p.Current(ctx)
		if err != nil {
			return successes, fmt.Errorf("Current iteration %d: %w", i, err)
		}
		vh, ok := h.(*vaultTransitHandle)
		if !ok {
			return successes, fmt.Errorf("Current iteration %d returned %T, want *vaultTransitHandle", i, h)
		}
		if _, err := vh.Encrypt(ctx, []byte("payload"), []byte("aad")); err != nil {
			return successes, fmt.Errorf("Encrypt iteration %d: %w", i, err)
		}
		successes++
	}
	return successes, nil
}

// runConcurrentRotateLoop is the rotator body of
// TestTransitKeyProvider_ConcurrentEncryptRotate.
func runConcurrentRotateLoop(ctx context.Context, p *TransitKeyProvider, iterations int) (int, error) {
	successes := 0
	for i := range iterations {
		if _, err := p.Rotate(ctx); err != nil {
			return successes, fmt.Errorf("Rotate iteration %d: %w", i, err)
		}
		successes++
	}
	return successes, nil
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
	return nil, fmt.Errorf("test fake: NewLifetimeWatcher called without configured result")
}

// TestInitTokenRenewal_LookupFails_ReturnsAuthError verifies that a
// LookupSelfToken failure results in ErrKeyProviderAuthFailed, not
// ErrKeyProviderTransient (F4: wrong error code at startup).
//
// We inject the renewable=true result so initTokenRenewal proceeds to
// LookupSelfToken (non-renewable tokens are skipped without error).
func TestInitTokenRenewal_LookupFails_ReturnsAuthError(t *testing.T) {
	injectedErr := fmt.Errorf("vault: 403 forbidden")
	fake := &fakeTokenRenewer{
		fakeVaultClient: fakeVaultClient{latestVersion: 1},
		lookupErr:       injectedErr,
	}

	p := &TransitKeyProvider{
		client:     fake,
		mountPath:  "transit",
		keyName:    "gocell-config",
		authMethod: NewStaticTokenAuth(nil, "test-token"),
		logger:     slog.Default(),
	}
	// Pass renewable=true so initTokenRenewal proceeds past the renewable check.
	err := p.initTokenRenewal(context.Background(), AuthResult{
		ClientToken:  "test-token",
		LeaseSeconds: 3600,
		Renewable:    true,
	})

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

	p := &TransitKeyProvider{
		client:     fake,
		mountPath:  "transit",
		keyName:    "gocell-config",
		authMethod: NewStaticTokenAuth(nil, "test-token"),
		logger:     slog.Default(),
	}
	err := p.initTokenRenewal(context.Background(), AuthResult{
		ClientToken:  "test-token",
		LeaseSeconds: 3600,
		Renewable:    true,
	})

	if err == nil {
		t.Fatal("initTokenRenewal must return error when NewLifetimeWatcher fails")
	}
	if !errChainHasCode(err, errcode.ErrKeyProviderAuthFailed) {
		t.Errorf("expected ErrKeyProviderAuthFailed in error chain, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// fakeAuthMethod — injectable AuthMethod for unit tests
// ---------------------------------------------------------------------------

// fakeAuthMethod is a controllable AuthMethod for testing the renewal worker.
// results[i] is returned for the i-th Login call if errs[i] is nil.
// If errs[i] is non-nil, that error is returned instead.
// If permanentErr is non-nil and all queued errs are exhausted, permanentErr is
// returned for every subsequent call — use this to prevent the "default success"
// from triggering buildWatcher in tests that only need to verify counter behavior.
type fakeAuthMethod struct {
	method       Method
	results      []AuthResult // queued successful responses
	errs         []error      // parallel queue; errs[i] != nil means Login call i fails
	permanentErr error        // returned for all calls once errs is exhausted (if non-nil)
	mu           sync.Mutex
	calls        int
}

func (f *fakeAuthMethod) Method() Method { return f.method }

func (f *fakeAuthMethod) Login(_ context.Context) (AuthResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return AuthResult{}, f.errs[i]
	}
	if i < len(f.results) {
		return f.results[i], nil
	}
	if f.permanentErr != nil {
		return AuthResult{}, f.permanentErr
	}
	// Default: return a non-renewable result so no new watcher is needed.
	return AuthResult{ClientToken: "default-test-token", Renewable: false}, nil
}

// ---------------------------------------------------------------------------
// New PR-A8 tests: auth method, nil guard, real-mode guard
// ---------------------------------------------------------------------------

// TestNewTransitKeyProvider_NilAuth_Fails verifies that nil auth is rejected.
func TestNewTransitKeyProvider_NilAuth_Fails(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 1}
	_, err := NewTransitKeyProvider(context.Background(), fake, "transit", "gocell-config", nil, clock.Real())
	if err == nil {
		t.Fatal("expected error for nil AuthMethod, got nil")
	}
	if !errChainHasCode(err, errcode.ErrVaultAuthFailed) {
		t.Errorf("expected ErrVaultAuthFailed in error chain, got: %v", err)
	}
}

// TestNewTransitKeyProvider_WithFakeAuth verifies that a non-nil fakeAuthMethod
// allows construction (the fake Login succeeds, no renewal worker is started).
func TestNewTransitKeyProvider_WithFakeAuth(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 3}
	auth := &fakeAuthMethod{
		method:  MethodAppRole,
		results: []AuthResult{{ClientToken: "fake-token", Renewable: false}},
	}
	p, err := NewTransitKeyProvider(context.Background(), fake, "transit", "gocell-config", auth, clock.Real())
	if err != nil {
		t.Fatalf("NewTransitKeyProvider with fakeAuth: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil TransitKeyProvider")
	}
	if auth.calls != 1 {
		t.Errorf("expected 1 Login call during construction, got %d", auth.calls)
	}
}

// ---------------------------------------------------------------------------
// F-4a: Renewable() getter and real-mode non-renewable rejection
// ---------------------------------------------------------------------------

// TestTransitKeyProvider_Renewable_FalseForStaticToken verifies that
// Renewable() returns false when the auth method produced a non-renewable token
// (e.g. static VAULT_TOKEN, MethodToken).
func TestTransitKeyProvider_Renewable_FalseForStaticToken(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 1}
	p, err := NewTransitKeyProvider(
		context.Background(), fake, "transit", "gocell-config",
		NewStaticTokenAuth(nil, "test-token"), clock.Real(),
	)
	if err != nil {
		t.Fatalf("NewTransitKeyProvider: %v", err)
	}
	if p.Renewable() {
		t.Error("Renewable() = true, want false for static token")
	}
}

// TestTransitKeyProvider_Renewable_TrueForRenewableAuth verifies that
// Renewable() returns true when the auth method produced a renewable token.
func TestTransitKeyProvider_Renewable_TrueForRenewableAuth(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 1}
	auth := &fakeAuthMethod{
		method:  MethodAppRole,
		results: []AuthResult{{ClientToken: "fake-token", Renewable: true}},
	}
	p, err := NewTransitKeyProvider(context.Background(), fake, "transit", "gocell-config", auth, clock.Real())
	if err != nil {
		t.Fatalf("NewTransitKeyProvider: %v", err)
	}
	// Note: the renewal worker is not started (fakeVaultClient does not implement
	// TokenRenewer), so authRenewable is set but renewalWorker is nil.
	if !p.Renewable() {
		t.Error("Renewable() = false, want true for renewable auth result")
	}
}

// TestNewTransitKeyProviderFromEnv_MissingVaultAddr_Fails verifies F-2:
// when VAULT_ADDR is not set, NewTransitKeyProviderFromEnv fails fast with
// ErrVaultAuthFailed instead of silently using the SDK loopback default.
func TestNewTransitKeyProviderFromEnv_MissingVaultAddr_Fails(t *testing.T) {
	setEnv(t, "VAULT_ADDR", "")
	_, err := NewTransitKeyProviderFromEnv(false, clock.Real())
	if err == nil {
		t.Fatal("expected error when VAULT_ADDR is unset, got nil")
	}
	if !errChainHasCode(err, errcode.ErrVaultAuthFailed) {
		t.Errorf("expected ErrVaultAuthFailed in error chain, got: %v", err)
	}
}

// Note: TestStaticTokenAuth_* and TestAssertForRealMode_* tests live in
// auth_test.go — they test auth.go directly, not TransitKeyProvider.

// TestNewTransitKeyProviderFromEnv_RealModeGuardPrecedesVaultIO pins the
// ordering contract documented on NewTransitKeyProviderFromEnv: when
// realMode=true and VAULT_AUTH_METHOD=token, AssertForRealMode must reject the
// configuration BEFORE NewTransitKeyProvider attempts any Vault network I/O.
//
// The test proves this structurally: it configures an unreachable VAULT_ADDR
// and verifies the returned error originates from AssertForRealMode (message
// "static VAULT_TOKEN is not allowed in real mode") rather than a network
// timeout. If someone reorders the calls and puts NewTransitKeyProvider before
// AssertForRealMode, this test will fail because the error message will be a
// connection/timeout error instead.
func TestNewTransitKeyProviderFromEnv_RealModeGuardPrecedesVaultIO(t *testing.T) {
	// Unreachable address — if the guard runs before NewTransitKeyProvider we
	// should never dial it. Use a short startup timeout so a regression would
	// surface as a timeout error quickly rather than hanging the test.
	setEnv(t,
		"VAULT_ADDR", "http://127.0.0.1:1",
		"VAULT_AUTH_METHOD", "token",
		"VAULT_TOKEN", "test-token-not-a-demo",
		startupTimeoutEnvVar, "2s",
	)

	_, err := NewTransitKeyProviderFromEnv(true /* realMode */, clock.Real())
	if err == nil {
		t.Fatal("expected error in real mode with VAULT_AUTH_METHOD=token, got nil")
	}
	if !errChainHasCode(err, errcode.ErrVaultAuthFailed) {
		t.Errorf("expected ErrVaultAuthFailed in error chain, got: %v", err)
	}
	// The AssertForRealMode error carries this exact phrase; a network
	// timeout / connection-refused error would not.
	if !strings.Contains(err.Error(), "static VAULT_TOKEN is not allowed in real mode") {
		t.Errorf("expected AssertForRealMode rejection message; got: %v\n"+
			"this usually means the real-mode guard ran AFTER NewTransitKeyProvider — check ordering in NewTransitKeyProviderFromEnv",
			err)
	}
}

// TestResolveStartupTimeout_EnvOverride verifies the GOCELL_VAULT_STARTUP_TIMEOUT
// escape hatch works and rejects malformed / non-positive values.
func TestResolveStartupTimeout_EnvOverride(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		want    time.Duration
		wantErr bool
	}{
		{name: "unset → default", env: "", want: defaultStartupTimeout},
		{name: "45s override", env: "45s", want: transitProviderD45s},
		{name: "2m override", env: "2m", want: testtime.D2min},
		{name: "malformed", env: "not-a-duration", wantErr: true},
		{name: "zero rejected", env: "0s", wantErr: true},
		{name: "negative rejected", env: "-1s", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setEnv(t, startupTimeoutEnvVar, tc.env)
			// setEnv treats empty string as unset, matching our "default" case.
			got, err := resolveStartupTimeout()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveStartupTimeout(%q) = nil err; want error", tc.env)
				}
				if !errChainHasCode(err, errcode.ErrVaultAuthFailed) {
					t.Errorf("wrong error code; got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveStartupTimeout(%q) unexpected err: %v", tc.env, err)
			}
			if got != tc.want {
				t.Errorf("resolveStartupTimeout(%q) = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

// TestNewTransitKeyProviderFromEnv_RejectsHTTPVaultAddr verifies that
// NewTransitKeyProviderFromEnv rejects non-TLS VAULT_ADDR values for remote
// hosts once phase-2 wires secutil.ValidateTLSEndpoint. During TDD phase-1
// these rejection cases will FAIL because the stub returns nil for all inputs,
// meaning the function proceeds past TLS check and errors at auth setup instead
// of at the expected TLS validation stage.
//
// Loopback exception: http://127.0.0.1:8200 is accepted by TLS validation
// (no network I/O during validation); the function may still fail at auth
// setup but must NOT fail with a TLS validation error.
func TestNewTransitKeyProviderFromEnv_RejectsHTTPVaultAddr(t *testing.T) {
	tests := []struct {
		name       string
		addr       string
		wantTLSErr bool // expect error specifically from TLS validation
	}{
		{
			name:       "http remote — reject (TLS required)",
			addr:       "http://prod.vault.io:8200",
			wantTLSErr: true,
		},
		{
			name:       "https remote — ok (TLS validation passes)",
			addr:       "https://prod.vault.io:8200",
			wantTLSErr: false,
		},
		{
			name:       "http loopback — ok (loopback exception)",
			addr:       "http://127.0.0.1:8200",
			wantTLSErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Set VAULT_ADDR; VAULT_AUTH_METHOD intentionally left unset so the
			// function fails at auth setup for non-TLS-rejected cases. TLS validation
			// must happen BEFORE auth setup (fail-fast ordering constraint).
			t.Setenv("VAULT_ADDR", tc.addr)
			t.Setenv("VAULT_AUTH_METHOD", "") // unset to ensure auth setup fails fast

			_, err := NewTransitKeyProviderFromEnv(false, clock.Real())
			assertVaultTLSResult(t, tc.addr, err, tc.wantTLSErr)
		})
	}
}

// assertVaultTLSResult checks NewTransitKeyProviderFromEnv's error against the
// expected TLS validation outcome. When wantTLSErr is true the error must
// chain to ErrAdapterEndpointNotTLS; otherwise the function may still error at
// auth setup (missing VAULT_AUTH_METHOD) but must not produce a TLS validation
// error. Extracted to keep TestNewTransitKeyProviderFromEnv_RejectsHTTPVaultAddr's
// loop body within the cognitive-complexity budget.
func assertVaultTLSResult(t *testing.T, addr string, err error, wantTLSErr bool) {
	t.Helper()
	if !wantTLSErr {
		if err == nil {
			return
		}
		var ec *errcode.Error
		if errors.As(err, &ec) && ec.Code == errcode.ErrAdapterEndpointNotTLS {
			t.Errorf("NewTransitKeyProviderFromEnv(%q): got unexpected TLS error: %v", addr, err)
		}
		return
	}
	if err == nil {
		t.Errorf("NewTransitKeyProviderFromEnv(%q): expected TLS error, got nil", addr)
		return
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) || ec.Code != errcode.ErrAdapterEndpointNotTLS {
		t.Errorf("NewTransitKeyProviderFromEnv(%q): error %q is not errcode.ErrAdapterEndpointNotTLS",
			addr, err.Error())
	}
}
