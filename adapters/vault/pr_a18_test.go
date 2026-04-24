package vault

// PR-A18 tests:
//   A15  TestApplyNamespaceFromEnv_*   — VAULT_NAMESPACE env propagated to vaultapi.Client
//   A16  TestEncryptUsesDataKeyEndpoint — Encrypt now goes through transit/datakey/plaintext
//   A16  TestDecryptStorageCompatibility — legacy "vault:vN:..." EDKs still decrypt
//   A18  TestRotateInvalidatesVersionCache — atomic.Int64 cache + lock-free Rotate
//   A18  TestTransitKeyProviderHasNoRWMutex — reflective regression guard

import (
	"context"
	"encoding/base64"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	vaultapi "github.com/hashicorp/vault/api"
)

// ---------------------------------------------------------------------------
// A15 — VAULT_NAMESPACE env applied to the underlying vaultapi.Client
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// A16 — Encrypt uses transit/datakey/plaintext (server-side DEK)
// ---------------------------------------------------------------------------

// datakeyFake routes /datakey/plaintext/<key> with a deterministic 32B DEK
// and the canonical "vault:vN:..." ciphertext envelope. /decrypt/ unwraps via
// the same XOR key so encrypt/decrypt round-trips verify end to end.
type datakeyFake struct {
	mu            sync.Mutex
	masterKey     [32]byte
	latestVersion int

	datakeyCalls atomic.Int64
	encryptCalls atomic.Int64 // legacy /encrypt path — must remain 0 after PR-A18
	decryptCalls atomic.Int64
	rotateCalls  atomic.Int64
	readCalls    atomic.Int64

	lastDataKeyData map[string]any
	lastDataKeyPath string
}

var _ VaultClient = (*datakeyFake)(nil)

func (f *datakeyFake) Write(_ context.Context, path string, data map[string]any) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case strings.Contains(path, "/datakey/plaintext/"):
		f.datakeyCalls.Add(1)
		f.lastDataKeyData = data
		f.lastDataKeyPath = path
		dek := make([]byte, 32)
		for i := range dek {
			dek[i] = byte(i + 1) // deterministic per-test DEK
		}
		wrapped := xorBytes(dek, f.masterKey[:])
		return map[string]any{
			"plaintext":  base64.StdEncoding.EncodeToString(dek),
			"ciphertext": "vault:v" + itoa(f.latestVersion) + ":" + base64.StdEncoding.EncodeToString(wrapped),
		}, nil
	case strings.Contains(path, "/decrypt/"):
		f.decryptCalls.Add(1)
		cipherStr, _ := data["ciphertext"].(string)
		parts := strings.SplitN(cipherStr, ":", 3)
		if len(parts) != 3 {
			return nil, ErrInvalidCipher{}
		}
		wrapped, err := base64.StdEncoding.DecodeString(parts[2])
		if err != nil {
			return nil, err
		}
		dek := xorBytes(wrapped, f.masterKey[:])
		return map[string]any{"plaintext": base64.StdEncoding.EncodeToString(dek)}, nil
	case strings.HasSuffix(path, "/rotate"):
		f.rotateCalls.Add(1)
		f.latestVersion++
		return map[string]any{}, nil
	case strings.Contains(path, "/encrypt/"):
		f.encryptCalls.Add(1)
		return nil, ErrLegacyEncryptCalled{}
	default:
		return map[string]any{}, nil
	}
}

func (f *datakeyFake) Read(_ context.Context, _ string) (map[string]any, error) {
	f.readCalls.Add(1)
	return map[string]any{"latest_version": f.latestVersion}, nil
}

// ErrLegacyEncryptCalled signals an unexpected fall-through to the legacy
// transit/encrypt path; the PR-A18 implementation must use datakey only.
type ErrLegacyEncryptCalled struct{}

func (ErrLegacyEncryptCalled) Error() string { return "legacy /encrypt/ path called" }

// ErrInvalidCipher signals a malformed vault:vN:... ciphertext in the fake.
type ErrInvalidCipher struct{}

