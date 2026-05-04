package vault

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/ghbvf/gocell/kernel/clock"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/aeadutil"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/logutil"
	"github.com/ghbvf/gocell/pkg/secutil"
)

// reauthBackoffInitial is the first backoff interval for re-authentication retries.
const reauthBackoffInitial = time.Second

// reauthBackoffCap is the maximum backoff interval for re-authentication retries.
const reauthBackoffCap = 60 * time.Second

// reauthBackoffMultiplier is the exponential backoff factor applied on each retry.
const reauthBackoffMultiplier time.Duration = 2

// defaultStartupTimeout bounds the total time spent on Vault-facing startup I/O
// (auth Login, optional wrap-token unwrap, initial key metadata read). Override
// via GOCELL_VAULT_STARTUP_TIMEOUT (a time.ParseDuration string, e.g. "45s").
const defaultStartupTimeout = 30 * time.Second

// startupTimeoutEnvVar is the env var used to override defaultStartupTimeout.
const startupTimeoutEnvVar = "GOCELL_VAULT_STARTUP_TIMEOUT"

// applyNamespaceFromEnv reads VAULT_NAMESPACE and applies it to the given Vault
// client via SetNamespace so that all subsequent Auth Login + transit calls are
// scoped to the namespace (HCP Vault / Vault Enterprise multi-tenancy).
// Returns the applied namespace ("" when unset) for caller logging.
//
// ref: hashicorp/vault api/client.go SetNamespace + EnvVaultNamespace = "VAULT_NAMESPACE"
func applyNamespaceFromEnv(raw *vaultapi.Client) string {
	ns := os.Getenv("VAULT_NAMESPACE")
	if ns == "" {
		return ""
	}
	raw.SetNamespace(ns)

	slog.Info("vault-transit: namespace configured", slog.String("namespace", logutil.Sanitize(ns)))
	return ns
}

// resolveStartupTimeout returns the startup deadline for Vault-facing I/O,
// honoring GOCELL_VAULT_STARTUP_TIMEOUT when set. Accepts any time.ParseDuration
// string (e.g. "45s", "2m"). Returns an error on malformed or non-positive values
// rather than silently falling back to the default — misconfiguration should be
// visible at startup.
func resolveStartupTimeout() (time.Duration, error) {
	raw := os.Getenv(startupTimeoutEnvVar)
	if raw == "" {
		return defaultStartupTimeout, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, errcode.Wrap(errcode.KindUnavailable, errcode.ErrVaultAuthFailed,
			fmt.Sprintf("vault-transit: invalid %s=%q (expected time.ParseDuration format, e.g. 45s)", startupTimeoutEnvVar, raw), err)
	}
	if d <= 0 {
		return 0, errcode.New(errcode.KindUnavailable, errcode.ErrVaultAuthFailed,
			fmt.Sprintf("vault-transit: %s=%q must be positive", startupTimeoutEnvVar, raw))
	}
	return d, nil
}

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

// TokenRenewer is an optional capability for VaultClient implementations that
// support token lifecycle management. The production vaultAPIClient implements
// this; test fakes do not (static tokens need no renewal).
//
// Type-assert the VaultClient to TokenRenewer to enable background renewal:
//
//	if r, ok := client.(TokenRenewer); ok { ... }
//
// ref: hashicorp/vault api/lifetime_watcher.go@main
type TokenRenewer interface {
	// LookupSelfToken returns the token's current metadata (TTL, renewable flag)
	// by calling auth/token/lookup-self. Required to seed the LifetimeWatcher
	// with an accurate initial LeaseDuration.
	LookupSelfToken(ctx context.Context) (*vaultapi.Secret, error)
	// NewLifetimeWatcher creates a LifetimeWatcher that automatically renews
	// the token at ~2/3 of its TTL.
	NewLifetimeWatcher(i *vaultapi.LifetimeWatcherInput) (*vaultapi.LifetimeWatcher, error)
}

// ---------------------------------------------------------------------------
// tokenWatcher — abstraction over vaultapi.LifetimeWatcher for testability
// ---------------------------------------------------------------------------

// tokenWatcher abstracts the Vault SDK LifetimeWatcher channels so that
// tokenRenewalWorker can be unit-tested without a real Vault connection.
// The production implementation wraps *vaultapi.LifetimeWatcher directly.
//
// ref: hashicorp/vault api/lifetime_watcher.go@main — LifetimeWatcher.Start/Stop/DoneCh/RenewCh
type tokenWatcher interface {
	// Start begins the background renewal loop. Blocks until Stop() or the
	// token can no longer be renewed.
	Start()
	// Stop signals the watcher to stop its internal loop.
	Stop()
	// DoneCh fires once, either when renewal is no longer possible (nil error)
	// or when an unrecoverable error occurs.
	DoneCh() <-chan error
	// RenewCh fires on each successful token renewal.
	RenewCh() <-chan *vaultapi.RenewOutput
}

// vaultLifetimeWatcherAdapter wraps *vaultapi.LifetimeWatcher to satisfy
// the tokenWatcher interface. It is only used in the production path where
// a real Vault client supports TokenRenewer.
type vaultLifetimeWatcherAdapter struct {
	w *vaultapi.LifetimeWatcher
}

func (a *vaultLifetimeWatcherAdapter) Start()                                { a.w.Start() }
func (a *vaultLifetimeWatcherAdapter) Stop()                                 { a.w.Stop() }
func (a *vaultLifetimeWatcherAdapter) DoneCh() <-chan error                  { return a.w.DoneCh() }
func (a *vaultLifetimeWatcherAdapter) RenewCh() <-chan *vaultapi.RenewOutput { return a.w.RenewCh() }

// ---------------------------------------------------------------------------
// tokenRenewalWorker — worker.Worker wrapping the token renewal loop
// ---------------------------------------------------------------------------

