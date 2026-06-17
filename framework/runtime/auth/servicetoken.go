package auth

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/httputil"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

const (
	msgInvalidServiceTokenFormat = "invalid service token format"
	// msgInternalServerError is the canonical client-visible 5xx message, kept
	// in lockstep with httputil.WriteError's 5xx mask so probes/tests assert
	// against a single string.
	msgInternalServerError = "internal server error"

	// msgLegacy2Part and msgLegacy3Part are the canonical message literals emitted
	// by verifyServiceTokenPayload for legacy token format rejections. They are
	// defined as constants so classifyServiceTokenVerifyError can match against
	// errcode.Error.Message (exact field comparison) rather than the formatted
	// err.Error() string (which includes the "[CODE] " prefix and is fragile to
	// message text changes).
	msgLegacy2Part = "legacy 2-part service token format rejected"
	msgLegacy3Part = "legacy 3-part service token format rejected"
	// msgExpired is the canonical message for service token expiry; paired with
	// ErrAuthTokenExpired code (already unique, but kept here for consistency).
	msgExpired = "service token expired"
	// msgFutureTimestamp is the canonical message for tokens with future timestamps.
	msgFutureTimestamp = "service token timestamp is too far in the future"
	// msgInvalidMAC is the canonical message for HMAC verification failure.
	msgInvalidMAC = "invalid service token MAC"
	// msgCallerCellMissing and msgCallerCellInvalid are the canonical messages
	// emitted by validateCallerCell for missing and pattern-invalid caller cell IDs.
	msgCallerCellMissing = "caller cell missing"
	msgCallerCellInvalid = "caller cell id invalid"
)

// WithServiceTokenLogger sets the logger for ServiceTokenMiddleware.
func WithServiceTokenLogger(l *slog.Logger) ServiceTokenOption {
	return func(c *serviceTokenConfig) {
		if l != nil {
			c.logger = l
		}
	}
}

// ServiceTokenMaxAge is the maximum age of a service token before it is
// rejected. Tokens with timestamps at or beyond this window are refused.
const ServiceTokenMaxAge = 5 * time.Minute

// ServiceTokenClockSkew is the maximum future timestamp skew accepted for
// service tokens. It is intentionally separate from ServiceTokenMaxAge:
// old tokens expire by age, while future-dated tokens are only tolerated for
// bounded clock drift.
const ServiceTokenClockSkew = 30 * time.Second

// ServiceTokenNonceTTL is the required replay-retention window for nonce
// stores used with service tokens. It covers the full token validity window
// plus the accepted future clock skew.
const ServiceTokenNonceTTL = ServiceTokenMaxAge + ServiceTokenClockSkew

// MinHMACKeyBytes aliases kauth.MinHMACKeyBytes so runtime/auth and kernel/cell
// share a single canonical strength threshold (NIST SP 800-107: HMAC-SHA-256
// requires ≥256-bit keys). Kept as a const for callers that reference the
// runtime/auth namespace; the source of truth lives in kernel/cell.
const MinHMACKeyBytes = kauth.MinHMACKeyBytes

// HeaderTenantID is the canonical HTTP header carrying the caller's tenant
// assertion on internal service-token requests, and the single signed header in
// the service-token MAC material. buildServiceTokenMessage folds its value into
// the HMAC unconditionally and verifyServiceTokenPayload reconstructs the same
// value from r.Header.Get(HeaderTenantID), so tampering with, injecting, or
// stripping the header changes the MAC input and fails verification
// (defense-in-depth, AWS SigV4 SignedHeaders style).
//
// INVARIANT: X-Tenant-ID is unconditionally part of the service-token MAC
// material; sign and verify share buildServiceTokenMessage. Dropping or altering
// the tenant segment breaks the golden + tamper/inject/strip negative tests in
// servicetoken_tenant_sign_test.go.
const HeaderTenantID = "X-Tenant-ID"

