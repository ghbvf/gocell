package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/observability"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
)

const (
	// headerIdempotencyKey is the request header carrying the client-chosen key.
	headerIdempotencyKey = "Idempotency-Key"

	// headerIdempotencyReplayed is set to "true" on replayed responses so
	// clients can detect that the response was served from store.
	headerIdempotencyReplayed = "Idempotency-Replayed"

	// defaultMaxBodyBytes is the maximum response body size to buffer for
	// future replay. Responses larger than this are not stored.
	defaultMaxBodyBytes = 256 * 1024

	// maxIdempotencyKeyLen is the maximum permitted byte length of an
	// Idempotency-Key header value. Values longer than this are rejected with
	// 400 to prevent oversized Redis keys and memory-amplification DoS.
	maxIdempotencyKeyLen = 256

	// msgStoreUnavailable is the client-visible message for store errors.
	// Must be a const literal (MESSAGE-CONST-LITERAL-01 archtest).
	msgStoreUnavailable = "idempotency store unavailable"

	// msgInProgress is the client-visible message for concurrent in-flight keys.
	// Must be a const literal (MESSAGE-CONST-LITERAL-01 archtest).
	msgInProgress = "a request with this Idempotency-Key is already in progress"

	// msgKeyTooLong is the client-visible message when the Idempotency-Key
	// header value exceeds maxIdempotencyKeyLen bytes.
	// Must be a const literal (MESSAGE-CONST-LITERAL-01 archtest).
	msgKeyTooLong = "Idempotency-Key header value exceeds maximum length"

	// msgKeyInvalidChars is the client-visible message when the Idempotency-Key
	// header value contains characters that are invalid (curly braces or
	// non-printable ASCII). Rejected at middleware edge (before any store I/O)
	// to prevent Redis hashtag confusion and avoid opaque store errors.
	// Must be a const literal (MESSAGE-CONST-LITERAL-01 archtest).
	msgKeyInvalidChars = "Idempotency-Key header value contains invalid characters"

	// msgKeyReused is the client-visible message when the same Idempotency-Key
	// is presented with a different request body (fingerprint mismatch).
	// Must be a const literal (MESSAGE-CONST-LITERAL-01 archtest).
	msgKeyReused = "idempotency key reused with a different request body"

	// maxMismatchedFields caps the number of differing field names reported in
	// the 422 key-reused response details, bounding response size when a request
	// body has many top-level fields. When the diff exceeds this, the names are
	// truncated and a mismatchedFieldsTruncated=true detail is added.
	maxMismatchedFields = 20

	// maxFieldNameLen caps the byte length of each reported field name. Field
	// names come from client-controlled JSON keys, so an over-long name is
	// truncated (with a … marker) to bound 422 response size.
	maxFieldNameLen = 128

	// hashHexLen is the byte length of a hex(sha256) digest (32 bytes × 2).
	hashHexLen = sha256.Size * 2

	// maxFingerprintFieldsBytes bounds the byte cost of the per-field hash map
	// PERSISTED in the fingerprint blob. The per-field map exists ONLY to drive
	// the best-effort per-field diff on mismatch; the whole-body hash
	// (fingerprintBlob.Body) is what actually drives the match decision. A request
	// body with a pathological number of top-level fields (or an over-long field
	// name) could otherwise inflate the stored blob far beyond the body itself
	// (Redis value amplification / memory pressure across the 24h record TTL).
	// When the accumulated per-field cost would exceed this budget, fieldHashes
	// drops the map entirely (degrade to body-hash-only). Because the decision is
	// a pure function of the body, identical bodies always yield identical blobs,
	// so the degrade never causes a false mismatch — only the diagnostic diff is
	// unavailable for pathological bodies. 8 KiB comfortably covers any realistic
	// request shape (≈100+ fields) while hard-capping amplification.
	maxFingerprintFieldsBytes = 8 * 1024

	// retryAfterHintSeconds is the Retry-After header value sent on 409
	// ClaimBusy responses. A small hint (5 s) is better than the full lease
	// TTL (300 s default) because clients should retry soon; the lease may
	// expire or the in-flight request may complete well before TTL.
	retryAfterHintSeconds = 5

	// keyHashPrefixLen is the number of hex chars (bytes * 2) to include in
	// the idempotency_key_hash log attribute. 12 hex chars = 48 bits of the
	// SHA-256 digest, sufficient for correlation without exposing the raw key.
	keyHashPrefixLen = 12
)