// tokenRenewalWorker implements worker.Worker. It manages a LifetimeWatcher
// loop and, on terminal watcher failure, attempts re-authentication via
// authMethod.Login (with exponential back-off) before rebuilding a new watcher.
//
// The only exit condition is ctx cancellation. Static tokens (Renewable=false)
// must not be passed to this worker; initTokenRenewal skips worker creation for
// non-renewable auth results.
//
// State transitions:
//
//	healthy(watcher running) → DoneCh fires → authHealthy=0 → reauthenticate → authHealthy=1 → rebuild watcher → loop
//	any state → ctx.Done → return nil
//
// ref: hashicorp/vault api/lifetime_watcher.go@main — LifetimeWatcher usage pattern
// ref: kubernetes/kubernetes staging/src/k8s.io/client-go/rest/transport.go — re-auth loop pattern
type tokenRenewalWorker struct {
	// client is used to build new LifetimeWatcher instances after re-auth.
	client TokenRenewer
	// authMethod is called during re-authentication.
	authMethod AuthMethod
	logger     *slog.Logger

	// Prometheus metrics.
	renewSuccess prometheus.Counter     // gocell_vault_token_renew_success_total
	renewFailure prometheus.Counter     // gocell_vault_token_renew_failure_total
	authHealthy  prometheus.Gauge       // gocell_vault_token_auth_healthy (1=healthy, 0=re-authing)
	loginOutcome *prometheus.CounterVec // gocell_vault_auth_login_total{method,result,reason}

	// clock is the time source used for backoff sleeps.
	clock clock.Clock

	// Internal state protected by mu.
	//
	// Stop() and runWatcher's ctx.Done branch may both attempt to stop a
	// watcher; we rely on Vault SDK LifetimeWatcher.Stop being idempotent
	// (internally guarded by sync.Once) rather than guarding with a shared
	// sync.Once here — a shared Once could let one path claim the slot while
	// currentWatcher has just been swapped by doReauth, leaving the new
	// watcher unstopped. See:
	// ref: hashicorp/vault api/lifetime_watcher.go#Stop — idempotent via sync.Once
	mu             sync.Mutex
	currentWatcher tokenWatcher
}

// Start blocks until ctx is canceled. On each watcher termination it
// re-authenticates (with exponential backoff capped at 60 s), rebuilds a new
// LifetimeWatcher, and resumes. authHealthy gauge transitions 1→0 on watcher
// failure and 0→1 on successful re-auth.
//
// Only ctx cancellation causes Start to return nil.
//
// ref: hashicorp/vault api/lifetime_watcher.go@main — DoneCh / RenewCh semantics
// ref: kubernetes/kubernetes client-go rest/transport.go — re-auth loop pattern
func (w *tokenRenewalWorker) Start(ctx context.Context) error {
	w.mu.Lock()
	watcher := w.currentWatcher
	w.mu.Unlock()

	// currentWatcher is set by initTokenRenewal during construction. A nil value
	// here indicates a programming error (e.g. Start called on a worker that
	// skipped initialization) and must not be silently swallowed.
	if watcher == nil {
		return errcode.New(errcode.KindUnavailable, errcode.ErrVaultAuthFailed,
			"vault-transit: renewal worker started with nil watcher (initTokenRenewal skipped?)")
	}

	for {
		if w.runWatcher(ctx, watcher) {
			return nil
		}
		newWatcher, ok := w.doReauth(ctx)
		if !ok {
			return nil
		}
		w.mu.Lock()
		w.currentWatcher = newWatcher
		watcher = newWatcher
		w.mu.Unlock()
	}
}

// doReauth drives the re-authentication cycle after a watcher terminates.
// It sets authHealthy=0, then loops forever: reauthenticate (with exponential
// backoff) → buildWatcher. Only ctx cancellation causes the loop to exit.
//
// Contract: reauthenticate returns a non-nil error only when ctx is canceled.
// buildWatcher failures are logged and cause the loop to sleep (with exponential
// backoff, capped at reauthBackoffCap) before retrying. This prevents a hot
// loop when Vault is healthy (Login succeeds) but NewLifetimeWatcher consistently
// fails (e.g. SDK issue). The backoff is local to doReauth — it is separate from
// the backoff inside reauthenticate.
//
// Returns (newWatcher, true) once both reauthenticate and buildWatcher succeed,
// or (nil, false) if ctx was canceled.
func (w *tokenRenewalWorker) doReauth(ctx context.Context) (tokenWatcher, bool) {
	if w.authHealthy != nil {
		w.authHealthy.Set(0)
	}
	watcherBackoff := reauthBackoffInitial
	for {
		if ctx.Err() != nil {
			return nil, false
		}
		if err := w.reauthenticate(ctx); err != nil {
			// reauthenticate returns non-nil only on ctx cancellation.
			return nil, false
		}
		newWatcher, err := w.buildWatcher(ctx)
		if err == nil {
			if w.authHealthy != nil {
				w.authHealthy.Set(1)
			}
			return newWatcher, true
		}
		// buildWatcher failed — sleep with exponential backoff before retrying.
		// Without this sleep, a Vault deployment that accepts Login but rejects
		// NewLifetimeWatcher would spin the CPU at 100%.
		w.logger.WarnContext(ctx, "vault-transit: buildWatcher failed after re-auth; retrying",
			slog.Any("error", err),
			slog.Duration("backoff", watcherBackoff))
		backoffTimer := w.clock.NewTimerAt(w.clock.Now().Add(watcherBackoff))
		select {
		case <-ctx.Done():
			backoffTimer.Stop()
			return nil, false
		case <-backoffTimer.C():
		}
		backoffTimer.Stop()
		watcherBackoff *= reauthBackoffMultiplier
		if watcherBackoff > reauthBackoffCap {
			watcherBackoff = reauthBackoffCap
		}
	}
}