// HeaderPrincipal is the canonical HTTP header carrying the caller's propagated
// business principal (base64url compact JSON of outbox.PrincipalMetadata with
// TenantID cleared). It is integrity-bound into the service-token MAC via the
// "x-gocell-principal=<value>" segment appended by buildServiceTokenMessage,
// so tampering with, injecting, or stripping the header invalidates the MAC
// (same defense-in-depth mechanism as HeaderTenantID).
//
// INVARIANT: X-Gocell-Principal is unconditionally part of the service-token
// MAC material when set by SignInternalRequest; sign and verify share
// buildServiceTokenMessage. The segment is appended after "x-tenant-id=<value>",
// so the two signed headers are unambiguously ordered.
const HeaderPrincipal = "X-Gocell-Principal"

// serviceTokenConfig holds per-middleware options.
type serviceTokenConfig struct {
	clk        clock.Clock
	logger     *slog.Logger
	nonceStore NonceStore
	metrics    *AuthMetrics
}

// ServiceTokenOption configures ServiceTokenMiddleware behavior.
type ServiceTokenOption func(*serviceTokenConfig)

// WithServiceTokenNonceStore configures the replay-defense store. The
// middleware rejects nonces already consumed within the store's TTL window.
// Replay protection is mandatory — both ServiceTokenMiddleware and
// NewServiceTokenAuthenticator reject nil/Noop NonceStore at construction
// time. Use NewInMemoryNonceStore(ServiceTokenNonceTTL, clock.Real()) for dev/test wiring.
//
// Passing nil is a no-op: cfg.nonceStore stays nil and construction will fail.
func WithServiceTokenNonceStore(ns NonceStore) ServiceTokenOption {
	return func(c *serviceTokenConfig) {
		if validation.IsNilInterface(ns) {
			return
		}
		c.nonceStore = ns
	}
}

// WithServiceTokenMetrics sets the AuthMetrics for ServiceTokenMiddleware.
func WithServiceTokenMetrics(m *AuthMetrics) ServiceTokenOption {
	return func(c *serviceTokenConfig) { c.metrics = m }
}

// serviceTokenHKDFInfo is the HKDF info-prefix for per-cell service-token subkey
// derivation; the cell id is appended to form info = prefix + cellID. The "/v1/"
// segment namespaces the scheme so a future derivation change is a distinct,
// non-colliding key space.
//
// BREAKING: changing this constant (even the version segment) re-derives every
// per-cell subkey → all previously provisioned split cells must re-run
// `gocell derive-service-keys` and redeploy atomically, or cross-cell verification
// fails. Treat any edit as a wire-affecting key rotation.
//
//nolint:gosec // G101: not a credential — this is a fixed HKDF domain-separation label.
const serviceTokenHKDFInfo = "gocell/service-token/v1/caller-cell:"

// deriveCellSecret derives a per-cell HMAC subkey from a parent secret using
// HKDF-SHA256 (RFC 5869) with the cell id as info context. Distinct cell ids
// yield independent subkeys, and knowledge of one subkey reveals neither the
// parent nor any sibling subkey — the cryptographic basis for per-cell caller
// isolation (#2153). The output is MinHMACKeyBytes long (HMAC-SHA256 strength).
//
// ref: RFC 5869 §2 (HKDF-Expand with per-context info)
func deriveCellSecret(parent []byte, cellID string) ([]byte, error) {
	return hkdf.Key(sha256.New, parent, nil, serviceTokenHKDFInfo+cellID, MinHMACKeyBytes)
}