// sensitiveResponseHeaders is a case-insensitive set of response header names
// that MUST NOT be recorded in the idempotency store. Replaying these headers
// to a different request context is dangerous:
//
//   - Set-Cookie / Set-Cookie2 — would replay a session cookie into a new
//     browser session (session fixation / stale-cookie replay attack).
//   - Authorization / WWW-Authenticate / Proxy-Authenticate — would expose
//     credentials or challenge data to a different principal.
//   - Clear-Site-Data — would incorrectly clear storage for a different session.
//
// The filter is applied at capture time (in capturedHeader / recordOrRelease),
// so RecordedResponse is a faithful container of whatever the recorder receives.
var sensitiveResponseHeaders = map[string]struct{}{
	"set-cookie":         {},
	"set-cookie2":        {},
	"authorization":      {},
	"www-authenticate":   {},
	"proxy-authenticate": {},
	"clear-site-data":    {},
}

// idempotentMethods lists the HTTP methods for which idempotency is enforced.
// GET/HEAD are naturally idempotent (no side effects) so they are excluded.
var idempotentMethods = map[string]bool{
	http.MethodPost:   true,
	http.MethodPut:    true,
	http.MethodPatch:  true,
	http.MethodDelete: true,
}

// Option is a functional option for Middleware.
type Option func(*middlewareConfig)

type middlewareConfig struct {
	maxBodyBytes  int
	leaseTTL      time.Duration
	doneTTL       time.Duration
	exemptMatcher func(*http.Request) bool
	metrics       MetricsObserver
}

func defaultConfig() middlewareConfig {
	return middlewareConfig{
		maxBodyBytes: defaultMaxBodyBytes,
		leaseTTL:     idempotency.DefaultLeaseTTL,
		doneTTL:      idempotency.DefaultTTL,
	}
}

// WithMaxBodyBytes sets the maximum number of response body bytes to buffer
// for future replay. Responses whose body exceeds this limit are served to the
// client normally but are not stored; a subsequent identical request will
// re-invoke the handler.
//
// Non-positive values are clamped to the default (256 KiB) to prevent
// accidentally disabling body buffering.
//
// Default: 256 KiB.
func WithMaxBodyBytes(n int) Option {
	return func(c *middlewareConfig) {
		if n <= 0 {
			c.maxBodyBytes = defaultMaxBodyBytes
			return
		}
		c.maxBodyBytes = n
	}
}

// WithLeaseTTL sets the in-flight processing-lease TTL.
// If a handler does not respond before the TTL expires, the lease is released
// and another request may re-claim the key.
//
// Non-positive values are clamped to the default (kernel/idempotency.DefaultLeaseTTL = 5 min).
//
// Default: kernel/idempotency.DefaultLeaseTTL (5 min).
func WithLeaseTTL(d time.Duration) Option {
	return func(c *middlewareConfig) {
		if d <= 0 {
			c.leaseTTL = idempotency.DefaultLeaseTTL
			return
		}
		c.leaseTTL = d
	}
}

// WithDoneTTL sets how long a successfully recorded response is retained for
// future replay.
//
// Non-positive values are clamped to the default (kernel/idempotency.DefaultTTL = 24 h).
//
// Default: kernel/idempotency.DefaultTTL (24 h).
func WithDoneTTL(d time.Duration) Option {
	return func(c *middlewareConfig) {
		if d <= 0 {
			c.doneTTL = idempotency.DefaultTTL
			return
		}
		c.doneTTL = d
	}
}