// runWatcher starts the given watcher in a goroutine and loops on its channels
// until DoneCh fires or ctx is canceled.
// Returns true if the loop should terminate (ctx done / channel closed), false
// if the watcher terminated with an error that should trigger re-auth.
func (w *tokenRenewalWorker) runWatcher(ctx context.Context, watcher tokenWatcher) (ctxDone bool) {
	go watcher.Start()

	for {
		select {
		case <-ctx.Done():
			// Stop the watcher passed to this invocation directly. Using a
			// shared sync.Once with Stop() would race with doReauth replacing
			// currentWatcher, potentially leaving the newly-built watcher
			// running. LifetimeWatcher.Stop is internally idempotent, so
			// calling it here and from Stop() concurrently is safe.
			watcher.Stop()
			return true
		case err, ok := <-watcher.DoneCh():
			return w.handleDoneCh(ctx, err, ok)
		case renewal, ok := <-watcher.RenewCh():
			if w.handleRenewCh(ctx, renewal, ok) {
				return true
			}
		}
	}
}

// handleDoneCh processes a value received from watcher.DoneCh().
// Returns true if the caller should terminate cleanly (channel closed),
// false if re-auth should be triggered (token no longer renewable or error).
func (w *tokenRenewalWorker) handleDoneCh(ctx context.Context, err error, ok bool) bool {
	if !ok {
		// Channel closed: watcher stopped externally — clean exit.
		return true
	}
	if w.renewFailure != nil {
		w.renewFailure.Inc()
	}
	if err != nil {
		w.logger.WarnContext(ctx, "vault-transit: token renewal watcher stopped with error; will re-authenticate",
			slog.Any("error", err))
	} else {
		w.logger.WarnContext(ctx, "vault-transit: token is no longer renewable; will re-authenticate")
	}
	return false // trigger re-auth
}

// handleRenewCh processes a value received from watcher.RenewCh().
// Returns true if the channel was closed (caller should terminate), false otherwise.
func (w *tokenRenewalWorker) handleRenewCh(ctx context.Context, renewal *vaultapi.RenewOutput, ok bool) bool {
	if !ok {
		return true
	}
	if renewal == nil || renewal.Secret == nil || renewal.Secret.Auth == nil {
		w.logger.WarnContext(ctx, "vault-transit: received nil or incomplete renewal notification")
		return false
	}
	w.logger.InfoContext(ctx, "vault-transit: token renewed",
		slog.Int("lease_duration", renewal.Secret.Auth.LeaseDuration))
	if w.renewSuccess != nil {
		w.renewSuccess.Inc()
	}
	return false
}

// reauthenticate loops on authMethod.Login with exponential backoff until it
// succeeds or ctx is canceled. On each failure it increments loginOutcome
// counter and logs at Warn level.
//
// Backoff: 1s → 2s → 4s → … → 60s (cap). Sleep is interruptible by ctx.Done.
//
// ref: kubernetes/kubernetes client-go util/retry/retry.go — exponential backoff cap pattern
func (w *tokenRenewalWorker) reauthenticate(ctx context.Context) error {
	backoff := reauthBackoffInitial
	methodStr := string(w.authMethod.Method())
	for {
		_, err := w.authMethod.Login(ctx)
		if err == nil {
			if w.loginOutcome != nil {
				w.loginOutcome.WithLabelValues(methodStr, "success", reasonNone).Inc()
			}
			return nil
		}
		reason := classifyAuthLoginError(err)
		if w.loginOutcome != nil {
			w.loginOutcome.WithLabelValues(methodStr, "failure", reason).Inc()
		}
		w.logger.WarnContext(ctx, "vault-transit: re-authentication failed; will retry",
			slog.String("method", methodStr),
			slog.String("reason", reason),
			slog.Any("error", err),
			slog.Duration("backoff", backoff))

		retryTimer := w.clock.NewTimerAt(w.clock.Now().Add(backoff))
		select {
		case <-ctx.Done():
			retryTimer.Stop()
			return errcode.New(errcode.KindUnavailable, errcode.ErrVaultAuthFailed,
				"vault-transit: re-authentication loop canceled by context")
		case <-retryTimer.C():
		}
		retryTimer.Stop()
		backoff *= reauthBackoffMultiplier
		if backoff > reauthBackoffCap {
			backoff = reauthBackoffCap
		}
	}
}

// buildWatcher creates a new LifetimeWatcher using the current token.
func (w *tokenRenewalWorker) buildWatcher(ctx context.Context) (tokenWatcher, error) {
	secret, err := w.client.LookupSelfToken(ctx)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderAuthFailed,
			"vault-transit: lookup self token after re-auth", err)
	}
	raw, err := w.client.NewLifetimeWatcher(&vaultapi.LifetimeWatcherInput{Secret: secret})
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderAuthFailed,
			"vault-transit: create new LifetimeWatcher after re-auth", err)
	}
	if raw == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrKeyProviderAuthFailed,
			"vault-transit: NewLifetimeWatcher returned nil after re-auth")
	}
	return &vaultLifetimeWatcherAdapter{w: raw}, nil
}

// Stop signals the currently-active watcher to stop. Idempotent: safe to call
// multiple times and safe to call concurrently with runWatcher's own ctx.Done
// cleanup path (the underlying LifetimeWatcher.Stop is guarded by sync.Once).
//
// Stop alone does not terminate the Start goroutine; callers shutting the
// worker down must cancel the context passed to Start (the typical
// ManagedResource.Stop contract: cancel parent ctx, then call Stop()).
func (w *tokenRenewalWorker) Stop(_ context.Context) error {
	w.mu.Lock()
	watcher := w.currentWatcher
	w.mu.Unlock()
	if watcher != nil {
		watcher.Stop()
	}
	return nil
}

// Compile-time interface assertions — fail at build time if the scaffold drifts
// from the kernel/crypto contracts.
var (
	_ kcrypto.KeyProvider = (*TransitKeyProvider)(nil)
	_ kcrypto.KeyHandle   = (*vaultTransitHandle)(nil)
)

// ---------------------------------------------------------------------------
// vaultTransitHandle
// ---------------------------------------------------------------------------