// HMACKeyRing is the monolith (single trust domain) ServiceKeyring: it holds the
// master secret(s) and derives per-cell subkeys on demand via HKDF. Position 0
// (current) is the active master; previous (optional) covers a rotation overlap
// window — verification tries both in order.
//
// SECURITY: in a monolith every cell runs in one process that holds the master,
// so a process compromise yields the master and thus any per-cell subkey — HKDF
// here provides NO cross-cell isolation. Per-cell isolation (a compromised cell
// cannot forge another cell) is delivered only by ProvisionedKeyring (split:
// master-absent, only this cell's signing subkey + its declared callers' verify
// subkeys). See kauth.ServiceKeyring godoc and ADR 202606131142-1423 §#2153.
//
// ref: zeromicro/go-zero rest/token/tokenparser.go — dual-key [current, previous] model
// ref: gorilla/securecookie — DecodeMulti try-all-keys pattern
type HMACKeyRing struct {
	current  []byte
	previous []byte
}

// Compile-time assertion: HMACKeyRing implements the kernel ServiceKeyring.
var _ kauth.ServiceKeyring = (*HMACKeyRing)(nil)

// NewHMACKeyRing creates an HMACKeyRing master keyring. current must be at least
// MinHMACKeyBytes (32 bytes). previous may be nil for single-secret mode; if set,
// it must also meet the minimum length.
func NewHMACKeyRing(current, previous []byte) (*HMACKeyRing, error) {
	if len(current) == 0 {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrAuthKeyMissing, "current HMAC secret must not be empty")
	}
	if len(current) < MinHMACKeyBytes {
		return nil, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthKeyInvalid,
			"current HMAC secret is too short",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("got=%d min=%d", len(current), MinHMACKeyBytes))))
	}
	if len(previous) > 0 && len(previous) < MinHMACKeyBytes {
		return nil, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthKeyInvalid,
			"previous HMAC secret is too short",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("got=%d min=%d", len(previous), MinHMACKeyBytes))))
	}
	return &HMACKeyRing{
		current:  current,
		previous: previous,
	}, nil
}

// SigningSecrets returns the per-cell subkeys (current first, then previous) for
// signing as ownCell, derived from the master via HKDF. A monolith may sign as
// any cell, so ownCell is never rejected here (only the empty id is invalid).
func (r *HMACKeyRing) SigningSecrets(ownCell string) ([][]byte, error) {
	return r.deriveAll(ownCell)
}

// VerifySecrets returns the per-cell subkeys (current first, then previous) for
// verifying a token claiming callerCell, derived from the master via HKDF.
func (r *HMACKeyRing) VerifySecrets(callerCell string) ([][]byte, error) {
	return r.deriveAll(callerCell)
}

// deriveAll derives the cell subkey from current (and previous, when set),
// returning them in verification try-order.
func (r *HMACKeyRing) deriveAll(cellID string) ([][]byte, error) {
	if cellID == "" {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrAuthKeyInvalid,
			"service keyring derivation requires a non-empty cell id")
	}
	cur, err := deriveCellSecret(r.current, cellID)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrAuthKeyInvalid, "derive current cell subkey", err)
	}
	out := [][]byte{cur}
	if len(r.previous) > 0 {
		prev, err := deriveCellSecret(r.previous, cellID)
		if err != nil {
			return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrAuthKeyInvalid, "derive previous cell subkey", err)
		}
		out = append(out, prev)
	}
	return out, nil
}

// Validate reports whether the master secret(s) meet MinHMACKeyBytes. Derived
// subkeys are always exactly MinHMACKeyBytes, so validating the master suffices.
func (r *HMACKeyRing) Validate() error {
	if len(r.current) < MinHMACKeyBytes {
		return errcode.New(errcode.KindInternal, errcode.ErrAuthKeyInvalid,
			"current HMAC master secret is too short",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("got=%d min=%d", len(r.current), MinHMACKeyBytes))))
	}
	if len(r.previous) > 0 && len(r.previous) < MinHMACKeyBytes {
		return errcode.New(errcode.KindInternal, errcode.ErrAuthKeyInvalid,
			"previous HMAC master secret is too short",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("got=%d min=%d", len(r.previous), MinHMACKeyBytes))))
	}
	return nil
}