// WithExemptMatcher installs a predicate that opts individual routes out of
// idempotency recording. When fn(r) returns true the middleware passes the
// request directly to the next handler — no Claim is issued, no response is
// recorded, and any Idempotency-Key header sent by the client is silently
// ignored. The check runs BEFORE reading the request body so exempt routes
// pay no body-buffering cost.
//
// A nil fn is a no-op (all routes remain subject to idempotency tracking).
// Router.buildMux wires this via a lazy closure so FinalizeAuth can compile
// the exempt set after the middleware is constructed.
func WithExemptMatcher(fn func(*http.Request) bool) Option {
	return func(c *middlewareConfig) {
		c.exemptMatcher = fn
	}
}

// WithMetrics installs an optional MetricsObserver that records one
// idempotency_requests_total{cell,state} increment per terminal idempotency
// decision. Metrics are best-effort: a nil/typed-nil observer is not stored
// and emission is skipped — idempotency correctness never depends on it.
func WithMetrics(obs MetricsObserver) Option {
	return func(c *middlewareConfig) {
		if validation.IsNilInterface(obs) {
			return
		}
		c.metrics = obs
	}
}

// observeState emits one metric observation for the terminal idempotency
// decision. It is a no-op when no MetricsObserver was wired (metrics optional).
//
// The observer is supplied by the composition root and runs on the request hot
// path; a panic inside it must never change the idempotency outcome (a panic
// here on the StateAcquired branch, for example, would skip the handler and leak
// the just-acquired lease). observability.SafeObserve isolates any such panic —
// observability is best-effort by design — mirroring the body-limit / HTTP
// metrics middleware hooks.
func (c middlewareConfig) observeState(ctx context.Context, state RequestState) {
	if c.metrics == nil {
		return
	}
	observability.SafeObserve(slog.Default(), func() {
		c.metrics.ObserveRequest(ctx, state)
	})
}

// Middleware returns an HTTP middleware that enforces per-(tenant,user,key)
// idempotency for mutating methods (POST, PUT, PATCH, DELETE).
//
// The middleware reads the "Idempotency-Key" request header and the
// authenticated Principal from the request context (set by runtime/auth).
// If either is absent, or if the Principal is not a user principal, the
// request passes through without idempotency tracking.
//
// Key composition (namespace, key) is derived by DeriveKey (key.go) from
// (tenantID, subject, method, path, idemKey); see it for the exact byte layout,
// NUL-separator rationale, and node-agnostic invariant.
// Including method+path in the key means the same client-supplied header value
// is independent per endpoint — a key for POST /orders does NOT collide with
// POST /payments. (Stripe / IETF idempotency-key draft §3 aligned.)
//
// Body fingerprinting: the request body is read and reduced to a canonical
// fingerprint blob (whole-body hash + per-field hashes; see computeFingerprint).
// BodyLimit middleware MUST run before this middleware so r.Body is already
// size-bounded. Fingerprint mismatch (same key, different body) returns 422
// (KindUnprocessable) ErrIdempotencyKeyReused, with the differing top-level field
// names in the error details (per-field diff).
//
// clk must be non-nil; clock.MustHaveClock panics on nil.
// store must be non-nil; a nil store causes a panic with panicregister.Approved
// (B-class programmer-error; callers must supply a valid store at wiring time).
func Middleware(clk clock.Clock, store Store, opts ...Option) func(http.Handler) http.Handler {
	clock.MustHaveClock(clk, "idempotency.Middleware")
	if validation.IsNilInterface(store) {
		panic(panicregister.Approved("idempotency-middleware-store-nil",
			errcode.Assertion("Middleware: store is required; pass a non-nil Store implementation")))
	}

	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}

	return func(next http.Handler) http.Handler {
		return idempotencyHandler{
			next:   next,
			clk:    clk,
			store:  store,
			config: cfg,
		}
	}
}

type idempotencyHandler struct {
	next   http.Handler
	clk    clock.Clock
	store  Store
	config middlewareConfig
}

