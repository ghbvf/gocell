package webhook

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

const dispatchTestTS = 1700000000

// shortDeliveryTimeout is the per-attempt delivery timeout for the timeout-path
// test; negativeTimeout exercises the WithDeliveryTimeout non-positive guard
// (TEST-TIME-LITERAL-01: named package-level consts, not inline literals).
const (
	shortDeliveryTimeout = 20 * time.Millisecond
	negativeTimeout      = -1 * time.Second
)

func dispatchTestSource(t *testing.T) Source {
	t.Helper()
	src, err := NewSource(MustSourceID("dispatch-src"), minSecret())
	require.NoError(t, err)
	return src
}

func dispatchTestSigner(t *testing.T) Signer {
	t.Helper()
	s, err := NewHMACSigner(dispatchTestSource(t))
	require.NoError(t, err)
	return s
}

// staticSelector returns a WebhookDispatchSelector that always yields target.
func staticSelector(target string) WebhookDispatchSelector {
	return func(_ context.Context, _ []byte) (string, error) { return target, nil }
}

// newTestEntry builds an outbox Entry with the given payload at the fixed test
// clock so signing is deterministic.
func newTestEntry(t *testing.T, payload []byte) outbox.Entry {
	t.Helper()
	e, err := outbox.NewEntry(clockmock.New(time.Unix(dispatchTestTS, 0)), context.Background(),
		"webhook.test.v1", payload)
	require.NoError(t, err)
	return e
}

func TestNewDispatcher_NilDeps(t *testing.T) {
	t.Parallel()
	policy := NewSafePolicy(WithAllowLoopback())
	sel := staticSelector("http://example.test/")
	signer := dispatchTestSigner(t)
	clk := clockmock.New(time.Unix(dispatchTestTS, 0))

	t.Run("nil signer", func(t *testing.T) {
		t.Parallel()
		_, err := NewDispatcher(clk, nil, policy, sel)
		requireErrCode(t, err, errcode.ErrWebhookConfigInvalid, errcode.KindInvalid)
	})
	t.Run("nil policy", func(t *testing.T) {
		t.Parallel()
		_, err := NewDispatcher(clk, signer, nil, sel)
		requireErrCode(t, err, errcode.ErrWebhookConfigInvalid, errcode.KindInvalid)
	})
	t.Run("nil selector", func(t *testing.T) {
		t.Parallel()
		_, err := NewDispatcher(clk, signer, policy, nil)
		requireErrCode(t, err, errcode.ErrWebhookConfigInvalid, errcode.KindInvalid)
	})
}

// TestDispatcher_Handle_StatusMapping covers the response-status branch of the
// disposition mapping against a live loopback server.
func TestDispatcher_Handle_StatusMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status int
		want   outbox.Disposition
	}{
		{"200_ack", http.StatusOK, outbox.DispositionAck},
		{"202_ack", http.StatusAccepted, outbox.DispositionAck},
		{"500_requeue", http.StatusInternalServerError, outbox.DispositionRequeue},
		{"429_requeue", http.StatusTooManyRequests, outbox.DispositionRequeue},
		// standard-webhooks aligned: non-2xx (incl. 4xx) is a retryable failure.
		{"404_requeue", http.StatusNotFound, outbox.DispositionRequeue},
		{"410_requeue", http.StatusGone, outbox.DispositionRequeue},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
				dispatchTestSigner(t), NewSafePolicy(WithAllowLoopback()), staticSelector(srv.URL))
			require.NoError(t, err)

			res := d.Handle(context.Background(), newTestEntry(t, []byte(`{"k":"v"}`)))
			assert.Equal(t, tc.want, res.Disposition)
		})
	}
}