const (
	// EnvServiceSecret is the environment variable for the current HMAC secret.
	EnvServiceSecret = "GOCELL_SERVICE_SECRET"
	// EnvServiceSecretPrevious is the environment variable for the previous HMAC secret.
	EnvServiceSecretPrevious = "GOCELL_SERVICE_SECRET_PREVIOUS"
)

// LoadHMACKeyRingFromEnv loads an HMACKeyRing from environment variables.
// GOCELL_SERVICE_SECRET is required; GOCELL_SERVICE_SECRET_PREVIOUS is optional.
//
// Both variables are read as raw UTF-8 strings and used directly as HMAC key
// bytes (no base64 decoding is performed). The value must be at least 32 bytes
// long. To generate a suitable value: openssl rand -base64 32.
func LoadHMACKeyRingFromEnv() (*HMACKeyRing, error) {
	current := os.Getenv(EnvServiceSecret)
	if current == "" {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrAuthKeyMissing,
			"environment variable GOCELL_SERVICE_SECRET is not set")
	}

	previous := os.Getenv(EnvServiceSecretPrevious)
	var prevBytes []byte
	if previous != "" {
		prevBytes = []byte(previous)
	}

	return NewHMACKeyRing([]byte(current), prevBytes)
}

// ServiceTokenMiddleware validates requests using HMAC-SHA256 service tokens.
// The token is expected in the Authorization header as the 4-part payload:
//
//	ServiceToken {unix_timestamp}:{nonce}:{callerCell}:{hex_hmac}
//
// The HMAC is computed over
// "{method} {path}[?{canonicalQuery}] {timestamp} {nonce} {callerCell} x-tenant-id=<value> x-gocell-principal=<value>"
// — i.e. the caller cell is a MAC segment, and both signed headers are
// unconditionally folded as space-delimited trailing segments: first
// "x-tenant-id=<value>" (empty for no-tenant requests), then
// "x-gocell-principal=<value>" (empty when no business principal is propagated).
// buildServiceTokenMessage is the single source for this material; sign
// (GenerateServiceToken) and verify (verifyServiceTokenPayload) both call it, so
// tampering with, injecting, or stripping the caller identity, the tenant header,
// or the principal header invalidates the MAC (see HeaderTenantID,
// HeaderPrincipal).
//
// Verification tries each secret in the key ring in order (current, then
// previous). Tokens older than 5 minutes (exclusive boundary) are rejected.
//
// Principal construction is fully delegated to NewServiceTokenAuthenticator
// so that the service identity shape is defined in a single place.
//
// ring accepts kauth.ServiceKeyring (the kernel interface); *HMACKeyRing
// (monolith master-derived) and *ProvisionedKeyring (split) both satisfy it.
//
// Misconfiguration paths (nil ring, sub-strength HMAC, missing/Noop NonceStore,
// authenticator build failure) return an error middleware that serves 500 on every
// request. All misconfiguration paths share the same errorMiddlewareInternal helper
// so the 500 behavior is consistent and observable via the "internal" metric label.
func ServiceTokenMiddleware(ring kauth.ServiceKeyring, clk clock.Clock, opts ...ServiceTokenOption) func(http.Handler) http.Handler {
	clock.MustHaveClock(clk, "auth.ServiceTokenMiddleware")
	cfg := serviceTokenConfig{
		clk:    clk,
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(&cfg)
	}

	if validation.IsNilInterface(ring) {
		return errorMiddlewareInternal(cfg, "service token middleware called with nil key ring")
	}

	// Defense-in-depth strength check (PR269 round-3 F5): auth.NewAuthServiceToken
	// already enforces key strength at construction time via Validate(), but
	// ServiceTokenMiddleware is also reachable via direct call paths (tests,
	// custom wiring) that bypass the kernel constructor. Reject invalid rings here
	// so no path leaks a short HMAC secret into hmac.New.
	if err := ring.Validate(); err != nil {
		return errorMiddlewareInternal(cfg, "HMAC ring invalid: "+err.Error())
	}

	if cfg.nonceStore == nil {
		return errorMiddlewareInternal(cfg,
			"nonce store not configured (use WithServiceTokenNonceStore)")
	}
	if cfg.nonceStore.Kind() == NonceStoreKindNoop {
		return errorMiddlewareInternal(cfg,
			"NonceStoreKindNoop not allowed; replay protection mandatory")
	}

	// Construct a single Authenticator instance for the lifetime of this
	// middleware. All validation and Principal construction is delegated here,
	// eliminating the previously duplicated logic in handleServiceToken.
	authenticator, err := NewServiceTokenAuthenticator(ring, clk, opts...)
	if err != nil {
		return errorMiddlewareInternal(cfg, "authenticator build failed: "+err.Error())
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleServiceToken(cfg, authenticator, next, w, r)
		})
	}
}