// validateIdempotencyKey validates the key length and character set, writing
// the appropriate error response and returning false if invalid.
func validateIdempotencyKey(ctx context.Context, w http.ResponseWriter, idemKey string) bool {
	if len(idemKey) > maxIdempotencyKeyLen {
		httputil.WriteError(ctx, w, errcode.New(
			errcode.KindInvalid,
			errcode.ErrValidationFailed,
			msgKeyTooLong,
			errcode.WithDetails(errcode.PublicInt("maxLen", maxIdempotencyKeyLen)),
		))
		return false
	}
	if !isValidIdempotencyKey(idemKey) {
		httputil.WriteError(ctx, w, errcode.New(
			errcode.KindInvalid,
			errcode.ErrValidationFailed,
			msgKeyInvalidChars,
		))
		return false
	}
	return true
}

// readBodyFingerprint reads the request body, restores it for the handler,
// and returns the hex(sha256(body)) fingerprint. Returns ("", false) on error.
func readBodyFingerprint(ctx context.Context, w http.ResponseWriter, r *http.Request) (string, bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httputil.WriteError(ctx, w, errcode.New(
			errcode.KindInternal,
			errcode.ErrInternal,
			msgStoreUnavailable,
		))
		return "", false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return computeFingerprint(body), true
}

// ServeHTTP executes the per-request idempotency decision for a
// single request, reducing the cognitive complexity of the closure returned by
// Middleware. It checks exempt status first (before body read), then the method
// gate, then principal identity, key validation, and finally the full claim flow.
func (h idempotencyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Exempt check runs first — before method gate and before body read,
	// so exempt routes pay zero body-buffering cost.
	cfg := h.config
	if cfg.exemptMatcher != nil && cfg.exemptMatcher(r) {
		h.next.ServeHTTP(w, r)
		return
	}
	if !shouldIntercept(r) {
		h.next.ServeHTTP(w, r)
		return
	}
	p, ok := extractIdentity(r.Context())
	if !ok {
		h.next.ServeHTTP(w, r)
		return
	}
	idemKey := r.Header.Get(headerIdempotencyKey)
	if !validateIdempotencyKey(r.Context(), w, idemKey) {
		return
	}
	fp, ok := readBodyFingerprint(r.Context(), w, r)
	if !ok {
		return
	}
	h.handle(w, r, p, idemKey, fp)
}

// shouldIntercept returns true when the request method and Idempotency-Key
// header both qualify for idempotency enforcement.
func shouldIntercept(r *http.Request) bool {
	return idempotentMethods[r.Method] && r.Header.Get(headerIdempotencyKey) != ""
}

// handleWithIdempotency executes the full idempotency flow for a qualifying request.
func (h idempotencyHandler) handle(w http.ResponseWriter, r *http.Request, p *auth.Principal, idemKey string, fp string) {
	cfg := h.config
	k := DeriveKey(p.TenantID, p.Subject, r.Method, r.URL.Path, idemKey)
	ns := k.Namespace() // for slog correlation + recordOrRelease below
	ctx := r.Context()
	keyHash := keyShortHash(idemKey)

	state, rec, receipt, err := h.store.Claim(ctx, k, fp, cfg.leaseTTL)
	if err != nil {
		if errors.Is(err, ErrFingerprintMismatch) {
			slog.WarnContext(
				ctx, "idempotency: fingerprint mismatch — key reused with different body",
				"idempotency_key_hash", keyHash,
				"subject", p.Subject,
				"tenant_id", ns,
			)
			cfg.observeState(ctx, StateKeyReused)
			httputil.WriteError(ctx, w, keyReusedError(err, fp))
			return
		}
		slog.ErrorContext(
			ctx, "idempotency: store claim failed",
			"err", err,
			"idempotency_key_hash", keyHash,
			"subject", p.Subject,
			"tenant_id", ns,
		)
		cfg.observeState(ctx, StateStoreError)
		httputil.WriteError(ctx, w, errcode.New(
			errcode.KindInternal,
			errcode.ErrInternal,
			msgStoreUnavailable,
		))
		return
	}

	switch state {
	case idempotency.ClaimDone:
		// Replay returns this principal's own previously-recorded response WITHOUT
		// re-running the route Policy. This is by-design and not an authz bypass:
		// the cache key includes subject+tenant (cross-principal replay is
		// structurally impossible — see DeriveKey), and the recorded
		// response is from an operation this same principal already performed while
		// authorized. Re-checking authz on replay would let a previously-succeeded
		// key later return 403, violating Idempotency-Key semantics (same key →
		// same response). See ADR 202606021000-1043 威胁矩阵 row "回放跳过当前授权再校验".
		slog.DebugContext(
			ctx, "idempotency: replay hit",
			"idempotency_key_hash", keyHash,
			"subject", p.Subject,
			"tenant_id", ns,
		)
		cfg.observeState(ctx, StateReplayed)
		replayResponse(w, rec)

	case idempotency.ClaimBusy:
		slog.WarnContext(
			ctx, "idempotency: key in progress",
			"idempotency_key_hash", keyHash,
			"subject", p.Subject,
			"tenant_id", ns,
		)
		cfg.observeState(ctx, StateBusy)
		// Use a small fixed hint (retryAfterHintSeconds) rather than the full
		// leaseTTL (300 s default) so clients retry soon without a long wait.
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterHintSeconds))
		httputil.WriteError(ctx, w, errInProgress)

	default: // ClaimAcquired
		cfg.observeState(ctx, StateAcquired)
		h.recordOrRelease(w, r, receipt, keyHash, ns, p.Subject)
	}
}