// TestDispatcher_Handle_SignsRequest verifies the dispatcher injects the
// vendor-neutral webhook-* signature headers and that they round-trip through
// the verifier (dispatcher↔verifier interop).
func TestDispatcher_Handle_SignsRequest(t *testing.T) {
	t.Parallel()
	src := dispatchTestSource(t)
	payload := []byte(`{"event":"order.created"}`)

	var gotID, gotTS, gotSig string
	var verifyErr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotID = r.Header.Get(HeaderID)
		gotTS = r.Header.Get(HeaderTimestamp)
		gotSig = r.Header.Get(HeaderSignature)
		verifier, err := NewHMACVerifier(clockmock.New(time.Unix(dispatchTestTS, 0)))
		require.NoError(t, err)
		verifyErr = verifier.Verify(body, Headers{
			DeliveryID: DeliveryID(gotID), Timestamp: gotTS, Signature: gotSig,
		}, src)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	signer, err := NewHMACSigner(src)
	require.NoError(t, err)
	d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
		signer, NewSafePolicy(WithAllowLoopback()), staticSelector(srv.URL))
	require.NoError(t, err)

	entry := newTestEntry(t, payload)
	res := d.Handle(context.Background(), entry)

	assert.Equal(t, outbox.DispositionAck, res.Disposition)
	assert.Equal(t, entry.ID(), gotID, "webhook-id header == entry id (delivery id)")
	assert.NotEmpty(t, gotTS, "webhook-timestamp header set")
	assert.Contains(t, gotSig, "v1,", "webhook-signature header is a v1 token")
	assert.NoError(t, verifyErr, "server-side verify of dispatcher signature")
}

// TestDispatcher_Handle_SelectorError covers F2: a generic selector error is
// transient (Requeue) so a store/config hiccup does not dead-letter the
// delivery; only a selector that signals ErrWebhookPermanentFailure (e.g. no
// subscription configured) is permanent (Reject).
func TestDispatcher_Handle_SelectorError(t *testing.T) {
	t.Parallel()

	t.Run("generic_error_requeue", func(t *testing.T) {
		t.Parallel()
		sel := func(_ context.Context, _ []byte) (string, error) {
			return "", errors.New("config store temporarily unavailable")
		}
		d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
			dispatchTestSigner(t), NewSafePolicy(WithAllowLoopback()), sel)
		require.NoError(t, err)

		res := d.Handle(context.Background(), newTestEntry(t, []byte(`{}`)))
		assert.Equal(t, outbox.DispositionRequeue, res.Disposition,
			"a generic selector error is transient (Requeue), not permanent")
	})

	t.Run("permanent_error_reject", func(t *testing.T) {
		t.Parallel()
		sel := func(_ context.Context, _ []byte) (string, error) {
			return "", errcode.New(errcode.KindInvalid, errcode.ErrWebhookPermanentFailure,
				"no webhook subscription configured for event")
		}
		d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
			dispatchTestSigner(t), NewSafePolicy(WithAllowLoopback()), sel)
		require.NoError(t, err)

		res := d.Handle(context.Background(), newTestEntry(t, []byte(`{}`)))
		assert.Equal(t, outbox.DispositionReject, res.Disposition,
			"a selector signaling ErrWebhookPermanentFailure is permanent (Reject)")
	})
}

// TestDispatcher_Handle_SSRFBlocked_Reject uses a policy WITHOUT loopback and a
// blocked metadata IP literal so ValidateTargetURL rejects before any dial.
func TestDispatcher_Handle_SSRFBlocked_Reject(t *testing.T) {
	t.Parallel()
	d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
		dispatchTestSigner(t), NewSafePolicy(), staticSelector("http://169.254.169.254/latest/meta-data/"))
	require.NoError(t, err)

	res := d.Handle(context.Background(), newTestEntry(t, []byte(`{}`)))
	require.Equal(t, outbox.DispositionReject, res.Disposition)

	var ee *errcode.Error
	require.True(t, errors.As(res.Err, &ee))
	assert.Equal(t, errcode.ErrWebhookSSRFBlocked, ee.Code)
}