// errorMiddlewareInternal returns a middleware that fails every request with
// 500 ERR_INTERNAL. Used for misconfiguration paths (nil ring, short HMAC,
// missing/Noop NonceStore, authenticator build failure) so the listener
// fails closed instead of silently falling through.
func errorMiddlewareInternal(cfg serviceTokenConfig, reason string) func(http.Handler) http.Handler {
	return func(_ http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cfg.metrics.recordServiceVerify(r.Context(), "failure", "internal")
			cfg.logger.Error("service token middleware misconfigured",
				slog.String("reason", reason),
				slog.String("path", r.URL.Path))
			httputil.WriteError(r.Context(), w,
				errcode.New(errcode.KindInternal, errcode.ErrInternal, msgInternalServerError))
		})
	}
}

// handleServiceToken contains all request-handling logic extracted from
// ServiceTokenMiddleware to reduce cognitive complexity.
// Validation and Principal construction are fully delegated to the provided
// Authenticator (NewServiceTokenAuthenticator); this function maps the returned
// error to granular metrics labels and HTTP responses, preserving all existing
// metric reason labels and HTTP status codes.
func handleServiceToken(cfg serviceTokenConfig, auth Authenticator, next http.Handler, w http.ResponseWriter, r *http.Request) {
	token := extractServiceToken(r)
	if token == "" {
		cfg.metrics.recordServiceVerify(r.Context(), "failure", "missing")
		httputil.WriteError(r.Context(), w,
			errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "missing service token"))
		return
	}

	// Extract callerCell from the 4-part payload for logging purposes.
	// parts: [ts, nonce, callerCell, mac]; safe to read index 2 when len>=3.
	callerCell := ""
	if parts := strings.SplitN(token, ":", 4); len(parts) >= 3 {
		callerCell = parts[2]
	}

	p, ok, err := auth.Authenticate(r)
	if err != nil {
		writeServiceTokenError(cfg, err, callerCell, w, r)
		return
	}
	if !ok {
		// Absent: no ServiceToken header (already handled by extractServiceToken
		// above, so this branch is a safety net for unexpected absent outcomes).
		cfg.metrics.recordServiceVerify(r.Context(), "failure", "missing")
		httputil.WriteError(r.Context(), w,
			errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "missing service token"))
		return
	}

	cfg.metrics.recordServiceVerify(r.Context(), "success", "ok")
	ctx := WithPrincipal(r.Context(), p)
	// Producer-side principal bridge — identical to the JWT path
	// (middleware.go). Without this, an outbox.Entry produced downstream on a
	// service-token-authenticated request (e.g. /internal/v1/access/roles/assign)
	// would carry an empty Principal across the async boundary. Service principals
	// express identity via CallerCellID (Subject is empty), so injectPrincipalCtxKeys
	// stamps actor_id = CallerCellID; subject_id / session_id stay unset (no source).
	ctx = injectPrincipalCtxKeys(ctx, p)
	// Business principal propagation: if the request carried X-Gocell-Principal,
	// the MAC already validated its integrity. Decode and restore the business
	// actor/subject/session so downstream handlers see the original user identity,
	// not just the CallerCellID service actor.
	if ph := r.Header.Get(HeaderPrincipal); ph != "" {
		ctx = rebuildPropagatedPrincipal(ctx, ph)
	}
	next.ServeHTTP(w, r.WithContext(ctx))
}