// extractIdentity checks that a PrincipalUser with a non-empty Subject is
// present. Service and anonymous principals bypass idempotency.
func extractIdentity(ctx context.Context) (*auth.Principal, bool) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return nil, false
	}
	if p.Kind != auth.PrincipalUser {
		return nil, false
	}
	if p.Subject == "" {
		return nil, false
	}
	return p, true
}

// replayResponse writes the stored RecordedResponse to w with the
// Idempotency-Replayed marker.
func replayResponse(w http.ResponseWriter, rec *RecordedResponse) {
	for k, vv := range rec.Header() {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set(headerIdempotencyReplayed, "true")
	w.WriteHeader(rec.Status())
	writeRecordedBody(w, rec.Body())
}

func writeRecordedBody(w http.ResponseWriter, body []byte) {
	if _, err := io.Copy(w, bytes.NewReader(body)); err != nil {
		slog.Error("idempotency: replay response body write failed", slog.Any("error", err))
	}
}

// recordOrRelease wraps the handler invocation: runs next inside a
// bufferingWriter, then either records the response (on success) or
// releases the lease (on failure or oversized body) so the key can be
// re-tried.
//
// A defer ensures Release is called even if the handler panics, so the lease is
// not held indefinitely after a panic. The panic propagates naturally after
// Release.
func (h idempotencyHandler) recordOrRelease(
	w http.ResponseWriter,
	r *http.Request,
	receipt Receipt,
	keyHash string,
	namespace string,
	subject string,
) {
	ctx := r.Context()
	cfg := h.config
	bw := newBufferingWriter(w, cfg.maxBodyBytes)

	recorded := false
	defer func() {
		if !recorded {
			// Use context.WithoutCancel so the Release reaches the store even
			// if the request context was canceled during handler execution.
			// The lease will expire via TTL regardless; log at Warn on error.
			if err := receipt.Release(context.WithoutCancel(ctx)); err != nil {
				slog.WarnContext(
					ctx, "idempotency: lease release failed (will expire via TTL)",
					"err", err,
					"idempotency_key_hash", keyHash,
					"subject", subject,
					"tenant_id", namespace,
				)
			}
		}
	}()

	h.next.ServeHTTP(bw, r)

	// Only record if the handler committed a successful (2xx/3xx) response
	// and the body did not overflow the capture limit.
	if bw.committed() && shouldRecord(bw.status()) && !bw.isOversized() {
		filteredHeader := filterSensitiveHeaders(bw.capturedHeader())
		resp := newRecordedResponse(h.clk, bw.status(), bw.bufferedBody(), filteredHeader)
		if err := receipt.Record(context.WithoutCancel(ctx), &resp, cfg.doneTTL); err != nil {
			slog.ErrorContext(
				ctx, "idempotency: receipt record failed",
				"err", err,
				"idempotency_key_hash", keyHash,
				"subject", subject,
				"tenant_id", namespace,
			)
			// Fall through to Release via defer.
		} else {
			recorded = true
		}
	} else if bw.committed() && bw.isOversized() {
		slog.WarnContext(
			ctx, "idempotency: response body oversized, not recorded",
			"max_body_bytes", cfg.maxBodyBytes,
			"idempotency_key_hash", keyHash,
			"subject", subject,
			"tenant_id", namespace,
		)
		cfg.observeState(ctx, StateOversize)
	}
}

// filterSensitiveHeaders returns a clone of h with sensitiveResponseHeaders
// removed. The filter runs at capture/record time so RecordedResponse only
// ever stores safe-to-replay headers.
//
// http.Header.Del uses net/textproto.CanonicalMIMEHeaderKey internally, so
// passing lowercase names correctly deletes the canonically-cased entry.
func filterSensitiveHeaders(h http.Header) http.Header {
	filtered := h.Clone()
	for name := range sensitiveResponseHeaders {
		filtered.Del(name) // Del is case-insensitive via CanonicalMIMEHeaderKey
	}
	return filtered
}

// shouldRecord returns true for 2xx and 3xx status codes. 4xx/5xx responses
// are not idempotency-safe to replay (e.g., validation errors that the client
// can fix).
func shouldRecord(status int) bool {
	return status >= 200 && status < 400
}

// fingerprintBlob is the canonical fingerprint persisted by the Store as an
// opaque string. Body is hex(sha256(rawBody)) — the match decision is exact
// equality of the whole blob, so Body preserves byte-exact match semantics
// (reordered keys or whitespace change Body and therefore mismatch). Fields maps
// each top-level JSON field name to hex(sha256(rawValueBytes)) and exists ONLY
// to drive the per-field diff on mismatch; it holds hashes, never raw values.
type fingerprintBlob struct {
	Body   string            `json:"b"`
	Fields map[string]string `json:"f,omitempty"`
}

// computeFingerprint returns the canonical fingerprint blob (JSON-encoded) for a
// request body. json.Marshal sorts map keys, so the encoding is deterministic.
// For a non-JSON-object body, Fields is nil (no per-field diff is available) and
// only Body participates.
func computeFingerprint(body []byte) string {
	blob := fingerprintBlob{Body: hashHex(body), Fields: fieldHashes(body)}
	encoded, err := json.Marshal(blob)
	if err != nil {
		// Unreachable for string + map[string]string, but fall back to the bare
		// body hash so Claim always has a stable, non-empty key.
		return hashHex(body)
	}
	return string(encoded)
}

// parseFingerprint decodes a fingerprint blob produced by computeFingerprint. A
// parse failure degrades to an empty blob, which yields an empty per-field diff
// (the base 422 is still returned) rather than an error.
func parseFingerprint(s string) fingerprintBlob {
	var blob fingerprintBlob
	if err := json.Unmarshal([]byte(s), &blob); err != nil {
		return fingerprintBlob{}
	}
	return blob
}

// fieldHashes returns {topLevelField: hex(sha256(rawValueBytes))} for a JSON
// object body, or nil when body is not a JSON object. Only top-level fields are
// hashed (Stripe-param granularity); a changed nested value surfaces as a change
// to its top-level parent. Each value is hashed by its raw bytes (json.RawMessage
// defers value parsing — no recursive decode/re-encode), which keeps the per-field
// diff consistent with the byte-exact whole-body match (Body) and avoids any
// hot-path cost or deep-nesting surface from canonicalizing untrusted values.
//
// The accumulated per-field cost (field name + hash digest, both client-influenced)
// is bounded by maxFingerprintFieldsBytes. If a pathological body would push the
// map past the budget, the whole map is dropped (returns nil → body-hash-only),
// keeping the PERSISTED fingerprint bounded regardless of body size. The cut is a
// pure function of the body, so identical bodies still produce identical maps and
// the degrade can never cause a false mismatch — only the diagnostic per-field
// diff is unavailable for such bodies.
func fieldHashes(body []byte) map[string]string {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil // not a JSON object — no per-field diff available
	}
	out := make(map[string]string, len(top))
	size := 0
	for k, raw := range top {
		size += len(k) + hashHexLen
		if size > maxFingerprintFieldsBytes {
			return nil // oversized field set — degrade to body-hash-only
		}
		out[k] = hashHex(raw)
	}
	return out
}

