package idempotency

import (
	"context"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
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

	// msgStoreUnavailable is the client-visible message for store errors.
	// Must be a const literal (MESSAGE-CONST-LITERAL-01 archtest).
	msgStoreUnavailable = "idempotency store unavailable"

	// msgInProgress is the client-visible message for concurrent in-flight keys.
	// Must be a const literal (MESSAGE-CONST-LITERAL-01 archtest).
	msgInProgress = "a request with this Idempotency-Key is already in progress"
)

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
// Default: 256 KiB.
func WithMaxBodyBytes(n int) Option {
	return func(c *middlewareConfig) {
		c.maxBodyBytes = n
	}
}

// WithLeaseTTL sets the in-flight processing-lease TTL.
// If a handler does not respond before the TTL expires, the lease is released
// and another request may re-claim the key.
//
// Default: kernel/idempotency.DefaultLeaseTTL (5 min).
func WithLeaseTTL(d time.Duration) Option {
	return func(c *middlewareConfig) {
		c.leaseTTL = d
	}
}

// WithDoneTTL sets how long a successfully recorded response is retained for
// future replay.
//
// Default: kernel/idempotency.DefaultTTL (24 h).
func WithDoneTTL(d time.Duration) Option {
	return func(c *middlewareConfig) {
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
// key = subject + ":" + Idempotency-Key header value.
//
// clk must be non-nil; clock.MustHaveClock panics on nil.
// store must be non-nil; a nil store causes a panic with panicregister.Approved.
func Middleware(clk clock.Clock, store Store, opts ...Option) func(http.Handler) http.Handler {
	clock.MustHaveClock(clk, "idempotency.Middleware")

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
			handleWithIdempotency(w, r, next, p, clk, store, cfg)
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
	clk clock.Clock,
	store Store,
	cfg middlewareConfig,
) {
	idemKey := r.Header.Get(headerIdempotencyKey)
	ns, key := buildNamespaceKey(p, idemKey)
	ctx := r.Context()

	state, rec, receipt, err := store.Claim(ctx, ns, key, cfg.leaseTTL)
	if err != nil {
		httputil.WriteError(ctx, w, errcode.New(
			errcode.KindInternal,
			errcode.ErrInternal,
			msgStoreUnavailable,
		))
		return
	}

	switch state {
	case idempotency.ClaimDone:
		replayResponse(w, rec)

	case idempotency.ClaimBusy:
		w.Header().Set("Retry-After", "1")
		httputil.WriteError(ctx, w, errcode.New(
			errcode.KindConflict,
			errcode.ErrIdempotencyInProgress,
			msgInProgress,
		))

	default: // ClaimAcquired
		recordOrRelease(ctx, w, r, next, clk, receipt, cfg)
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
// key = subject + ":" + idemKey
//
// Using tenantID as the namespace means the Redis key for a cluster-aware
// adapter would be "{<tenantID>}:<subject>:<idemKey>", which colocates all
// keys for the same tenant on the same hash slot — good for single-slot
// transactions.
func buildNamespaceKey(p *auth.Principal, idemKey string) (ns, key string) {
	ns = p.TenantID
	if ns == "" {
		ns = noTenantSentinel
	}
	key = p.Subject + ":" + idemKey
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
		resp := newRecordedResponse(clk, bw.status(), bw.bufferedBody(), bw.capturedHeader())
		if err := receipt.Record(context.WithoutCancel(ctx), &resp, cfg.doneTTL); err == nil {
			recorded = true
		}
		// On Record error we fall through to Release via defer.
	}
}

// shouldRecord returns true for 2xx and 3xx status codes. 4xx/5xx responses
// are not idempotency-safe to replay (e.g., validation errors that the client
// can fix).
func shouldRecord(status int) bool {
	return status >= 200 && status < 400
}