// TestDispatcher_Handle_Timeout_Requeue drives a server that never responds
// within the short delivery timeout; a transport timeout is transient →
// Requeue. The handler blocks on a test-controlled channel (deterministic, no
// time.Sleep) that is closed BEFORE srv.Close — so the handler unblocks and
// srv.Close cannot hang, independent of how the server observes the client
// disconnect.
func TestDispatcher_Handle_Timeout_Requeue(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // block until the test releases, after the client has timed out
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()    // runs second: handler already released, returns promptly
	defer close(release) // runs first (LIFO): unblock the handler

	d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
		dispatchTestSigner(t), NewSafePolicy(WithAllowLoopback()), staticSelector(srv.URL),
		WithDeliveryTimeout(shortDeliveryTimeout))
	require.NoError(t, err)

	res := d.Handle(context.Background(), newTestEntry(t, []byte(`{}`)))
	assert.Equal(t, outbox.DispositionRequeue, res.Disposition)
}

// T-02: 408 Request Timeout must map to Requeue (covers the statusReason 408
// branch end-to-end, which is the same as 429 but from the opposite side of
// the switch: both are explicit cases, so dropping either case fails here).
func TestDispatcher_Handle_StatusMapping_408Requeue(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestTimeout)
	}))
	defer srv.Close()

	d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
		dispatchTestSigner(t), NewSafePolicy(WithAllowLoopback()), staticSelector(srv.URL))
	require.NoError(t, err)

	res := d.Handle(context.Background(), newTestEntry(t, []byte(`{}`)))
	assert.Equal(t, outbox.DispositionRequeue, res.Disposition,
		"408 Request Timeout must be transient (Requeue)")
}

// errSigner is a white-box test stub Signer whose Sign always fails.
// It implements sealed() because this file is package webhook (white-box).
type errSigner struct{}

func (errSigner) Sign(_ []byte, _ time.Time, _ DeliveryID) (Headers, error) {
	return Headers{}, errors.New("inject signer error")
}
func (errSigner) sealed() {}

// T-03: a Signer that always errors must produce DispositionReject (permanent
// failure; signing errors are not transient).
func TestDispatcher_Handle_SignerError_Reject(t *testing.T) {
	t.Parallel()
	d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
		errSigner{}, NewSafePolicy(WithAllowLoopback()), staticSelector("http://127.0.0.1/"))
	require.NoError(t, err)

	res := d.Handle(context.Background(), newTestEntry(t, []byte(`{}`)))
	assert.Equal(t, outbox.DispositionReject, res.Disposition,
		"signer error must produce Reject (permanent)")
}

// T-04 SKIPPED — infeasible to isolate the request-build branch deterministically.
//
// The request-build branch in prepare() is reached only when
// http.NewRequestWithContext rejects the URL. URLs with control characters
// (\x00, \x01, \x7f, etc.) are rejected by url.Parse inside ValidateTargetURL
// before ever reaching http.NewRequestWithContext — so those cannot isolate
// the request-build path without also triggering the SSRF pre-flight. URLs with
// spaces are accepted by url.Parse AND by http.NewRequestWithContext (the stdlib
// percent-encodes them), so they bypass both checks and reach the dial phase.
// There is no URL form that passes ValidateTargetURL but fails
// http.NewRequestWithContext without producing a flaky (network-dependent) test.
// The branch remains covered by integration if a caller selects a malformed URL
// at runtime. Tracking: no backlog entry needed (infeasible, not a gap in
// coverage intent).

// T-05: SSRF blocked at DIAL TIME (DNS-resolved private IP).
//
// The existing TestDispatcher_Handle_SSRFBlocked_Reject covers the
// ValidateTargetURL pre-flight path (IP literal rejected before any dial).
// This test covers the DIAL-TIME path: a public-looking hostname that passes
// ValidateTargetURL, but whose DNS lookup (via the fake resolver injected
// through withResolver) returns a private IP so the SafePolicy.DialContext
// vet fires and rejects the connection. The transport error wraps the SSRF
// errcode, which Classify maps to DispositionReject.
func TestDispatcher_Handle_SSRFBlocked_ViaDialContext_Reject(t *testing.T) {
	t.Parallel()
	// Build a SafePolicy with a fake resolver that always returns a private IP.
	// withResolver is the unexported test seam used by ssrf_test.go.
	privateResolver := fakeResolver{addrs: []net.IPAddr{{IP: net.ParseIP("10.0.0.5")}}}
	policy := NewSafePolicy(withResolver(privateResolver))

	// The selector returns a public-looking hostname; ValidateTargetURL passes
	// (no IP literal, scheme is http, host is non-empty). The dial-time vet
	// then resolves the name and blocks the private IP.
	sel := staticSelector("http://blocked.example.test/")
	d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
		dispatchTestSigner(t), policy, sel)
	require.NoError(t, err)

	res := d.Handle(context.Background(), newTestEntry(t, []byte(`{}`)))
	require.Equal(t, outbox.DispositionReject, res.Disposition,
		"dial-time SSRF block must produce Reject")

	var ee *errcode.Error
	require.True(t, errors.As(res.Err, &ee),
		"res.Err must be *errcode.Error, got %T: %v", res.Err, res.Err)
	assert.Equal(t, errcode.ErrWebhookSSRFBlocked, ee.Code,
		"dial-time SSRF error must carry ErrWebhookSSRFBlocked code")
}