// vaultTransitHandle implements kernel/crypto.KeyHandle using envelope
// encryption via HashiCorp Vault Transit.
//
// Envelope encryption layout (对标 k8s KMS v2):
//   - Encrypt: Vault Transit datakey/plaintext → server-generated 32B DEK
//   - wrapped EDK (single round-trip; HSM-backed RNG in HCP).
//     AES-GCM(DEK, plaintext, aad) → return (ct, nonce, EDK, keyID).
//   - keyID is extracted from the Vault datakey response ciphertext prefix
//     "vault:vN:" — mirrors k8s KMS v2 EncryptResponse.KeyID, eliminates the
//     race between Current() and a concurrent rotation.
//   - Decrypt: Vault Transit decrypt(edk) → unwrapped DEK → AES-GCM Open(ct, nonce, DEK, aad).
//     The decrypt endpoint accepts EDKs minted by either /datakey/plaintext
//     (current path) or the legacy /encrypt path; storage is forward-compatible.
//   - AAD is bound entirely in the local AES-GCM layer; it is NOT sent to Vault.
//     This fixes the S1 P0 bug where the pre-R1c path sent AAD as the Vault
//     "context" field, which Vault ignores for non-derived aes256-gcm96 keys.
//
// ref: hashicorp/vault sdk/helper/keysutil/policy.go@main:L127 (version prefix)
// ref: kubernetes/kubernetes staging/src/k8s.io/apiserver/pkg/storage/value/encrypt/envelope/kmsv2/envelope.go@master
// ref: hashicorp/vault api-docs/secret/transit POST /datakey/plaintext/:name (server-side DEK)
type vaultTransitHandle struct {
	id        string
	mountPath string
	keyName   string
	client    VaultClient
}

// ID returns the key version identifier (e.g. "vault-transit:v3").
func (h *vaultTransitHandle) ID() string { return h.id }

// Encrypt encrypts plaintext using envelope encryption with a Vault-issued DEK.
//
// Envelope flow:
//  1. Vault Transit datakey/plaintext → fresh 32B DEK + wrapped EDK (single round-trip).
//     The DEK is generated server-side (HSM-backed in HCP), eliminating client RNG.
//  2. AES-GCM encrypt plaintext with DEK and aad → (ct, nonce).
//     AAD is bound here at the local AEAD layer; it is NOT sent to Vault.
//  3. Extract keyID from the Vault response ciphertext prefix "vault:vN:" → "vault-transit:vN".
//  4. Return (ct, nonce, []byte(vaultCiphertext), keyID, nil).
//
// Storage compatibility: the EDK format is the canonical "vault:vN:..." string;
// Decrypt uses the same /transit/decrypt endpoint and accepts EDKs produced by
// either /datakey/plaintext (current path) or the legacy /encrypt path.
//
// ref: hashicorp/vault api-docs/secret/transit POST /transit/datakey/plaintext/:name
// ref: kubernetes/kubernetes kmsv2/envelope.go@master (EncryptResponse.KeyID)
func (h *vaultTransitHandle) Encrypt(ctx context.Context, plaintext, aad []byte) (ciphertext, nonce, edk []byte, keyID string, err error) {
	dkPath := h.mountPath + "/datakey/plaintext/" + h.keyName
	result, err := h.client.Write(ctx, dkPath, map[string]any{"bits": 256})
	if err != nil {
		return nil, nil, nil, "", classifyVaultEncryptError(err)
	}

	plaintextB64, ok := result["plaintext"].(string)
	if !ok {
		return nil, nil, nil, "", errcode.New(errcode.KindInternal, errcode.ErrKeyProviderEncryptFailed,
			"vault-transit: datakey response missing string 'plaintext' field")
	}
	dek, err := base64.StdEncoding.DecodeString(plaintextB64)
	if err != nil {
		return nil, nil, nil, "", errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderEncryptFailed,
			"vault-transit: base64 decode DEK from datakey response", err)
	}
	defer clear(dek)

	ciphertextStr, ok := result["ciphertext"].(string)
	if !ok {
		return nil, nil, nil, "", errcode.New(errcode.KindInternal, errcode.ErrKeyProviderEncryptFailed,
			"vault-transit: datakey response missing string 'ciphertext' field")
	}
	keyID, err = parseVaultKeyID(ciphertextStr, errcode.ErrKeyProviderEncryptFailed)
	if err != nil {
		return nil, nil, nil, "", err
	}

	ciphertext, nonce, err = aeadutil.EncryptGCM(dek, plaintext, aad)
	if err != nil {
		return nil, nil, nil, "", errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderEncryptFailed,
			"vault-transit: local AES-GCM encrypt", err)
	}

	return ciphertext, nonce, []byte(ciphertextStr), keyID, nil
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
	edkVersion, parseErr := parseVaultKeyID(string(edk), errcode.ErrKeyProviderDecryptFailed)
	if parseErr != nil {
		return nil, parseErr
	}
	if err := kcrypto.MatchKeyID(h.id, edkVersion); err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderDecryptFailed, "vault-transit: keyID mismatch", err)
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
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderDecryptFailed,
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
		// No "context" or "associated_data" — DEK is a random per-record value
		// with no row identity. AAD binding lives in the local AES-GCM layer.
	}

	result, err := h.client.Write(ctx, decPath, data)
	if err != nil {
		return nil, classifyVaultDecryptError(err)
	}

	plaintextB64, ok := result["plaintext"].(string)
	if !ok {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrKeyProviderDecryptFailed,
			"vault-transit: decrypt response missing string 'plaintext' field")
	}

	dek, err = base64.StdEncoding.DecodeString(plaintextB64)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderDecryptFailed,
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
// auth is a required AuthMethod. Construction calls auth.Login to obtain the
// initial token, then optionally starts a background LifetimeWatcher + re-auth
// loop (when the token is renewable and the client implements TokenRenewer).
//
// Environment variables (standard Vault SDK env vars):
//   - VAULT_ADDR:                  Vault server address
//   - VAULT_NAMESPACE:             (HCP / Vault Enterprise) namespace; applied via SetNamespace before all I/O. Empty = root namespace.
//   - VAULT_AUTH_METHOD:           auth method (token|approle|kubernetes) — REQUIRED
//   - VAULT_TOKEN:                 (token method) Vault token
//   - VAULT_ROLE_ID:               (approle method)
//   - VAULT_SECRET_ID:             (approle + VAULT_SECRET_ID_TYPE=direct)
//   - GOCELL_VAULT_TRANSIT_MOUNT:  transit mount path (default: "transit")
//   - GOCELL_VAULT_TRANSIT_KEY:    key name (default: "gocell-config")
//
// ref: hashicorp/vault builtin/logical/transit/path_rewrap.go@main
// ref: kubernetes/kubernetes kmsv2/envelope.go@master
type TransitKeyProvider struct {
	client    VaultClient
	mountPath string
	keyName   string

	// cachedLatestVersion is the cached transit/keys/{name} latest_version.
	// Zero means uninitialised or invalidated; readers fall back to a Vault
	// readLatestVersion call. Rotate() invalidates by storing 0, then refreshes.
	//
	// Multi-pod staleness is benign by design:
	//   - Vault /transit/datakey/plaintext always uses latest_version server-side
	//     and the keyID returned to callers is parsed from the response, not from
	//     this cache. Stale cache only affects KeyHandle.ID() returned by Current()
	//     between a remote rotate and this pod's next Vault round-trip — diagnostic
	//     surface only.
	//   - Decrypt validates against the EDK's own version prefix; cache is never
	//     consulted on the decrypt path.
	cachedLatestVersion atomic.Int64

	// authMethod stored so renewal worker can re-authenticate.
	authMethod AuthMethod

	// authRenewable records whether the initial auth produced a renewable token.
	// False for MethodToken or for misconfigured AppRole/K8s roles that return
	// non-renewable tokens. Exposed via Renewable() so NewTransitKeyProviderFromEnv
	// can reject non-renewable tokens in real mode (F-4a).
	authRenewable bool

	// renewalWorker manages background Vault token renewal.
	// nil when auth result is not renewable (e.g. static token) or when the
	// VaultClient does not implement TokenRenewer (e.g. test fakes).
	renewalWorker *tokenRenewalWorker
	logger        *slog.Logger
	clock         clock.Clock
}

