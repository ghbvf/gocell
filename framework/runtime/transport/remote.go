package transport

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/wrapper"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// Constructor fail-fast messages — MESSAGE-CONST-LITERAL-01.
const (
	msgRemoteNilResolver     = "remote transport: resolver must not be nil"
	msgRemoteNilClient       = "remote transport: http.Client must not be nil"
	msgRemoteEmptyTargetCell = "remote transport: targetCellID must not be empty"
)

// DoContract error messages — MESSAGE-CONST-LITERAL-01.
const (
	msgRemoteDialFailed     = "remote transport: upstream cell unreachable"
	msgRemoteURLRewriteFail = "remote transport: failed to rewrite request URL to remote endpoint"
	msgRemoteZeroValue      = "remote transport: DoContract called on a zero-value RemoteHTTPTransport"
)

// RemoteHTTPTransport is the remote (network) implementation of [CellTransport].
// It resolves the target cell's endpoint via [Resolver.Resolve], rewrites the
// request URL to that endpoint, and dispatches the request via an *http.Client.
// The caller is responsible for signing the request before calling DoContract
// (signing stays caller-side, transport carries the already-signed request).
//
// Sealed type: all fields are unexported. The only constructor is [NewRemoteHTTP].
// A zero-value RemoteHTTPTransport has a nil resolver and nil client; its
// DoContract fails fast with KindInternal rather than panicking. Field set frozen
// by REMOTE-TRANSPORT-SEALED-01.
//
// Observability (ADR D4): DoContract opens a trace span with transport_mode=remote
// and contract.id; on success records cell_transport_requests_total{transport_mode=remote}.
// A 5xx response marks the span StatusError but is returned as (resp, nil) — the
// caller decides retry/circuit-break semantics. A dial failure (no response) is
// returned as (nil, KindUnavailable).
type RemoteHTTPTransport struct {
	resolver     Resolver
	targetCellID string
	client       *http.Client
	metrics      *Metrics
	tracer       wrapper.Tracer
	clk          clock.Clock
}

// NewRemoteHTTP constructs a RemoteHTTPTransport. All mandatory parameters are
// enforced at construction time via fail-fast guards (registered panics per
// error-handling.md §Panic):
//
//   - clk nil → clock.MustHaveClock (panicregister.Approved)
//   - targetCellID empty → panicregister.Approved("remote-transport-empty-target-cell-id")
//   - resolver nil → panicregister.Approved("remote-transport-nil-resolver")
//   - client nil → panicregister.Approved("remote-transport-nil-client")
//
// metrics and tracer are optional: nil metrics records nothing; nil tracer
// degrades to wrapper.NoopTracer{}.
func NewRemoteHTTP(
	clk clock.Clock,
	targetCellID string,
	resolver Resolver,
	client *http.Client,
	metrics *Metrics,
	tracer wrapper.Tracer,
) *RemoteHTTPTransport {
	clock.MustHaveClock(clk, "transport.NewRemoteHTTP")

	if strings.TrimSpace(targetCellID) == "" {
		panic(panicregister.Approved(
			"remote-transport-empty-target-cell-id",
			errcode.Assertion(msgRemoteEmptyTargetCell),
		))
	}
	if validation.IsNilInterface(resolver) {
		panic(panicregister.Approved(
			"remote-transport-nil-resolver",
			errcode.Assertion(msgRemoteNilResolver),
		))
	}
	if client == nil {
		panic(panicregister.Approved(
			"remote-transport-nil-client",
			errcode.Assertion(msgRemoteNilClient),
		))
	}
	if tracer == nil {
		tracer = wrapper.NoopTracer{}
	}
	return &RemoteHTTPTransport{
		resolver:     resolver,
		targetCellID: targetCellID,
		client:       client,
		metrics:      metrics,
		tracer:       tracer,
		clk:          clk,
	}
}

