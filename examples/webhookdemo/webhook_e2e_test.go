package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kwh "github.com/ghbvf/gocell/kernel/webhook"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// Contract-declared inbound path + signature header names
// (contracts/webhook/demo/events/v1/contract.yaml). The receiver reads the
// signature from THESE contract-declared header names, so the test sets them
// explicitly rather than using kwh.Headers.Apply, which writes the fixed
// outbound (signer-side) header constants kwh.HeaderID/HeaderTimestamp/
// HeaderSignature — a different set from a contract's inbound header names in
// the general case.
const (
	e2ePath       = "/api/webhooks/demo/events"
	hdrDeliveryID = "Webhook-Delivery-Id"
	hdrTimestamp  = "Webhook-Timestamp"
	hdrSignature  = "Webhook-Signature"
	// forgedSigToken is a well-formed v1 token (base64 of 32 zero bytes) whose MAC
	// cannot match any real signature — it exercises the verify-failure → 401 path.
	forgedSigToken = "v1,AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
)

// startWebhookdemo boots the real webhookdemo assembly on injected loopback
// listeners (so the test learns the ephemeral webhook port and can POST to it),
// waits for readiness, and registers cleanup that cancels + asserts graceful
// shutdown. It returns the webhook listener base URL.
func startWebhookdemo(t *testing.T) string {
	t.Helper()
	webhookLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	healthLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	addrs := webhookdemoAddrs{
		webhook: listenerBinding{addr: webhookLn.Addr().String(), ln: webhookLn},
		health:  listenerBinding{addr: healthLn.Addr().String(), ln: healthLn},
	}
	app, err := buildWebhookdemoBootstrap("webhookdemo", []string{"hooks"}, addrs)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- app.Run(ctx) }()

	t.Cleanup(func() {
		cancel()
		select {
		case runErr := <-runErrCh:
			if runErr != nil &&
				!errors.Is(runErr, context.Canceled) &&
				!errors.Is(runErr, context.DeadlineExceeded) {
				t.Errorf("app.Run returned a non-context error on shutdown: %v", runErr)
			}
		case <-time.After(testtime.SelectShutdown):
			t.Error("app.Run did not return within the shutdown window after cancel")
		}
	})

	healthURL := "http://" + healthLn.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := http.Get(healthURL + "/readyz") // #nosec G107,G704 -- loopback health listener under test, not user input
		if err != nil {
			return false
		}
		status := resp.StatusCode
		closeErr := resp.Body.Close()
		return status == http.StatusOK && closeErr == nil
	}, testtime.EventuallyDefault, testtime.MediumPoll, "health listener did not become ready")

	return "http://" + webhookLn.Addr().String()
}

// TestWebhookdemoE2E is the end-to-end proof of the inbound webhook path: a
// validly HMAC-signed request reaches the handler (200), while a forged or
// unsigned request is rejected by the runtime verifier before the handler runs.
func TestWebhookdemoE2E(t *testing.T) {
	t.Parallel()
	webhookURL := startWebhookdemo(t)

	// A signer for the same demo source the assembly seeded into its SourceStore.
	sourceID, err := kwh.NewSourceID(demoSourceID)
	require.NoError(t, err)
	src, err := kwh.NewSource(sourceID, []byte(demoWebhookSecret))
	require.NoError(t, err)
	signer, err := kwh.NewHMACSigner(src)
	require.NoError(t, err)

	body := []byte(`{"eventId":"evt-1","type":"order.created","data":{"orderId":"o-9"}}`)

	// newSignedReq builds a POST carrying a fresh in-window signature for the
	// given delivery id, with the signature headers under the contract's names.
	newSignedReq := func(t *testing.T, deliveryID string) *http.Request {
		t.Helper()
		did, err := kwh.NewDeliveryID(deliveryID)
		require.NoError(t, err)
		headers, err := signer.Sign(body, time.Now(), did)
		require.NoError(t, err)
		req, err := http.NewRequest(http.MethodPost, webhookURL+e2ePath, bytes.NewReader(body))
		require.NoError(t, err)
		req.Header.Set(hdrDeliveryID, string(headers.DeliveryID))
		req.Header.Set(hdrTimestamp, headers.Timestamp)
		req.Header.Set(hdrSignature, headers.Signature)
		return req
	}

	do := func(t *testing.T, req *http.Request) int {
		t.Helper()
		resp, err := http.DefaultClient.Do(req) // #nosec G704 -- targets the in-process loopback listener under test, not user input
		require.NoError(t, err)
		defer func() { assert.NoError(t, resp.Body.Close()) }()
		return resp.StatusCode
	}

	t.Run("signed request is accepted (200)", func(t *testing.T) {
		assert.Equal(t, http.StatusOK, do(t, newSignedReq(t, "delivery-accept")))
	})

	t.Run("forged signature is rejected (401)", func(t *testing.T) {
		req := newSignedReq(t, "delivery-forged")
		req.Header.Set(hdrSignature, forgedSigToken) // overwrite valid MAC with a forged one
		assert.Equal(t, http.StatusUnauthorized, do(t, req))
	})

	t.Run("missing signature header is rejected (400)", func(t *testing.T) {
		req := newSignedReq(t, "delivery-missing")
		req.Header.Del(hdrSignature)
		assert.Equal(t, http.StatusBadRequest, do(t, req))
	})

	t.Run("replayed delivery is idempotent (still 200)", func(t *testing.T) {
		// Same delivery id twice: both succeed (200). The runtime receiver's
		// idempotency claim collapses the replay (ClaimDone → cached response,
		// handler not re-run); that handler-skip is unit-tested in runtime/webhook
		// — here we only assert the end-to-end replay stays 200, not the internal
		// claim mechanics (the e2e has no seam to count handler invocations).
		first := newSignedReq(t, "delivery-replay")
		assert.Equal(t, http.StatusOK, do(t, first))
		second := newSignedReq(t, "delivery-replay")
		assert.Equal(t, http.StatusOK, do(t, second))
	})
}