// NewTransitKeyProvider creates a TransitKeyProvider with the given VaultClient
// and AuthMethod. auth is REQUIRED — pass NewStaticTokenAuth(nil, "test-token")
// in unit tests that do not need a real Vault connection.
//
// ctx governs the initial Login and key existence check. If Vault is unreachable,
// the call blocks until ctx is canceled (or the call times out). Callers that
// do not have a deadline should use context.WithTimeout to avoid blocking startup
// indefinitely. NewTransitKeyProviderFromEnv uses a 30-second timeout by default.
//
// Construction calls auth.Login to acquire the initial token, then performs a
// fail-fast key existence check (readLatestVersion). The check doubles as the
// initial cachedLatestVersion seed, so the first Current() call after
// construction is served lock-free without a Vault round-trip. If the token is
// renewable and the client implements TokenRenewer, initTokenRenewal configures
// the background renewal + re-auth worker (not started — call Worker().Start).
//
// Returns an error if auth is nil, Login fails, or the key existence check fails.
func NewTransitKeyProvider(
	ctx context.Context, client VaultClient, mountPath, keyName string, auth AuthMethod, clk clock.Clock,
) (*TransitKeyProvider, error) {
	clock.MustHaveClock(clk, "vault.NewTransitKeyProvider")
	if auth == nil {
		return nil, errcode.New(errcode.KindUnavailable, errcode.ErrVaultAuthFailed,
			"vault-transit: auth method is required (pass NewStaticTokenAuth in tests)")
	}
	if mountPath == "" {
		mountPath = "transit"
	}
	if keyName == "" {
		keyName = "gocell-config"
	}
	p := &TransitKeyProvider{
		client:     client,
		mountPath:  mountPath,
		keyName:    keyName,
		authMethod: auth,
		logger:     slog.Default(),
		clock:      clk,
	}

	// Perform initial login to acquire token and configure the client.
	result, err := p.authenticate(ctx)
	if err != nil {
		return nil, err
	}
	p.authRenewable = result.Renewable

	// Fail-fast: verify the key exists. Warm the cache so the first Current()
	// call after construction is lock-free and Vault-free.
	version, err := p.readLatestVersion(ctx)
	if err != nil {
		return nil, err
	}
	p.cachedLatestVersion.Store(int64(version))

	// Initialize background token renewal if applicable.
	if err := p.initTokenRenewal(ctx, result); err != nil {
		return nil, err
	}

	return p, nil
}

// authenticate calls auth.Login and returns the result. On success it records
// a loginOutcome metric (if the renewal worker is already configured from a
// prior call — during initial construction the worker is not yet set, so the
// metric is recorded separately in initTokenRenewal).
func (p *TransitKeyProvider) authenticate(ctx context.Context) (AuthResult, error) {
	result, err := p.authMethod.Login(ctx)
	if err != nil {
		return AuthResult{}, errcode.Wrap(errcode.KindUnavailable, errcode.ErrVaultAuthFailed,
			"vault-transit: initial authentication failed", err)
	}
	return result, nil
}