// writeServiceTokenError maps a verifyServiceTokenPayload error to granular
// metrics labels and the appropriate HTTP response. It preserves the
// middleware's split between 401 (auth failures, replay), 503 (nonce store
// full), and 500 (nonce store infrastructure failures) by inspecting the
// wrapped Cause.
//
// callerCell is extracted from the raw token payload for structured logging;
// it may be empty for legacy-format or malformed tokens.
//
// Branch order is load-bearing: the ErrNonceReused check MUST precede the
// generic Cause check. Both replay and store-infra errors are wrapped via
// WrapAuth and therefore carry a non-nil Cause; swapping the checks would
// classify every replay as an infrastructure failure (500) instead of an
// auth failure (401), downgrading a security signal to a server error.
func writeServiceTokenError(cfg serviceTokenConfig, err error, callerCell string, w http.ResponseWriter, r *http.Request) {
	// errors.Is traverses the full chain, so ErrNonceReused in the Cause matches.
	if errors.Is(err, ErrNonceReused) {
		cfg.metrics.recordServiceVerify(r.Context(), "failure", "replay")
		// httputil.WriteError only logs at 5xx, but replay is a security signal
		// that operators must be able to attribute back to caller IP / path /
		// request_id. Emit a structured Warn here so the alerting-rules.md
		// triage step "grep slog code=ERR_AUTH_REPLAY_DETECTED" produces hits.
		cfg.logger.WarnContext(
			r.Context(), "service token replay detected",
			slog.String("code", string(errcode.ErrAuthReplayDetected)),
			slog.String("path", r.URL.Path),
			slog.String("remote", r.RemoteAddr),
			slog.String("caller_cell", callerCell),
		)
		httputil.WriteError(r.Context(), w,
			errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthReplayDetected, "service token replay detected"))
		return
	}

	// ErrNonceStoreFull: store is at capacity with no expired entries to reclaim.
	// Return 503 (transient; not a replay signal, not a permanent auth failure).
	if errors.Is(err, ErrNonceStoreFull) {
		cfg.metrics.recordServiceVerify(r.Context(), "failure", "nonce_store_full")
		cfg.logger.Error("nonce store full; rejecting request",
			slog.String("path", r.URL.Path))
		httputil.WriteError(r.Context(), w,
			errcode.New(errcode.KindUnavailable, errcode.ErrNonceStoreFull, "service temporarily unavailable"))
		return
	}

	// If the error has a Cause (wrapped by WrapAuth for nonce check failures)
	// that is NOT ErrNonceReused or ErrNonceStoreFull, it is a nonce store
	// infrastructure failure.
	var ec *errcode.Error
	if errors.As(err, &ec) && ec.Cause != nil {
		cfg.metrics.recordServiceVerify(r.Context(), "failure", "nonce_store_error")
		cfg.logger.Error("nonce store check failed", slog.Any("error", ec.Cause))
		httputil.WriteError(r.Context(), w,
			errcode.New(errcode.KindInternal, errcode.ErrInternal, msgInternalServerError))
		return
	}

	// Classify remaining auth errors by their message for metric granularity.
	reason := classifyServiceTokenVerifyError(err)
	switch reason {
	case "legacy_format":
		cfg.logger.WarnContext(
			r.Context(), "legacy service token format rejected",
			slog.String("path", r.URL.Path),
			slog.String("format", "2-part"),
		)
	case "missing_caller_cell":
		cfg.logger.WarnContext(
			r.Context(), "service token missing caller cell",
			slog.String("path", r.URL.Path),
			slog.String("remote", r.RemoteAddr),
			slog.String("caller_cell", callerCell),
		)
	case "invalid_caller_cell":
		cfg.logger.WarnContext(
			r.Context(), "service token invalid caller cell",
			slog.String("path", r.URL.Path),
			slog.String("remote", r.RemoteAddr),
			slog.String("caller_cell", callerCell),
		)
	case "invalid_format":
		cfg.logger.WarnContext(
			r.Context(), "service token invalid format",
			slog.String("path", r.URL.Path),
			slog.String("remote", r.RemoteAddr),
			slog.String("caller_cell", callerCell),
		)
	}
	cfg.metrics.recordServiceVerify(r.Context(), "failure", reason)
	httputil.WriteError(r.Context(), w,
		errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "invalid service token"))
}

