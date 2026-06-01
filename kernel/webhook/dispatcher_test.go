package webhook

import (
	"context"
	"errors"
	"io"
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
		{"404_reject", http.StatusNotFound, outbox.DispositionReject},
		{"410_reject", http.StatusGone, outbox.DispositionReject},
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

func TestDispatcher_Handle_SelectorError_Reject(t *testing.T) {
	t.Parallel()
	sel := func(_ context.Context, _ []byte) (string, error) {
		return "", errors.New("no target configured")
	}
	d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
		dispatchTestSigner(t), NewSafePolicy(WithAllowLoopback()), sel)
	require.NoError(t, err)

	res := d.Handle(context.Background(), newTestEntry(t, []byte(`{}`)))
	assert.Equal(t, outbox.DispositionReject, res.Disposition)
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

// TestDispatcher_Handle_Timeout_Requeue drives a slow server past a short
// delivery timeout; a transport timeout is transient → Requeue. The handler
// sleeps a bounded interval (not block-on-context) so srv.Close cannot hang if
// the server is slow to observe the client disconnect.
func TestDispatcher_Handle_Timeout_Requeue(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond) // outlasts the 20ms delivery timeout
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d, err := NewDispatcher(clockmock.New(time.Unix(dispatchTestTS, 0)),
		dispatchTestSigner(t), NewSafePolicy(WithAllowLoopback()), staticSelector(srv.URL),
		WithDeliveryTimeout(20*time.Millisecond))
	require.NoError(t, err)

	res := d.Handle(context.Background(), newTestEntry(t, []byte(`{}`)))
	assert.Equal(t, outbox.DispositionRequeue, res.Disposition)
}