// NewTransitKeyProviderFromEnv constructs a TransitKeyProvider from environment
// variables using the real HashiCorp vault/api client.
//
// Required env vars:
//   - VAULT_ADDR          — Vault server address
//   - VAULT_AUTH_METHOD   — auth method: token | approle | kubernetes (REQUIRED, no default)
//
// Optional env vars (default values shown):
//   - GOCELL_VAULT_TRANSIT_MOUNT  (default: "transit")
//   - GOCELL_VAULT_TRANSIT_KEY    (default: "gocell-config")
//
// When realMode is true, static VAULT_TOKEN is rejected (ErrVaultAuthFailed).
// Operators must use approle or kubernetes in production.
//
// Ordering constraint — the real-mode guard (AssertForRealMode) runs BEFORE
// NewTransitKeyProvider so that a rejected token configuration fails fast
// without spending the Vault round-trip for Login + key metadata read. Do
// not reorder without understanding the security/perf implication: a misconfig
// that would be rejected in real mode should never perform Vault I/O.
//
// ref: hashicorp/vault api/client.go@main — DefaultConfig + NewClient
// ref: hashicorp/vault api/auth/approle/approle.go — AppRole auth
func NewTransitKeyProviderFromEnv(realMode bool, clk clock.Clock) (*TransitKeyProvider, error) {
	// F-2: VAULT_ADDR is required — fail fast rather than silently defaulting to
	// the SDK loopback address (https://127.0.0.1:8200), which contradicts docs
	// that mark VAULT_ADDR as required and hides misconfigurations.
	addr := os.Getenv("VAULT_ADDR")
	if addr == "" {
		return nil, errcode.New(errcode.KindUnavailable, errcode.ErrVaultAuthFailed,
			"vault-transit: VAULT_ADDR is required (known values: Vault server address, e.g. https://vault.example.internal:8200)")
	}
	// SEC-FAIL-CLOSED: reject non-TLS Vault addresses before any SDK or network
	// operation. Loopback addresses are exempt for dev/CI testcontainer use.
	if err := secutil.ValidateTLSEndpoint(addr); err != nil {
		return nil, err
	}
	cfg := vaultapi.DefaultConfig()
	cfg.Address = addr

	// Fail-fast: construction failure is a configuration error, not an encrypt error.
	raw, err := vaultapi.NewClient(cfg)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrConfigKeyMissing,
			"vault-transit: create vault api client (check VAULT_ADDR)", err)
	}

	// A15 — apply VAULT_NAMESPACE before any I/O so Login + datakey + decrypt +
	// key reads + rotate all inherit the namespace header. Return value is the
	// applied namespace (empty when unset); discarded here because the helper
	// already emits the configuration log via slog. Namespace validation is
	// delegated to the vault SDK at first I/O — a malformed namespace surfaces
	// as a Login error with full context (addr / method / namespace).
	_ = applyNamespaceFromEnv(raw)

	// F-3a: Create the startup context FIRST so all I/O (including the wrapping
	// token unwrap in NewAuthMethodFromEnv) respects the startup deadline.
	// The original code created the ctx AFTER NewAuthMethodFromEnv, allowing
	// unwrapSecretID to block ~120s on an unreachable Vault.
	startupTimeout, err := resolveStartupTimeout()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()

	// Build auth method from env. VAULT_AUTH_METHOD is required.
	// ctx is passed so the wrapped secret-ID unwrap call is bounded (F-3a).
	auth, err := NewAuthMethodFromEnv(ctx, raw)
	if err != nil {
		return nil, err
	}

	// Real-mode guard: static token is not allowed in production.
	// IMPORTANT: this MUST run before NewTransitKeyProvider so a rejected
	// configuration fails fast without spending the Login + key metadata I/O.
	if realMode {
		if err := AssertForRealMode(auth); err != nil {
			return nil, err
		}
	}

	mountPath := os.Getenv("GOCELL_VAULT_TRANSIT_MOUNT")
	keyName := os.Getenv("GOCELL_VAULT_TRANSIT_KEY")

	client := NewVaultAPIClient(raw)
	p, err := NewTransitKeyProvider(ctx, client, mountPath, keyName, auth, clk)
	if err != nil {
		return nil, err
	}

	// F-4a: In real mode, reject non-renewable tokens. A non-renewable token
	// means the background renewal worker was not started, so the token will
	// silently expire without any alerting. Operators must configure the Vault
	// role to issue renewable tokens (token_type=default or service with
	// renewable=true).
	if realMode && !p.Renewable() {
		_ = p.Close(context.Background())
		return nil, errcode.New(errcode.KindUnavailable, errcode.ErrVaultAuthFailed,
			"vault-transit: non-renewable token rejected in real mode —"+
				" configure the Vault role to issue renewable tokens"+
				" (token_type=default or service with renewable=true)")
	}

	return p, nil
}

// Current returns the active KeyHandle for encrypting new values.
// Lock-free: serves cached latest_version when available, falls back to a
// Vault read on cache miss / invalidation.
func (p *TransitKeyProvider) Current(ctx context.Context) (kcrypto.KeyHandle, error) {
	if v := p.cachedLatestVersion.Load(); v > 0 {
		return p.handleForVersion(int(v)), nil
	}
	version, err := p.readLatestVersion(ctx)
	if err != nil {
		return nil, err
	}
	p.cachedLatestVersion.Store(int64(version))
	return p.handleForVersion(version), nil
}

// ByID returns the KeyHandle identified by keyID.
// Validates the "vault-transit:" prefix; wrong prefix → ErrKeyProviderKeyNotFound.
// Lock-free: handle fields are immutable after construction.
func (p *TransitKeyProvider) ByID(_ context.Context, keyID string) (kcrypto.KeyHandle, error) {
	if !strings.HasPrefix(keyID, vaultKeyIDPrefix) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrKeyProviderKeyNotFound,
			fmt.Sprintf("vault-transit: key ID %q does not have expected prefix %q", keyID, vaultKeyIDPrefix))
	}
	return &vaultTransitHandle{
		id:        keyID,
		mountPath: p.mountPath,
		keyName:   p.keyName,
		client:    p.client,
	}, nil
}