func (ErrInvalidCipher) Error() string { return "invalid vault cipher" }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for n := i; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func TestEncryptUsesDataKeyEndpoint(t *testing.T) {
	fake := &datakeyFake{latestVersion: 3}
	p, err := NewTransitKeyProvider(context.Background(), fake, "transit", "gocell-config", NewStaticTokenAuth(nil, "test-token"))
	if err != nil {
		t.Fatalf("NewTransitKeyProvider: %v", err)
	}

	h, err := p.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	ct, _, edk, keyID, err := h.Encrypt(context.Background(), []byte("payload"), []byte("aad"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	if fake.datakeyCalls.Load() != 1 {
		t.Errorf("expected 1 datakey call, got %d", fake.datakeyCalls.Load())
	}
	if fake.encryptCalls.Load() != 0 {
		t.Errorf("legacy /encrypt path must not be called, got %d calls", fake.encryptCalls.Load())
	}
	if fake.lastDataKeyPath != "transit/datakey/plaintext/gocell-config" {
		t.Errorf("datakey path = %q, want %q", fake.lastDataKeyPath, "transit/datakey/plaintext/gocell-config")
	}
	if bits := fake.lastDataKeyData["bits"]; bits != 256 {
		t.Errorf("datakey body bits = %v, want 256", bits)
	}
	if keyID != "vault-transit:v3" {
		t.Errorf("keyID = %q, want vault-transit:v3", keyID)
	}
	if !strings.HasPrefix(string(edk), "vault:v3:") {
		t.Errorf("edk does not have expected prefix vault:v3:, got %q", string(edk))
	}
	if string(ct) == "payload" {
		t.Error("ciphertext must not equal plaintext")
	}
}

func TestEncryptDecryptRoundTrip_DataKey(t *testing.T) {
	fake := &datakeyFake{latestVersion: 5}
	p, err := NewTransitKeyProvider(context.Background(), fake, "transit", "gocell-config", NewStaticTokenAuth(nil, "test-token"))
	if err != nil {
		t.Fatalf("NewTransitKeyProvider: %v", err)
	}

	h, _ := p.Current(context.Background())
	plaintext := []byte("hello world")
	aad := []byte("row:42")

	ct, nonce, edk, _, err := h.Encrypt(context.Background(), plaintext, aad)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := h.Decrypt(context.Background(), ct, nonce, edk, aad)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("round-trip mismatch: got %q, want %q", string(got), string(plaintext))
	}
}

// ---------------------------------------------------------------------------
// A18 — atomic.Int64 version cache + lock-free Rotate
// ---------------------------------------------------------------------------

func TestRotateInvalidatesVersionCache(t *testing.T) {
	fake := &datakeyFake{latestVersion: 1}
	p, err := NewTransitKeyProvider(context.Background(), fake, "transit", "gocell-config", NewStaticTokenAuth(nil, "test-token"))
	if err != nil {
		t.Fatalf("NewTransitKeyProvider: %v", err)
	}

	// (a) Construction triggers one Read (key existence check) — record baseline.
	baselineReads := fake.readCalls.Load()

	// (b) First Current() either uses cache (already filled by NewTransitKeyProvider)
	// or hits Vault once. After PR-A18 the cache is filled by NewTransitKeyProvider's
	// readLatestVersion call, so Current() should be cache-only.
	_, _ = p.Current(context.Background())
	afterFirst := fake.readCalls.Load()
	if afterFirst > baselineReads {
		t.Errorf("first Current() should not hit Vault (cache already warm); reads grew %d→%d",
			baselineReads, afterFirst)
	}

	// (c) Many subsequent Current() calls — still 0 Vault reads.
	for i := 0; i < 50; i++ {
		_, _ = p.Current(context.Background())
	}
	if fake.readCalls.Load() != afterFirst {
		t.Errorf("Current() loop must hit cache (0 reads); got %d extra reads",
			fake.readCalls.Load()-afterFirst)
	}

	// (d) Rotate invalidates and re-reads.
	if _, err := p.Rotate(context.Background()); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if fake.rotateCalls.Load() != 1 {
		t.Errorf("expected 1 rotate, got %d", fake.rotateCalls.Load())
	}
	if fake.readCalls.Load() <= afterFirst {
		t.Errorf("Rotate() must trigger a fresh readLatestVersion; reads stayed at %d",
			fake.readCalls.Load())
	}

	// (e) Current() after rotate returns the new version (latestVersion=2).
	h, _ := p.Current(context.Background())
	if got := h.ID(); got != "vault-transit:v2" {
		t.Errorf("post-rotate Current().ID() = %q, want vault-transit:v2", got)
	}
}

func TestCurrentConcurrentReaders(t *testing.T) {
	fake := &datakeyFake{latestVersion: 7}
	p, err := NewTransitKeyProvider(context.Background(), fake, "transit", "gocell-config", NewStaticTokenAuth(nil, "test-token"))
	if err != nil {
		t.Fatalf("NewTransitKeyProvider: %v", err)
	}

	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			h, err := p.Current(context.Background())
			if err != nil {
				t.Errorf("Current: %v", err)
				return
			}
			if h.ID() != "vault-transit:v7" {
				t.Errorf("ID() = %q, want vault-transit:v7", h.ID())
			}
		}()
	}
	wg.Wait()
}

func TestTransitKeyProviderHasNoRWMutex(t *testing.T) {
	rt := reflect.TypeOf(TransitKeyProvider{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		// Allow renewalWorker etc. to embed sync state — only the provider's own
		// top-level mu must be gone (PR-A18 lock removal).
		if f.Name == "mu" {
			t.Fatalf("TransitKeyProvider.mu still present (type=%s); PR-A18 removed the RWMutex", f.Type)
		}
	}
}