// diffFields returns the sorted top-level field names whose hash differs between
// the stored and incoming fingerprint blobs (changed, added, or removed). It
// returns names only — never values, which are not present in either blob.
func diffFields(storedBlob, incomingBlob string) []string {
	stored := parseFingerprint(storedBlob).Fields
	incoming := parseFingerprint(incomingBlob).Fields
	seen := make(map[string]struct{}, len(incoming))
	var diff []string
	for k, h := range incoming {
		seen[k] = struct{}{}
		if stored[k] != h {
			diff = append(diff, k)
		}
	}
	for k := range stored {
		if _, ok := seen[k]; !ok {
			diff = append(diff, k)
		}
	}
	sort.Strings(diff)
	return diff
}

// keyReusedError builds the 422 response for a fingerprint mismatch, enriched
// with the names of the top-level request fields that differ (per-field diff).
// err is the *FingerprintMismatchError from Store.Claim; incoming is the current
// request's fingerprint blob.
func keyReusedError(err error, incoming string) *errcode.Error {
	return errcode.New(errcode.KindUnprocessable, errcode.ErrIdempotencyKeyReused,
		msgKeyReused, keyReusedDetailOpts(err, incoming)...)
}

// keyReusedDetailOpts derives the per-field diff details. It returns nil (no
// details) when the stored blob is unavailable or the diff is empty (e.g. body
// differs only by key order / whitespace, or a non-JSON-object body).
func keyReusedDetailOpts(err error, incoming string) []errcode.Option {
	var fpErr *FingerprintMismatchError
	if !errors.As(err, &fpErr) {
		return nil
	}
	fields := diffFields(fpErr.Stored, incoming)
	if len(fields) == 0 {
		return nil
	}
	truncated := false
	if len(fields) > maxMismatchedFields {
		fields = fields[:maxMismatchedFields]
		truncated = true
	}
	details := make([]errcode.PublicDetail, 0, len(fields)+1)
	for _, f := range fields {
		details = append(details, errcode.PublicString("mismatchedField", truncateFieldName(f)))
	}
	if truncated {
		details = append(details, errcode.PublicBool("mismatchedFieldsTruncated", true))
	}
	return []errcode.Option{errcode.WithDetails(details...)}
}

// truncateFieldName bounds a client-controlled JSON field name to maxFieldNameLen
// bytes (appending "…" when truncated) so an over-long key cannot bloat the 422
// response. Truncates on a rune boundary to keep the output valid UTF-8.
func truncateFieldName(name string) string {
	if len(name) <= maxFieldNameLen {
		return name
	}
	cut := maxFieldNameLen
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return name[:cut] + "…"
}

// hashHex returns hex(sha256(b)).
func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// keyShortHash returns the first keyHashPrefixLen hex chars of sha256(key).
// Used in structured log attributes instead of the raw key value to avoid
// leaking client-supplied opaque tokens into logs.
func keyShortHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:keyHashPrefixLen]
}

// isValidIdempotencyKey returns true if the key contains only printable ASCII
// characters and does not contain '{' or '}' (Redis hashtag chars that would
// confuse the cluster routing logic in the Redis adapter).
func isValidIdempotencyKey(key string) bool {
	for _, c := range key {
		if c == '{' || c == '}' {
			return false
		}
		if c > unicode.MaxASCII || !unicode.IsPrint(c) {
			return false
		}
	}
	return true
}