// Rotate generates a new key version via Vault Transit rotate API and refreshes
// the local version cache. Lock-free: Vault rotate is server-side atomic, and
// the version cache is an atomic.Int64.
//
// Failure semantics:
//   - If the rotate POST fails the cache is untouched and ErrKeyProviderRotateFailed
//     is returned; subsequent Current() continues to serve the prior version.
//   - If the post-rotate readLatestVersion fails the cache is left at 0 (already
//     invalidated); subsequent Current() will degrade to a Vault read on the next
//     call and refill the cache automatically — no further intervention needed.
//
// ref: hashicorp/vault builtin/logical/transit/path_keys.go@main
// ref: hashicorp/vault api-docs/secret/transit POST /transit/keys/:name/rotate
func (p *TransitKeyProvider) Rotate(ctx context.Context) (string, error) {
	slog.InfoContext(ctx, "vault-transit: rotating key",
		slog.String("key_name", p.keyName),
		slog.String("mount_path", p.mountPath))

	rotatePath := p.mountPath + "/keys/" + p.keyName + "/rotate"
	if _, err := p.client.Write(ctx, rotatePath, nil); err != nil {
		return "", errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderRotateFailed,
			"vault-transit: rotate key", err)
	}

	p.cachedLatestVersion.Store(0) // invalidate so the refresh below repopulates
	version, err := p.readLatestVersion(ctx)
	if err != nil {
		return "", errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderRotateFailed,
			"vault-transit: read key version after rotate", err)
	}
	p.cachedLatestVersion.Store(int64(version))

	return vaultKeyIDPrefix + fmt.Sprintf("v%d", version), nil
}

// handleForVersion builds a *vaultTransitHandle for the given key version.
// Reused by Current() to avoid duplicating the struct literal.
func (p *TransitKeyProvider) handleForVersion(version int) *vaultTransitHandle {
	return &vaultTransitHandle{
		id:        vaultKeyIDPrefix + fmt.Sprintf("v%d", version),
		mountPath: p.mountPath,
		keyName:   p.keyName,
		client:    p.client,
	}
}

// readLatestVersion reads the Vault key metadata and returns the latest_version integer.
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
		return 0, errcode.New(errcode.KindInternal, errcode.ErrKeyProviderKeyNotFound,
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
			return 0, errcode.New(errcode.KindInternal, errcode.ErrKeyProviderKeyNotFound,
				fmt.Sprintf("vault-transit: latest_version json.Number parse error: %v", err))
		}
		return int(n), nil
	default:
		return 0, errcode.New(errcode.KindInternal, errcode.ErrKeyProviderKeyNotFound,
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
		return errcode.Wrap(errcode.KindUnavailable, errcode.ErrKeyProviderTransient,
			"vault-transit: transient "+permanentMsg, err)
	}
	return errcode.Wrap(errcode.KindInternal, permanentCode,
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
// (transit/keys/{name}).
//
//   - 403 Forbidden (token revoked / insufficient permissions) →
//     ErrKeyProviderAuthFailed (distinct from key-not-found so operators can
//     route token failures separately from missing-key failures).
//   - 404 Not Found (missing key or mount) → ErrKeyProviderKeyNotFound.
//   - 429 / 408 / 5xx / network → ErrKeyProviderTransient (safe to retry).
func classifyVaultReadError(err error) error {
	var respErr *vaultapi.ResponseError
	if errors.As(err, &respErr) && respErr.StatusCode == 403 {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderAuthFailed,
			"vault-transit: read key metadata (Vault HTTP 403 — token revoked or permission denied)", err)
	}
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
// errCode is the errcode.Code to use on parse failure — callers should pass
// ErrKeyProviderEncryptFailed on the encrypt path and ErrKeyProviderDecryptFailed
// on the decrypt path so that the returned error code matches the operation.
//
// Returns "vault-transit:vN" on success, or an errcode wrapping errCode if the
// prefix is not in the expected "vault:vN:" format.
//
// ref: hashicorp/vault sdk/helper/keysutil/policy.go@main:L127 (version prefix)
// ref: kubernetes/kubernetes kmsv2/envelope.go@master (EncryptResponse.KeyID)
func parseVaultKeyID(ciphertext string, errCode errcode.Code) (string, error) {
	// Expected format: "vault:vN:base64payload"
	parts := strings.SplitN(ciphertext, ":", 3)
	if len(parts) != 3 || parts[0] != "vault" || !strings.HasPrefix(parts[1], "v") {
		// Do NOT include the full ciphertext in the error message — it contains
		// the wrapped DEK and would leak to server-side logs via the 5xx error chain.
		prefix := ciphertext
		if len(prefix) > 12 {
			prefix = prefix[:12] + "..."
		}
		return "", errcode.New(errcode.KindInternal, errCode,
			fmt.Sprintf("vault-transit: unexpected ciphertext prefix (want 'vault:vN:...'): %q", prefix),
			errcode.WithCategory(errcode.CategoryInfra))
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
// 3 seconds is sufficient for LAN Vault deployments.
const transitReadinessTimeout = 3 * time.Second

// Checkers returns a map of readiness probe functions for TransitKeyProvider.
// The single probe "vault_transit_ready" reads transit/keys/{keyName} metadata
// (the same path used by readLatestVersion) to verify that:
//   - The Vault token is valid and not revoked.
//   - The transit mount is enabled.
//   - The named key exists.
//
// The probe accepts a ctx from the /readyz handler; it further caps its own
// wait at transitReadinessTimeout (3 s) so a slow Vault does not hold the
// /readyz response indefinitely.
//
// This is intentionally NOT sys/health — sys/health only reports whether the
// Vault process is running and unsealed; it does NOT verify that the transit
// mount or the specific key are accessible. (vault#28846)
//
// ref: external-secrets/external-secrets pkg/provider/vault — ValidateStore
//
//	uses auth/token/lookup-self + business-path probe, not sys/health
func (p *TransitKeyProvider) Checkers() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"vault_transit_ready": func(ctx context.Context) error {
			probeCtx, cancel := context.WithTimeout(ctx, transitReadinessTimeout)
			defer cancel()
			_, err := p.readLatestVersion(probeCtx)
			return err
		},
	}
}

// RenewalMetrics returns the Prometheus collectors for token renewal and
// auth observability. The composition root must register these with its
// prometheus.Registerer so that renewal counters appear in /metrics scrapes.
// Returns nil when no renewal worker is configured (e.g. static token / no TokenRenewer).
//
// IMPORTANT: each TransitKeyProvider instance constructs a fresh set of
// collectors with identical metric names. Callers MUST register these to a
// dedicated *prometheus.Registry (or a scoped Registerer), NOT to
// prometheus.DefaultRegisterer — registering two instances' collectors to the
// same Registerer panics with "duplicate metrics collector registration"
// (the SDK enforces uniqueness of name+label tuples per Registerer). In tests
// that construct multiple providers, use prometheus.NewRegistry() per instance
// or guard with prometheus.WrapRegistererWith() labels.
func (p *TransitKeyProvider) RenewalMetrics() []prometheus.Collector {
	if p.renewalWorker == nil {
		return nil
	}
	w := p.renewalWorker
	collectors := []prometheus.Collector{w.renewSuccess, w.renewFailure}
	if w.authHealthy != nil {
		collectors = append(collectors, w.authHealthy)
	}
	if w.loginOutcome != nil {
		collectors = append(collectors, w.loginOutcome)
	}
	return collectors
}

// CacheVersionMetrics returns the Prometheus collector exposing the latest
// Vault Transit key version cached by this process. The collector is not
// registered here; composition roots own registry selection and duplicate
// handling.
func (p *TransitKeyProvider) CacheVersionMetrics() []prometheus.Collector {
	return []prometheus.Collector{
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Namespace: "gocell",
			Subsystem: "vault",
			Name:      "cached_key_version",
			Help:      "Latest Vault Transit key version cached by this process; 0 means cache miss.",
			ConstLabels: prometheus.Labels{
				"mount_path": p.mountPath,
				"key_name":   p.keyName,
			},
		}, func() float64 {
			return float64(p.cachedLatestVersion.Load())
		}),
	}
}