// DoContract resolves the target cell endpoint, rewrites the request URL to
// that endpoint, and dispatches the request. Span and metric are recorded per
// ADR D4.
//
// Fail-fast: a zero-value (un-minted) RemoteHTTPTransport returns KindInternal
// rather than panicking on a nil resolver or client dereference.
//
// Success path: returns (resp, nil) for ALL HTTP responses, including 5xx. The
// caller decides how to handle error status codes; DoContract's error channel
// is reserved for transport-level failures (dial error, ctx cancellation,
// resolver error, URL rewrite failure).
//
// Failure path:
//   - resolver error → returned directly (resolver sets the Kind).
//   - URL rewrite failure (malformed endpoint) → KindInternal.
//   - dial failure (no response from server) → KindUnavailable /
//     ErrUpstreamCellUnavailable (transient Net classification determines reason
//     text, not the error Kind).
func (t *RemoteHTTPTransport) DoContract(ctx context.Context, contractID string, req *http.Request) (*http.Response, error) {
	// Zero-value guard: resolver and client are nil only for a zero-value struct.
	if validation.IsNilInterface(t.resolver) || t.client == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrInternal, msgRemoteZeroValue,
			errcode.WithInternal(errcode.InternalAttr("contractID", contractID)))
	}

	ctx, span := t.tracer.Start(ctx, spanName,
		wrapper.Attr{Key: labelTransportMode, Value: modeRemote.String()},
		wrapper.Attr{Key: "contract.id", Value: contractID})
	defer span.End()

	endpoint, err := t.resolver.Resolve(ctx, t.targetCellID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(wrapper.StatusError, "resolver error")
		return nil, err
	}

	if err := rewriteToAbsolute(req, endpoint); err != nil {
		span.RecordError(err)
		span.SetStatus(wrapper.StatusError, "URL rewrite failed")
		return nil, err
	}

	// The request host was rewritten from the resolver's endpoint, which derives
	// from the operator-configured (sealed) deployment topology — not user input.
	// This is the intended cross-cell dial, not an SSRF sink.
	resp, err := t.client.Do(req.WithContext(ctx)) //nolint:gosec // G704: see rationale above (operator-configured endpoint)
	if err != nil {
		dialErr := wrapDialError(contractID, t.targetCellID, err)
		span.RecordError(dialErr)
		span.SetStatus(wrapper.StatusError, "dial failed")
		return nil, dialErr
	}

	// Success: record metric and set span attributes. 5xx marks span StatusError
	// but is returned as (resp, nil) — caller decides retry/circuit-break.
	t.metrics.Record(ctx, modeRemote)
	span.SetAttributes(wrapper.Attr{Key: attrHTTPStatusCode, Value: int64(resp.StatusCode)})
	if resp.StatusCode >= http.StatusInternalServerError {
		span.SetStatus(wrapper.StatusError, http.StatusText(resp.StatusCode))
	}

	return resp, nil
}

// rewriteToAbsolute rewrites req.URL in-place so that Scheme and Host point
// at the declared remote endpoint, preserving the original path, query, and
// fragment. The endpoint may be:
//
//   - A bare "host:port" → Scheme is set to "http".
//   - An "http(s)://host[:port]" URL → Scheme and Host are taken from the
//     parsed URL.
//
// The request is mutated in-place so that headers (incl. already-signed
// Authorization) are preserved — no new *http.Request is created.
//
// Returns KindInternal on an unparseable endpoint (a static wiring error, not
// a transient network condition).
func rewriteToAbsolute(req *http.Request, endpoint string) error {
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		u, err := url.Parse(endpoint)
		if err != nil {
			return errcode.New(errcode.KindInternal, errcode.ErrInternal, msgRemoteURLRewriteFail,
				errcode.WithInternal(errcode.InternalAttr("endpoint", endpoint)))
		}
		req.URL.Scheme = u.Scheme
		req.URL.Host = u.Host
		return nil
	}
	// Bare host:port — default to http (TLS enforcement is US6 #1964).
	// Security note: postgres topology bare host:port walks over plaintext HTTP,
	// so bearer/principal headers are confidential only within a private network.
	// MAC (X-Gocell-ServiceToken) ensures integrity but NOT confidentiality;
	// mTLS confidentiality is wired by US6 #1964.
	req.URL.Scheme = "http"
	req.URL.Host = endpoint
	return nil
}

// wrapDialError wraps a client.Do transport-level error (no HTTP response
// received) into a KindUnavailable errcode. IsTransientNet determines whether
// the error is transient for log-level annotation (it does NOT change the Kind
// — all dial failures are KindUnavailable from the caller's perspective since
// no response body is available).
func wrapDialError(contractID, cellID string, err error) error {
	reason := "connection failed"
	if errcode.IsTransientNet(err) {
		reason = "transient network error"
	}
	return errcode.New(errcode.KindUnavailable, errcode.ErrUpstreamCellUnavailable,
		msgRemoteDialFailed,
		errcode.WithInternal(
			errcode.InternalAttr("reason", reason),
			errcode.InternalAttr("cellID", cellID),
			errcode.InternalAttr("contractID", contractID),
			errcode.InternalAttr("cause", err.Error()),
		))
}

// compile-time: RemoteHTTPTransport satisfies the seam.
var _ CellTransport = (*RemoteHTTPTransport)(nil)
