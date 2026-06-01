package idempotency

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
)

const (
	// noTenantSentinel is the namespace substituted when the authenticated
	// principal carries no TenantID. Single-tenant deployments and contexts
	// where tenancy is not yet wired produce an empty TenantID; using a
	// named sentinel keeps the namespace non-empty and distinguishable.
	noTenantSentinel = "_notenant"

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
	maxBodyBytes int
	leaseTTL     time.Duration
	doneTTL      time.Duration
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

// Middleware returns an HTTP middleware that enforces per-(tenant,user,key)
// idempotency for mutating methods (POST, PUT, PATCH, DELETE).
//
// The middleware reads the "Idempotency-Key" request header and the
// authenticated Principal from the request context (set by runtime/auth).
// If either is absent, or if the Principal is not a user principal, the
// request passes through without idempotency tracking.
//
// Namespace composition: ns = tenantID (or "_notenant" when empty),
// key = subject + "\x00" + Idempotency-Key header value.
// The NUL separator (\x00) prevents collision when one principal's Subject
// is a prefix of another combined with the key (e.g. subject="alice",
// key="x" vs subject="alic", key="e:x").
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
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !shouldIntercept(r) {
				next.ServeHTTP(w, r)
				return
			}
			p, ok := extractIdentity(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			// Validate Idempotency-Key length before doing any store I/O.
			idemKey := r.Header.Get(headerIdempotencyKey)
			if len(idemKey) > maxIdempotencyKeyLen {
				httputil.WriteError(r.Context(), w, errcode.New(
					errcode.KindInvalid,
					errcode.ErrValidationFailed,
					msgKeyTooLong,
					errcode.WithDetails(errcode.PublicInt("maxLen", maxIdempotencyKeyLen)),
				))
				return
			}
			handleWithIdempotency(w, r, next, p, idemKey, clk, store, cfg)
		})
	}
}

// shouldIntercept returns true when the request method and Idempotency-Key
// header both qualify for idempotency enforcement.
func shouldIntercept(r *http.Request) bool {
	return idempotentMethods[r.Method] && r.Header.Get(headerIdempotencyKey) != ""
}

// handleWithIdempotency executes the full idempotency flow for a qualifying request.
func handleWithIdempotency(
	w http.ResponseWriter,
	r *http.Request,
	next http.Handler,
	p *auth.Principal,
	idemKey string,
	clk clock.Clock,
	store Store,
	cfg middlewareConfig,
) {
	ns, key := buildNamespaceKey(p, idemKey)
	ctx := r.Context()

	state, rec, receipt, err := store.Claim(ctx, ns, key, cfg.leaseTTL)
	if err != nil {
		slog.ErrorContext(ctx, "idempotency: store claim failed",
			"err", err,
			"idempotency_key", idemKey,
			"subject", p.Subject,
			"tenant_id", ns,
		)
		httputil.WriteError(ctx, w, errcode.New(
			errcode.KindInternal,
			errcode.ErrInternal,
			msgStoreUnavailable,
		))
		return
	}

	switch state {
	case idempotency.ClaimDone:
		slog.DebugContext(ctx, "idempotency: replay hit",
			"idempotency_key", idemKey,
			"subject", p.Subject,
			"tenant_id", ns,
		)
		replayResponse(w, rec)

	case idempotency.ClaimBusy:
		slog.WarnContext(ctx, "idempotency: key in progress",
			"idempotency_key", idemKey,
			"subject", p.Subject,
			"tenant_id", ns,
		)
		w.Header().Set("Retry-After", strconv.Itoa(int(cfg.leaseTTL.Seconds())))
		httputil.WriteError(ctx, w, errcode.New(
			errcode.KindConflict,
			errcode.ErrIdempotencyInProgress,
			msgInProgress,
		))

	default: // ClaimAcquired
		recordOrRelease(ctx, w, r, next, clk, receipt, cfg, idemKey, ns)
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

// buildNamespaceKey encodes the isolation tuple (tenantID, userID, idemKey)
// into the (ns, key) pair expected by Store.Claim.
//
// ns  = tenantID, or noTenantSentinel when empty.
// key = subject + "\x00" + idemKey
//
// The NUL byte (\x00) separator prevents key-space collision: it cannot appear
// in HTTP header values (RFC 7230 §3.2.6 limits field-value to VCHAR and obs-text,
// neither of which includes NUL), so subject="alic",key="e:x" is always distinct
// from subject="alice",key="x". A colon separator (:) would collide on those inputs.
//
// Using tenantID as the namespace means the Redis key for a cluster-aware
// adapter would be "{<tenantID>}:<subject>\x00<idemKey>", which colocates all
// keys for the same tenant on the same hash slot — good for single-slot
// transactions.
func buildNamespaceKey(p *auth.Principal, idemKey string) (ns, key string) {
	ns = p.TenantID
	if ns == "" {
		ns = noTenantSentinel
	}
	key = p.Subject + "\x00" + idemKey
	return
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
	_, _ = w.Write(rec.Body()) //nolint:gosec // G705: Body() returns a cloned []byte from a trusted store; no user-controlled taint path
}

// recordOrRelease wraps the handler invocation: runs next inside a
// bufferingWriter, then either records the response (on success) or
// releases the lease (on failure or oversized body) so the key can be
// re-tried.
//
// A defer ensures Release is called even if the handler panics, so the
// lease is not held indefinitely after a panic. The panic propagates
// naturally after Release.
func recordOrRelease(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	next http.Handler,
	clk clock.Clock,
	receipt Receipt,
	cfg middlewareConfig,
	idemKey string,
	ns string,
) {
	bw := newBufferingWriter(w, cfg.maxBodyBytes)

	recorded := false
	defer func() {
		if !recorded {
			// Use context.WithoutCancel so the Release reaches the store even
			// if the request context was canceled during handler execution.
			_ = receipt.Release(context.WithoutCancel(ctx))
		}
	}()

	next.ServeHTTP(bw, r)

	// Only record if the handler committed a successful (2xx/3xx) response
	// and the body did not overflow the capture limit.
	if bw.committed() && shouldRecord(bw.status()) && !bw.isOversized() {
		filteredHeader := filterSensitiveHeaders(bw.capturedHeader())
		resp := newRecordedResponse(clk, bw.status(), bw.bufferedBody(), filteredHeader)
		if err := receipt.Record(context.WithoutCancel(ctx), &resp, cfg.doneTTL); err != nil {
			slog.ErrorContext(ctx, "idempotency: receipt record failed",
				"err", err,
				"idempotency_key", idemKey,
				"tenant_id", ns,
			)
			// Fall through to Release via defer.
		} else {
			recorded = true
		}
	} else if bw.committed() && bw.isOversized() {
		slog.WarnContext(ctx, "idempotency: response body oversized, not recorded",
			"max_body_bytes", cfg.maxBodyBytes,
			"idempotency_key", idemKey,
			"tenant_id", ns,
		)
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