// classifyServiceTokenVerifyError maps a verifyServiceTokenPayload error to a
// metric reason label. This mirrors the legacy per-branch labels from the
// original handleServiceToken implementation.
//
// Classification uses errors.As to extract *errcode.Error and then matches on
// the .Code and .Message fields directly — not on the formatted err.Error()
// string (which includes the "[CODE] " prefix). This approach is robust to
// code-prefix format changes and locks each branch to a const-literal message
// defined in this file, so any message change triggers a compile-time const
// mismatch rather than a silent metric label downgrade.
func classifyServiceTokenVerifyError(err error) string {
	if err == nil {
		return "ok"
	}
	var ec *errcode.Error
	if errors.As(err, &ec) {
		// ErrAuthTokenExpired uniquely identifies the expiry family (future
		// timestamp and past-MaxAge both use this code). No message disambiguation
		// needed — all token-time failures map to "expired".
		if ec.Code == errcode.ErrAuthTokenExpired {
			return "expired"
		}
		// Remaining classification is on Message, which is a const literal defined
		// in this file and used at the errcode.New call site. Any message change
		// in verifyServiceTokenPayload / validateCallerCell must update these consts
		// simultaneously, making drift a compile-time or test failure rather than
		// a silent metric label regression.
		switch ec.Message {
		case msgLegacy2Part, msgLegacy3Part:
			return "legacy_format"
		case msgInvalidMAC:
			return "invalid_mac"
		case msgCallerCellMissing:
			return "missing_caller_cell"
		case msgCallerCellInvalid:
			return "invalid_caller_cell"
		}
	}
	return "invalid_format"
}

// verifyServiceTokenMAC checks whether the provided MAC is valid for message
// under the per-cell verify subkeys for callerCell (current, then previous).
// Returns false fail-closed when the keyring does not authorize callerCell
// (split least-privilege: the callee holds verify subkeys only for its declared
// callers) or derivation fails.
func verifyServiceTokenMAC(ring kauth.ServiceKeyring, callerCell, message string, providedMAC []byte) bool {
	secrets, err := ring.VerifySecrets(callerCell)
	if err != nil {
		return false
	}
	for _, secret := range secrets {
		mac := hmac.New(sha256.New, secret)
		_, _ = mac.Write([]byte(message))
		if hmac.Equal(providedMAC, mac.Sum(nil)) {
			return true
		}
	}
	return false
}

// buildServiceTokenMessage constructs the canonical HMAC message for the
// 4-part token format. The query string is canonicalized (keys sorted) and
// appended to the path when non-empty. callerCell is included as a segment so
// tampering with the caller identity invalidates the MAC. tenantID (the
// X-Tenant-ID signed header) is unconditionally folded as the
// "x-tenant-id=<value>" segment — empty for requests that assert no tenant.
// principalHeader (the X-Gocell-Principal signed header) is unconditionally
// folded as the final "x-gocell-principal=<value>" segment — empty when no
// business principal is propagated. Both signed-header segments are appended
// after the callerCell segment so tampering with, injecting, or stripping either
// header invalidates the MAC (defense-in-depth, AWS SigV4 SignedHeaders style).
// The label is lowercased canonical form. This function is the single source for
// the MAC message; sign and verify must both call it.
//
// SECURITY: segments are space-delimited. callerCell is validated against
// ^[a-z][a-z0-9]+$ before this is called; tenantID is either "" or a canonical
// UUID; principalHeader is either "" or a base64url string (no spaces). The two
// trailing signed-header segments are unambiguous in ordering and character set.
func buildServiceTokenMessage(method, path, rawQuery, tsStr, nonce, callerCell, tenantID, principalHeader string) string {
	cq := canonicalQuery(rawQuery)
	suffix := " x-tenant-id=" + tenantID + " x-gocell-principal=" + principalHeader
	if cq != "" {
		return fmt.Sprintf("%s %s?%s %s %s %s", method, path, cq, tsStr, nonce, callerCell) + suffix
	}
	return fmt.Sprintf("%s %s %s %s %s", method, path, tsStr, nonce, callerCell) + suffix
}