// F1: a DNS resolution FAILURE at dial time is transient (Requeue), not a
// permanent SSRF block. The fail-closed refusal to dial is preserved (no
// connection is made to an un-vettable target); only the disposition changes so
// a DNS hiccup does not dead-letter the delivery.
func TestDispatcher_Handle_DNSResolutionFailure_Requeue(t *testing.T) {
	t.Parallel()
	failingResolver := fakeResolver{err: errors.New("dns timeout")}
	policy := NewSafePolicy(withResolver(failingResolver))
	d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
		dispatchTestSigner(t), policy, staticSelector("http://unresolvable.example.test/"))
	require.NoError(t, err)

	res := d.Handle(context.Background(), newTestEntry(t, []byte(`{}`)))
	assert.Equal(t, outbox.DispositionRequeue, res.Disposition,
		"DNS resolution failure must be transient (Requeue), not Reject")

	var ee *errcode.Error
	require.True(t, errors.As(res.Err, &ee))
	assert.Equal(t, errcode.ErrWebhookDeliveryFailed, ee.Code,
		"DNS failure must carry the transient delivery-failed code, not SSRF-blocked")
}

// F8: a non-2xx response body is captured (bounded) into the server-side
// Internal diagnostic so ops can see WHY a receiver rejected — without the body
// ever reaching the wire.
func TestDispatcher_Handle_NonSuccessBodyDiagnostic(t *testing.T) {
	t.Parallel()
	const bodyText = "upstream says: tenant quota exceeded"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, bodyText)
	}))
	defer srv.Close()

	d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
		dispatchTestSigner(t), NewSafePolicy(WithAllowLoopback()), staticSelector(srv.URL))
	require.NoError(t, err)

	res := d.Handle(context.Background(), newTestEntry(t, []byte(`{}`)))
	require.Equal(t, outbox.DispositionRequeue, res.Disposition)

	var ee *errcode.Error
	require.True(t, errors.As(res.Err, &ee))
	assert.Contains(t, internalAttrValue(t, ee, "response_body"), "tenant quota exceeded",
		"non-2xx response body must be captured in the server-side diagnostic")
}

// internalAttrValue extracts a named InternalDetail value from an errcode.Error
// as a string (server-only observability; never on the wire). Returns "" absent.
func internalAttrValue(t *testing.T, ee *errcode.Error, key string) string {
	t.Helper()
	for _, d := range ee.InternalDetails {
		if a := d.AsSlogAttr(); a.Key == key {
			return a.Value.String()
		}
	}
	return ""
}

// T-06: WithDeliveryTimeout ignores non-positive durations; the effective
// timeout stays defaultDeliveryTimeout.
//
// White-box test: reads the unexported timeout field directly.
func TestWithDeliveryTimeout_NonPositiveIgnored(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		d    time.Duration
	}{
		{"zero", 0},
		{"negative", negativeTimeout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
				dispatchTestSigner(t), NewSafePolicy(WithAllowLoopback()), staticSelector("http://127.0.0.1/"),
				WithDeliveryTimeout(tc.d))
			require.NoError(t, err)
			assert.Equal(t, defaultDeliveryTimeout, d.timeout,
				"non-positive duration %v must leave timeout at defaultDeliveryTimeout", tc.d)
		})
	}
}
