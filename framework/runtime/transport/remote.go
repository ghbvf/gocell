package transport

import (
	"context"
	"errors"
	"net"
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
// and contract.id; EVERY dispatch records cell_transport_requests_total{transport_mode=remote,
// outcome=…} — success and every failure exit (#1966 review P2.6), so the failure
// rate is not under-reported. A 5xx response marks the span StatusError but is
// returned as (resp, nil) with outcome=success — the caller decides retry/circuit-break
// semantics. A dial failure (no response) is returned as (nil, err) with the Kind
// classified per the cause (canceled→499 / timeout→504 / other→503; #1966 review P2.8).
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
// metrics and tracer are optional: nil metrics records nothing; a nil or
// typed-nil tracer degrades to wrapper.NoopTracer{} (via validation.IsNilInterface,
// so a non-nil interface wrapping a nil pointer never reaches t.tracer.Start).
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
	if validation.IsNilInterface(tracer) {
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
// Failure path (every exit records cell_transport_requests_total{outcome=…}):
//   - resolver error → returned directly (resolver sets the Kind); outcome=resolver_error.
//   - URL rewrite failure (malformed endpoint) → KindInternal; outcome=rewrite_error.
//   - dial failure (no response from server) → ErrUpstreamCellUnavailable, with the
//     Kind classified (#1966 review P2.8): caller-ctx canceled → KindClientClosed
//     (499); deadline-exceeded / net timeout → KindDeadlineExceeded (504); other
//     dial errors (refused / reset / DNS) → KindUnavailable (503). outcome is
//     canceled / timeout / dial_error respectively.
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
		t.metrics.Record(ctx, modeRemote, outcomeResolverError)
		span.RecordError(err)
		span.SetStatus(wrapper.StatusError, "resolver error")
		return nil, err
	}

	if err := rewriteToAbsolute(req, endpoint); err != nil {
		t.metrics.Record(ctx, modeRemote, outcomeRewriteError)
		span.RecordError(err)
		span.SetStatus(wrapper.StatusError, "URL rewrite failed")
		return nil, err
	}

	// The request host was rewritten from the resolver's endpoint, which derives
	// from the operator-configured (sealed) deployment topology — not user input.
	// This is the intended cross-cell dial, not an SSRF sink.
	resp, err := t.client.Do(req.WithContext(ctx)) //nolint:gosec // G107: sealed-topology endpoint, not user input (see above)
	if err != nil {
		dialErr := classifyDialError(contractID, t.targetCellID, err)
		t.metrics.Record(ctx, modeRemote, dialOutcome(err))
		span.RecordError(dialErr)
		span.SetStatus(wrapper.StatusError, "dial failed")
		return nil, dialErr
	}

	// Success: record metric and set span attributes. 5xx marks span StatusError
	// but is returned as (resp, nil) — caller decides retry/circuit-break.
	t.metrics.Record(ctx, modeRemote, outcomeSuccess)
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
	scheme, host, ok := parseEndpoint(endpoint)
	if !ok {
		return errcode.New(errcode.KindInternal, errcode.ErrInternal, msgRemoteURLRewriteFail,
			errcode.WithInternal(errcode.InternalAttr("endpoint", endpoint)))
	}
	req.URL.Scheme = scheme
	req.URL.Host = host
	return nil
}

// parseEndpoint splits a topology endpoint into its (scheme, authority) parts.
// It is the SINGLE source shared by [rewriteToAbsolute] (request URL rewrite) and
// [EndpointDialTarget] (readiness TCP dial) so the two can never drift (#2251 P2.7).
//
// Accepted forms (ok=true):
//   - "http(s)://host[:port]" → (u.Scheme, u.Host).
//   - bare "host:port" → ("http", endpoint); plaintext is only reachable for a
//     loopback/demo peer — a non-loopback split peer is required to be https and
//     gated to mTLS at construction time by cellmodules/celltransport.Resolve
//     (#2263 fail-closed gate), so this default never carries cross-network
//     traffic unencrypted.
//
// Any path (beyond an optional root "/"), query, or fragment, or an empty
// endpoint, is rejected (ok=false) rather than silently truncated (#1966 review
// P2.9; netutil.IsValidNetworkAddress already rejects these at config time — this
// is defense-in-depth, aligned on the same root-"/" tolerance).
func parseEndpoint(endpoint string) (scheme, host string, ok bool) {
	if strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		u, err := url.Parse(endpoint)
		if err != nil {
			return "", "", false
		}
		if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return "", "", false
		}
		return u.Scheme, u.Host, true
	}
	if endpoint == "" || strings.ContainsAny(endpoint, "/?#") {
		return "", "", false
	}
	return "http", endpoint, true
}

// classifyDialError wraps a client.Do transport-level error (no HTTP response
// received) into an errcode whose Kind reflects the failure cause (#1966 review
// P2.8): caller-ctx cancellation → KindClientClosed (499); deadline-exceeded /
// net timeout → KindDeadlineExceeded (504); other dial failures → KindUnavailable
// (503). The errcode is always ErrUpstreamCellUnavailable (the diagnostic code is
// about the upstream being unreachable regardless of cause; the Kind drives the
// HTTP status). reason annotates the cause for server-side logs only.
func classifyDialError(contractID, cellID string, err error) error {
	kind, reason := classifyDialCause(err)
	return errcode.New(kind, errcode.ErrUpstreamCellUnavailable,
		msgRemoteDialFailed,
		errcode.WithInternal(
			errcode.InternalAttr("reason", reason),
			errcode.InternalAttr("cellID", cellID),
			errcode.InternalAttr("contractID", contractID),
			errcode.InternalAttr("cause", err.Error()),
		))
}

// dialOutcome classifies a client.Do error into the metric outcome, mirroring
// classifyDialCause so the metric and the errcode Kind agree (#1966 review P2.6).
func dialOutcome(err error) TransportOutcome {
	switch {
	case errors.Is(err, context.Canceled):
		return outcomeCanceled
	case isTimeout(err):
		return outcomeTimeout
	default:
		return outcomeDialError
	}
}

// classifyDialCause maps a client.Do error to (errcode.Kind, reason). Cancellation
// is checked before timeout because an http.Client.Timeout-induced deadline also
// reports Timeout(); a caller-initiated cancel is distinct from a timeout.
func classifyDialCause(err error) (errcode.Kind, string) {
	switch {
	case errors.Is(err, context.Canceled):
		return errcode.KindClientClosed, "request canceled"
	case isTimeout(err):
		return errcode.KindDeadlineExceeded, "deadline exceeded"
	case errcode.IsTransientNet(err):
		return errcode.KindUnavailable, "transient network error"
	default:
		return errcode.KindUnavailable, "connection failed"
	}
}

// isTimeout reports whether err is a deadline-exceeded or net-timeout failure.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// compile-time: RemoteHTTPTransport satisfies the seam.
var _ CellTransport = (*RemoteHTTPTransport)(nil)