// Metrics returns all Prometheus collectors exposed by TransitKeyProvider.
func (p *TransitKeyProvider) Metrics() []prometheus.Collector {
	collectors := p.CacheVersionMetrics()
	collectors = append(collectors, p.RenewalMetrics()...)
	return collectors
}

// Worker returns the token renewal worker when one has been configured, or nil
// when the VaultClient does not implement TokenRenewer (e.g. test fakes with
// static tokens). The bootstrap layer skips WithWorkers registration for nil.
//
// ref: kernel/lifecycle.ManagedResource — "Returning nil means no background
//
//	goroutine is needed"
func (p *TransitKeyProvider) Worker() worker.Worker {
	if p.renewalWorker == nil {
		return nil
	}
	return p.renewalWorker
}

// Renewable reports whether the initial auth produced a renewable token.
// False for MethodToken or for misconfigured AppRole/K8s roles that return
// non-renewable tokens. When false, the background renewal worker is not
// started and operators will not be alerted when the token approaches expiry.
//
// In real mode, NewTransitKeyProviderFromEnv rejects a non-renewable result
// (F-4a): operators must configure the Vault role to issue renewable tokens.
func (p *TransitKeyProvider) Renewable() bool { return p.authRenewable }

// Close stops the token renewal worker (if configured) and satisfies the
// lifecycle.ManagedResource contract. The underlying VaultClient manages
// HTTP connection pooling via net/http.Client, which requires no explicit close.
func (p *TransitKeyProvider) Close(ctx context.Context) error {
	if p.renewalWorker != nil {
		return p.renewalWorker.Stop(ctx)
	}
	return nil
}

// initTokenRenewal configures background token renewal when the client
// implements TokenRenewer and the auth result is renewable.
//
// Non-renewable tokens (e.g. static VAULT_TOKEN via MethodToken): a Warn log
// is emitted and no worker is created. The provider still functions but the
// token will expire without notification.
//
// Renewable tokens: LookupSelfToken seeds the LifetimeWatcher with accurate TTL.
// authHealthy is seeded to 1. loginOutcome CounterVec is registered with
// {method, result, reason} labels.
//
// ref: hashicorp/vault api/lifetime_watcher.go@main — LifetimeWatcher usage pattern
// ref: external-secrets/external-secrets pkg/provider/vault — ValidateStore (token lookup probe)
func (p *TransitKeyProvider) initTokenRenewal(ctx context.Context, result AuthResult) error {
	if !result.Renewable {
		p.logger.WarnContext(ctx,
			"vault-transit: auth token is not renewable; background renewal disabled",
			slog.String("method", string(p.authMethod.Method())))
		return nil
	}

	renewer, ok := p.client.(TokenRenewer)
	if !ok {
		p.logger.WarnContext(ctx,
			"vault-transit: VaultClient does not support token renewal; "+
				"token will expire without notification")
		return nil
	}

	// Look up the current token to seed the LifetimeWatcher with accurate TTL.
	secret, err := renewer.LookupSelfToken(ctx)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderAuthFailed,
			"vault-transit: lookup self token for renewal initialisation", err)
	}

	raw, err := renewer.NewLifetimeWatcher(&vaultapi.LifetimeWatcherInput{
		Secret: secret,
	})
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrKeyProviderAuthFailed,
			"vault-transit: create lifetime watcher", err)
	}
	if raw == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrKeyProviderAuthFailed,
			"vault-transit: NewLifetimeWatcher returned nil without error")
	}

	authHealthy := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "gocell",
		Subsystem: "vault",
		Name:      "token_auth_healthy",
		Help:      "1 when Vault token renewal is healthy; 0 when the background renewer is re-authenticating after a terminal renewal failure.",
	})
	authHealthy.Set(1)

	loginOutcome := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gocell",
		Subsystem: "vault",
		Name:      "auth_login_total",
		Help:      "Count of Vault auth Login attempts.",
	}, []string{"method", "result", "reason"})

	p.renewalWorker = &tokenRenewalWorker{
		client:     renewer,
		authMethod: p.authMethod,
		logger:     p.logger,
		clock:      p.clock,
		renewSuccess: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "gocell",
			Subsystem: "vault",
			Name:      "token_renew_success_total",
			Help:      "Number of successful Vault token renewals.",
		}),
		renewFailure: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "gocell",
			Subsystem: "vault",
			Name:      "token_renew_failure_total",
			Help:      "Number of Vault token renewal failures.",
		}),
		authHealthy:    authHealthy,
		loginOutcome:   loginOutcome,
		currentWatcher: &vaultLifetimeWatcherAdapter{w: raw},
	}
	return nil
}