// canonicalQuery returns a deterministic encoding of rawQuery with keys sorted.
// Returns empty string when rawQuery is empty. Falls back to rawQuery if
// url.ParseQuery fails.
func canonicalQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	params, err := url.ParseQuery(rawQuery)
	if err != nil {
		return rawQuery
	}
	return params.Encode() // url.Values.Encode() sorts keys alphabetically
}

// GenerateServiceToken creates a service token for the given callerCell, method,
// path, optional rawQuery, tenantID, principalHeader, and timestamp using the
// current secret from the key ring. The token format is
// "{timestamp}:{nonce}:{callerCell}:{hex_hmac}" where nonce is 16
// cryptographically random bytes, hex-encoded. It returns an empty string if:
//   - ring is nil
//   - callerCell is empty (mandatory — identifies the originating cell)
//   - callerCell contains ':' (would corrupt the 4-part token structure)
//
// rawQuery is canonicalized separately from path so the HMAC message remains
// stable across query parameter ordering. Pass "" when the request has no query
// parameters.
//
// tenantID is the X-Tenant-ID assertion folded into the MAC so the tenant is
// integrity-bound to the caller credential. Pass the zero value
// (tenant.TenantID("")) when the request asserts no tenant.
//
// principalHeader is the X-Gocell-Principal value (base64url compact JSON of
// outbox.PrincipalMetadata, produced by SignInternalRequest). It is folded into
// the MAC unconditionally so tampering with, injecting, or stripping the
// X-Gocell-Principal header invalidates the MAC. Pass "" when no business
// principal is propagated — the MAC then binds an empty principal segment.
//
// The token is signed with the per-cell HKDF subkey for callerCell (derived from
// the ring's signing material), so the MAC is keyed to the originating cell — a
// process lacking callerCell's subkey cannot produce a valid token (#2153).
//
// Production code MUST use SignInternalRequest instead of calling this function
// directly. The SVCTOKEN-CALLER-CELL-REQUIRED-01 archtest enforces this.
func GenerateServiceToken(
	ring kauth.ServiceKeyring, callerCell, method, path, rawQuery string,
	tenantID tenant.TenantID, principalHeader string, ts time.Time,
) string {
	if validation.IsNilInterface(ring) {
		return ""
	}
	if callerCell == "" {
		return ""
	}
	if strings.Contains(callerCell, ":") {
		return ""
	}

	// Sign with the per-cell subkey for callerCell (current = position 0).
	secrets, err := ring.SigningSecrets(callerCell)
	if err != nil || len(secrets) == 0 {
		return ""
	}

	tsStr := strconv.FormatInt(ts.Unix(), 10)

	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		// crypto/rand failure is not recoverable; return empty to signal error.
		return ""
	}
	nonce := hex.EncodeToString(nonceBytes)

	message := buildServiceTokenMessage(method, path, rawQuery, tsStr, nonce, callerCell, tenantID.String(), principalHeader)
	mac := hmac.New(sha256.New, secrets[0])
	_, _ = mac.Write([]byte(message))
	return tsStr + ":" + nonce + ":" + callerCell + ":" + hex.EncodeToString(mac.Sum(nil))
}

func extractServiceToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "servicetoken") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
