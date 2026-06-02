package webhook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kwh "github.com/ghbvf/gocell/kernel/webhook"
	"github.com/ghbvf/gocell/pkg/errcode"
	rtwh "github.com/ghbvf/gocell/runtime/webhook"
)

// ---- test fixtures ----

const (
	testSourceID     = "test-source"
	testContractID   = "webhook.test.v1"
	testCellID       = "testcell"
	testPathPattern  = "/webhooks/test"
	testSecret       = "12345678901234567890abcd" // 24 bytes
	testMaxBodyBytes = 1 << 20                    // 1 MiB
	testTolerance    = 300                        // seconds

	// testReplaySkew is the clock offset used to place a delivery outside the
	// tolerance window in timestamp-expiry tests (must exceed testTolerance seconds).
	testReplaySkew = 10 * time.Minute
)

// fixedNow is a stable reference time used across tests.
var fixedNow = time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

func testSpec() kwh.ReceiverSpec {
	return kwh.ReceiverSpec{
		ContractID:       testContractID,
		SourceID:         testSourceID,
		CellID:           testCellID,
		PathPattern:      testPathPattern,
		DeliveryIDHeader: "X-Delivery-Id",
		TimestampHeader:  "X-Timestamp",
		SignatureHeader:  "X-Signature",
		ToleranceSeconds: testTolerance,
		MaxBodyBytes:     testMaxBodyBytes,
	}
}

func testSource(t *testing.T) kwh.Source {
	t.Helper()
	id, err := kwh.NewSourceID(testSourceID)
	if err != nil {
		t.Fatalf("NewSourceID: %v", err)
	}
	src, err := kwh.NewSource(id, []byte(testSecret))
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	return src
}

func testStore(t *testing.T) *kwh.SourceRegistry {
	t.Helper()
	reg := kwh.NewSourceRegistry()
	if err := reg.Register(testSource(t)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return reg
}

// signedRequest builds an *http.Request with a valid HMAC signature for body.
func signedRequest(t *testing.T, body []byte, deliveryID string) *http.Request {
	t.Helper()
	src := testSource(t)
	signer, err := kwh.NewHMACSigner(src)
	if err != nil {
		t.Fatalf("NewHMACSigner: %v", err)
	}
	did, err := kwh.NewDeliveryID(deliveryID)
	if err != nil {
		t.Fatalf("NewDeliveryID: %v", err)
	}
	headers, err := signer.Sign(body, fixedNow, did)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, testPathPattern, bytes.NewReader(body))
	req.Header.Set("X-Delivery-Id", string(headers.DeliveryID))
	req.Header.Set("X-Timestamp", headers.Timestamp)
	req.Header.Set("X-Signature", headers.Signature)
	return req
}

// buildReceiver constructs a Receiver with optional overrides.
type receiverOpts struct {
	claimer idempotency.Claimer
	handler kwh.WebhookReceiveHandler
}

func buildReceiver(t *testing.T, opts receiverOpts) *rtwh.Receiver {
	t.Helper()
	clk := clockmock.New(fixedNow)
	store := testStore(t)
	verifier, err := kwh.NewHMACVerifier(clk, kwh.WithTolerance(testTolerance*time.Second))
	if err != nil {
		t.Fatalf("NewHMACVerifier: %v", err)
	}
	claimer := opts.claimer
	if claimer == nil {
		claimer = idempotency.NewInMemClaimer(clk)
	}
	handler := opts.handler
	if handler == nil {
		handler = func(_ context.Context, _ kwh.Delivery) error { return nil }
	}
	recv, err := rtwh.NewReceiver(clk, testSpec(), verifier, store, claimer, handler)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	return recv
}

// ---- fakeClaimer — configures the claim state per call ----

type claimResult struct {
	state idempotency.ClaimState
	err   error
}

type fakeClaimer struct {
	results []claimResult
	calls   int
}

func (f *fakeClaimer) Claim(_ context.Context, _ string, _, _ time.Duration) (idempotency.ClaimState, idempotency.Receipt, error) {
	if f.calls >= len(f.results) {
		return idempotency.ClaimAcquired, idempotency.NonAcquiredReceipt(), nil
	}
	r := f.results[f.calls]
	f.calls++
	if r.err != nil {
		return idempotency.ClaimBusy, nil, r.err
	}
	switch r.state {
	case idempotency.ClaimDone, idempotency.ClaimBusy:
		return r.state, idempotency.NonAcquiredReceipt(), nil
	default: // ClaimAcquired
		return r.state, &nopReceipt{}, nil
	}
}

func (f *fakeClaimer) Kind() idempotency.ClaimerKind { return idempotency.ClaimerKindInMemory }

type nopReceipt struct{}

func (nopReceipt) Commit(_ context.Context) error                  { return nil }
func (nopReceipt) Release(_ context.Context) error                 { return nil }
func (nopReceipt) Extend(_ context.Context, _ time.Duration) error { return nil }

// countingReceipt records Commit and Release call counts.
// commitErr, if non-nil, is returned by Commit to test best-effort commit failure.
// releaseErr, if non-nil, is returned by Release.
type countingReceipt struct {
	committed  int
	released   int
	commitErr  error
	releaseErr error
}

func (r *countingReceipt) Commit(_ context.Context) error                  { r.committed++; return r.commitErr }
func (r *countingReceipt) Release(_ context.Context) error                 { r.released++; return r.releaseErr }
func (r *countingReceipt) Extend(_ context.Context, _ time.Duration) error { return nil }

// countingClaimer always returns ClaimAcquired with a shared countingReceipt.
type countingClaimer struct {
	rcpt *countingReceipt
}

func (c *countingClaimer) Claim(_ context.Context, _ string, _, _ time.Duration) (idempotency.ClaimState, idempotency.Receipt, error) {
	return idempotency.ClaimAcquired, c.rcpt, nil
}

func (c *countingClaimer) Kind() idempotency.ClaimerKind { return idempotency.ClaimerKindInMemory }

// ---- NewReceiver construction tests ----

func TestNewReceiver_NilDeps(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(fixedNow)
	store := testStore(t)
	verifier, _ := kwh.NewHMACVerifier(clk)
	claimer := idempotency.NewInMemClaimer(clk)
	handler := func(_ context.Context, _ kwh.Delivery) error { return nil }
	spec := testSpec()

	t.Run("nil verifier", func(t *testing.T) {
		_, err := rtwh.NewReceiver(clk, spec, nil, store, claimer, handler)
		if err == nil {
			t.Fatal("expected error for nil verifier")
		}
	})
	t.Run("nil store", func(t *testing.T) {
		_, err := rtwh.NewReceiver(clk, spec, verifier, nil, claimer, handler)
		if err == nil {
			t.Fatal("expected error for nil store")
		}
	})
	t.Run("nil claimer", func(t *testing.T) {
		_, err := rtwh.NewReceiver(clk, spec, verifier, store, nil, handler)
		if err == nil {
			t.Fatal("expected error for nil claimer")
		}
	})
	t.Run("nil handler", func(t *testing.T) {
		_, err := rtwh.NewReceiver(clk, spec, verifier, store, claimer, nil)
		if err == nil {
			t.Fatal("expected error for nil handler")
		}
	})
}

// ---- ServeHTTP table-driven tests ----

func TestReceiver_ServeHTTP(t *testing.T) {
	t.Parallel()
	body := []byte(`{"event":"test"}`)
	deliveryID := "msg-001"

	tests := []struct {
		name       string
		req        func() *http.Request
		claimer    idempotency.Claimer
		handler    kwh.WebhookReceiveHandler
		wantStatus int
	}{
		{
			name:       "valid signature + handler success -> 200",
			req:        func() *http.Request { return signedRequest(t, body, deliveryID) },
			wantStatus: http.StatusOK,
		},
		{
			name: "repeat deliveryID (ClaimDone) -> 200 idempotent",
			req:  func() *http.Request { return signedRequest(t, body, deliveryID+"-done") },
			claimer: &fakeClaimer{results: []claimResult{
				{state: idempotency.ClaimDone},
			}},
			wantStatus: http.StatusOK,
		},
		{
			name: "concurrent delivery (ClaimBusy) -> 409",
			req:  func() *http.Request { return signedRequest(t, body, deliveryID+"-busy") },
			claimer: &fakeClaimer{results: []claimResult{
				{state: idempotency.ClaimBusy},
			}},
			wantStatus: http.StatusConflict,
		},
		{
			name: "claimer infra error -> 503",
			req:  func() *http.Request { return signedRequest(t, body, deliveryID+"-infra") },
			claimer: &fakeClaimer{results: []claimResult{
				{state: idempotency.ClaimBusy, err: errors.New("redis down")},
			}},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "missing delivery-id header -> 400",
			req: func() *http.Request {
				req := signedRequest(t, body, "msg-hdr")
				req.Header.Del("X-Delivery-Id")
				return req
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "missing timestamp header -> 400",
			req: func() *http.Request {
				req := signedRequest(t, body, "msg-ts")
				req.Header.Del("X-Timestamp")
				return req
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "missing signature header -> 400",
			req: func() *http.Request {
				req := signedRequest(t, body, "msg-sig")
				req.Header.Del("X-Signature")
				return req
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "bad signature -> 401",
			req: func() *http.Request {
				req := signedRequest(t, body, "msg-badsig")
				req.Header.Set("X-Signature", "v1,AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
				return req
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "body too large -> 413",
			req: func() *http.Request {
				// Use a spec-level Content-Length pre-check: set Content-Length > MaxBodyBytes.
				bigBody := bytes.Repeat([]byte("x"), int(testMaxBodyBytes)+1)
				req := signedRequest(t, bigBody[:10], "msg-large")
				req.ContentLength = int64(testMaxBodyBytes) + 1
				return req
			},
			wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name: "handler returns KindUnavailable -> 503",
			req:  func() *http.Request { return signedRequest(t, body, "msg-handler503") },
			handler: func(_ context.Context, _ kwh.Delivery) error {
				return errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable,
					"downstream unavailable")
			},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "handler returns generic error -> 500",
			req:  func() *http.Request { return signedRequest(t, body, "msg-handler500") },
			handler: func(_ context.Context, _ kwh.Delivery) error {
				return errors.New("unexpected failure")
			},
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			recv := buildReceiver(t, receiverOpts{
				claimer: tt.claimer,
				handler: tt.handler,
			})
			w := httptest.NewRecorder()
			recv.ServeHTTP(w, tt.req())
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}
}

func TestReceiver_SuccessResponseShape(t *testing.T) {
	t.Parallel()
	body := []byte(`{"event":"test"}`)
	recv := buildReceiver(t, receiverOpts{})
	w := httptest.NewRecorder()
	recv.ServeHTTP(w, signedRequest(t, body, "msg-shape"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["status"] != "accepted" {
		t.Fatalf("response.status = %q, want accepted", resp["status"])
	}
}

func TestReceiver_ErrorResponseEnvelope(t *testing.T) {
	t.Parallel()
	body := []byte(`{}`)
	recv := buildReceiver(t, receiverOpts{})
	w := httptest.NewRecorder()
	// Trigger a 400 by omitting the delivery-id header.
	req := signedRequest(t, body, "msg-env-check")
	req.Header.Del("X-Delivery-Id")
	recv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var envelope map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := envelope["error"]; !ok {
		t.Fatalf("response missing 'error' key; got: %s", w.Body.String())
	}
}

func TestReceiver_UnknownSource_Returns401(t *testing.T) {
	t.Parallel()
	// Build a receiver pointing at a source that isn't in the store.
	clk := clockmock.New(fixedNow)
	emptyStore := kwh.NewSourceRegistry()
	verifier, _ := kwh.NewHMACVerifier(clk, kwh.WithTolerance(testTolerance*time.Second))
	claimer := idempotency.NewInMemClaimer(clk)
	handler := func(_ context.Context, _ kwh.Delivery) error { return nil }

	recv, err := rtwh.NewReceiver(clk, testSpec(), verifier, emptyStore, claimer, handler)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	w := httptest.NewRecorder()
	recv.ServeHTTP(w, signedRequest(t, []byte("payload"), "msg-unknown"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for unknown source", w.Code)
	}
}

func TestReceiver_TimestampExpired_Returns401(t *testing.T) {
	t.Parallel()
	// The signer uses fixedNow but the verifier's clock is 10 minutes ahead
	// (outside the 300s tolerance window).
	body := []byte(`{"event":"test"}`)
	deliveryID := "msg-expired"

	// Sign at fixedNow.
	src := testSource(t)
	signer, _ := kwh.NewHMACSigner(src)
	did, _ := kwh.NewDeliveryID(deliveryID)
	headers, _ := signer.Sign(body, fixedNow, did)

	req := httptest.NewRequest(http.MethodPost, testPathPattern, bytes.NewReader(body))
	req.Header.Set("X-Delivery-Id", string(headers.DeliveryID))
	req.Header.Set("X-Timestamp", headers.Timestamp)
	req.Header.Set("X-Signature", headers.Signature)

	// Verifier clock is testReplaySkew ahead → outside ±300s window.
	laterClk := clockmock.New(fixedNow.Add(testReplaySkew))
	store := testStore(t)
	verifier, _ := kwh.NewHMACVerifier(laterClk, kwh.WithTolerance(testTolerance*time.Second))
	claimer := idempotency.NewInMemClaimer(laterClk)
	handler := func(_ context.Context, _ kwh.Delivery) error { return nil }

	recv, err := rtwh.NewReceiver(laterClk, testSpec(), verifier, store, claimer, handler)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	w := httptest.NewRecorder()
	recv.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for expired timestamp", w.Code)
	}
}

func TestReceiver_BodyExceedsLimitViaMaxBytesReader(t *testing.T) {
	t.Parallel()
	// Build a receiver with a tiny body limit.
	spec := testSpec()
	spec.MaxBodyBytes = 10 // 10 bytes limit

	clk := clockmock.New(fixedNow)
	store := testStore(t)
	verifier, _ := kwh.NewHMACVerifier(clk, kwh.WithTolerance(testTolerance*time.Second))
	claimer := idempotency.NewInMemClaimer(clk)
	handler := func(_ context.Context, _ kwh.Delivery) error { return nil }

	recv, err := rtwh.NewReceiver(clk, spec, verifier, store, claimer, handler)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	// Body that exceeds the 10-byte limit (Content-Length matches so pre-check passes).
	bigBody := bytes.Repeat([]byte("x"), 100)
	// Sign with the actually-sent body so signature is plausible (doesn't matter — 413 fires first).
	src := testSource(t)
	signer, _ := kwh.NewHMACSigner(src)
	did, _ := kwh.NewDeliveryID("msg-big-body")
	headers, _ := signer.Sign(bigBody, fixedNow, did)

	req := httptest.NewRequest(http.MethodPost, testPathPattern, bytes.NewReader(bigBody))
	req.Header.Set("X-Delivery-Id", string(headers.DeliveryID))
	req.Header.Set("X-Timestamp", headers.Timestamp)
	req.Header.Set("X-Signature", headers.Signature)
	// Don't set Content-Length above limit — let MaxBytesReader detect it.
	req.ContentLength = int64(len(bigBody)) // correct but above limit

	w := httptest.NewRecorder()
	recv.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body: %s", w.Code, w.Body.String())
	}
}

func TestReceiver_HandlerReceivesDelivery(t *testing.T) {
	t.Parallel()
	body := []byte(`{"event":"check-delivery"}`)
	deliveryID := "msg-delivery-check"

	var gotDelivery kwh.Delivery
	handler := func(_ context.Context, d kwh.Delivery) error {
		gotDelivery = d
		return nil
	}
	recv := buildReceiver(t, receiverOpts{handler: handler})
	w := httptest.NewRecorder()
	recv.ServeHTTP(w, signedRequest(t, body, deliveryID))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !bytes.Equal(gotDelivery.Payload, body) {
		t.Errorf("payload = %q, want %q", gotDelivery.Payload, body)
	}
	if string(gotDelivery.DeliveryID) != deliveryID {
		t.Errorf("deliveryID = %q, want %q", gotDelivery.DeliveryID, deliveryID)
	}
	if string(gotDelivery.SourceID) != testSourceID {
		t.Errorf("sourceID = %q, want %q", gotDelivery.SourceID, testSourceID)
	}
}

// TestReceiver_HandlerPanic_ReleasesLeaseAndRepanics verifies that when the
// business handler panics, the Receiver:
//  1. Calls Release on the idempotency receipt (so the TTL does not permanently
//     block re-delivery).
//  2. Re-panics with the original value so the HTTP recovery middleware can
//     convert the panic to a 500 response.
//
// This test uses a shared countingReceipt; the handler and release assertions
// require a specific execution order, so the sub-cases are NOT run in parallel.
func TestReceiver_HandlerPanic_ReleasesLeaseAndRepanics(t *testing.T) {
	t.Parallel()

	body := []byte(`{"event":"panic-test"}`)
	rcpt := &countingReceipt{}
	claimer := &countingClaimer{rcpt: rcpt}

	panicValue := "test-handler-panic"
	handler := func(_ context.Context, _ kwh.Delivery) error {
		panic(panicValue)
	}

	clk := clockmock.New(fixedNow)
	store := testStore(t)
	verifier, err := kwh.NewHMACVerifier(clk, kwh.WithTolerance(testTolerance*time.Second))
	if err != nil {
		t.Fatalf("NewHMACVerifier: %v", err)
	}

	recv, err := rtwh.NewReceiver(clk, testSpec(), verifier, store, claimer, handler)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	// Capture the re-panic from dispatch so we can assert it propagated.
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		w := httptest.NewRecorder()
		recv.ServeHTTP(w, signedRequest(t, body, "msg-panic-001"))
	}()

	// The lease must have been released before the panic propagated.
	if rcpt.released != 1 {
		t.Errorf("Release called %d times, want 1", rcpt.released)
	}

	// Commit must NOT have been called: handler panicked before success.
	if rcpt.committed != 0 {
		t.Errorf("Commit called %d times, want 0 (no commit on handler panic)", rcpt.committed)
	}

	// The panic must have re-propagated (recovered is non-nil).
	if recovered == nil {
		t.Error("expected panic to re-propagate, but recover() returned nil")
	}
}

// TestReceiver_CommitFailure_Still200 verifies that a Commit failure after a
// successful handler invocation does not change the HTTP 200 response. Commit
// is best-effort: the business operation succeeded; only the idempotency
// record write failed, so the caller still receives 200 Accepted.
func TestReceiver_CommitFailure_Still200(t *testing.T) {
	// Not parallel: uses a shared mutable countingReceipt.
	body := []byte(`{"event":"commit-fail-test"}`)
	rcpt := &countingReceipt{commitErr: errors.New("commit boom")}
	claimer := &countingClaimer{rcpt: rcpt}

	recv := buildReceiver(t, receiverOpts{claimer: claimer})
	w := httptest.NewRecorder()
	recv.ServeHTTP(w, signedRequest(t, body, "msg-commit-fail-001"))

	// Handler succeeded → 200 even though Commit returned an error.
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	// Commit was attempted exactly once.
	if rcpt.committed != 1 {
		t.Errorf("Commit called %d times, want 1", rcpt.committed)
	}
	// Release must NOT have been called: handler succeeded.
	if rcpt.released != 0 {
		t.Errorf("Release called %d times, want 0 (successful handler does not release)", rcpt.released)
	}
}

// errReader is a minimal io.Reader that always returns a non-MaxBytesError on
// Read, used to simulate a network/IO error during body reading.
type errReader struct{ err error }

func (e errReader) Read(_ []byte) (int, error) { return 0, e.err }

// TestReceiver_ReadBodyIOError_Returns503 verifies that an IO error during body
// reading (that is not a MaxBytesError) is mapped to HTTP 503 Service
// Unavailable. The handler must not be invoked in this case.
func TestReceiver_ReadBodyIOError_Returns503(t *testing.T) {
	t.Parallel()

	spec := testSpec()
	// Ensure ContentLength is below MaxBodyBytes so the pre-check does not
	// fire a 413 before the reader is even installed.
	clk := clockmock.New(fixedNow)
	store := testStore(t)
	verifier, err := kwh.NewHMACVerifier(clk, kwh.WithTolerance(testTolerance*time.Second))
	if err != nil {
		t.Fatalf("NewHMACVerifier: %v", err)
	}
	claimer := idempotency.NewInMemClaimer(clk)
	handlerCalled := false
	handler := func(_ context.Context, _ kwh.Delivery) error {
		handlerCalled = true
		return nil
	}
	recv, err := rtwh.NewReceiver(clk, spec, verifier, store, claimer, handler)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	ioErr := errors.New("network read error")
	req := httptest.NewRequest(http.MethodPost, testPathPattern, nil)
	// Replace the body with a reader that always fails; set ContentLength to a
	// small positive value so the Content-Length pre-check passes.
	req.Body = io.NopCloser(errReader{err: ioErr})
	req.ContentLength = 5 // below MaxBodyBytes; pre-check passes

	// Set required signature headers so the error is not caused by header parsing.
	req.Header.Set("X-Delivery-Id", "msg-ioerr-001")
	req.Header.Set("X-Timestamp", "1705312800")
	req.Header.Set("X-Signature", "v1,AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")

	w := httptest.NewRecorder()
	recv.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", w.Code, w.Body.String())
	}
	if handlerCalled {
		t.Error("handler must not be invoked when body read fails")
	}
	// Verify the error envelope contains the error field.
	var envelope map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if _, ok := envelope["error"]; !ok {
		t.Errorf("response missing 'error' key; got: %s", w.Body.String())
	}
}

// TestNewReceiver_InvalidSpec covers the spec.Validate() failure branch in
// NewReceiver (a zero-value spec fails validation before any dependency check).
func TestNewReceiver_InvalidSpec(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(fixedNow)
	store := testStore(t)
	verifier, _ := kwh.NewHMACVerifier(clk)
	claimer := idempotency.NewInMemClaimer(clk)
	handler := func(_ context.Context, _ kwh.Delivery) error { return nil }

	_, err := rtwh.NewReceiver(clk, kwh.ReceiverSpec{}, verifier, store, claimer, handler)
	if err == nil {
		t.Fatal("expected error for invalid (zero-value) spec")
	}
}

// TestReceiver_HandlerError_ReleaseError covers the branch where the handler
// returns an error AND the subsequent Release also fails (the release-error is
// logged, the handler error still drives the HTTP status).
func TestReceiver_HandlerError_ReleaseError(t *testing.T) {
	// Not parallel: uses a shared mutable countingReceipt.
	body := []byte(`{"event":"handler-err-release-err"}`)
	rcpt := &countingReceipt{releaseErr: errors.New("release boom")}
	claimer := &countingClaimer{rcpt: rcpt}
	handler := func(_ context.Context, _ kwh.Delivery) error {
		return errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable,
			"handler transient failure")
	}
	recv := buildReceiver(t, receiverOpts{claimer: claimer, handler: handler})
	w := httptest.NewRecorder()
	recv.ServeHTTP(w, signedRequest(t, body, "msg-handler-err-rel-001"))

	// KindUnavailable handler error → 503.
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", w.Code, w.Body.String())
	}
	if rcpt.released != 1 {
		t.Errorf("Release called %d times, want 1", rcpt.released)
	}
	if rcpt.committed != 0 {
		t.Errorf("Commit called %d times, want 0 (handler failed)", rcpt.committed)
	}
}

// TestReceiver_HandlerPanic_ReleaseError covers releaseOnPanic's branch where
// Release itself returns an error during the panic-recovery path.
func TestReceiver_HandlerPanic_ReleaseError(t *testing.T) {
	// Not parallel: shared mutable countingReceipt + specific execution order.
	body := []byte(`{"event":"panic-release-err"}`)
	rcpt := &countingReceipt{releaseErr: errors.New("release boom")}
	claimer := &countingClaimer{rcpt: rcpt}
	handler := func(_ context.Context, _ kwh.Delivery) error {
		panic("handler panic with failing release")
	}
	recv := buildReceiver(t, receiverOpts{claimer: claimer, handler: handler})

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		w := httptest.NewRecorder()
		recv.ServeHTTP(w, signedRequest(t, body, "msg-panic-rel-err-001"))
	}()

	if rcpt.released != 1 {
		t.Errorf("Release called %d times, want 1", rcpt.released)
	}
	if recovered == nil {
		t.Error("expected panic to re-propagate even when Release fails")
	}
}

// TestReceiver_MaxBytesReaderTruncation_Returns413 covers the MaxBytesReader
// (read-time) 413 branch — distinct from the Content-Length pre-check. With an
// unknown Content-Length (-1) the pre-check is skipped and MaxBytesReader is the
// only enforcer.
func TestReceiver_MaxBytesReaderTruncation_Returns413(t *testing.T) {
	t.Parallel()
	spec := testSpec()
	spec.MaxBodyBytes = 10

	clk := clockmock.New(fixedNow)
	store := testStore(t)
	verifier, err := kwh.NewHMACVerifier(clk, kwh.WithTolerance(testTolerance*time.Second))
	if err != nil {
		t.Fatalf("NewHMACVerifier: %v", err)
	}
	claimer := idempotency.NewInMemClaimer(clk)
	handler := func(_ context.Context, _ kwh.Delivery) error { return nil }
	recv, err := rtwh.NewReceiver(clk, spec, verifier, store, claimer, handler)
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}

	bigBody := bytes.Repeat([]byte("x"), 100)
	req := httptest.NewRequest(http.MethodPost, testPathPattern, bytes.NewReader(bigBody))
	req.Header.Set("X-Delivery-Id", "msg-maxbytes-001")
	req.Header.Set("X-Timestamp", "1705312800")
	req.Header.Set("X-Signature", "v1,AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	// Unknown Content-Length → pre-check skipped; MaxBytesReader enforces the cap.
	req.ContentLength = -1

	w := httptest.NewRecorder()
	recv.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body: %s", w.Code, w.Body.String())
	}
}

// TestReceiver_InvalidDeliveryIDFormat_Returns400 covers verify()'s
// NewDeliveryID error branch: a non-empty but malformed delivery-id header
// (whitespace is forbidden by the delivery-id pattern) → 400.
func TestReceiver_InvalidDeliveryIDFormat_Returns400(t *testing.T) {
	t.Parallel()
	recv := buildReceiver(t, receiverOpts{})
	req := signedRequest(t, []byte(`{"e":1}`), "valid-id")
	// Override with a non-empty value that fails the delivery-id pattern (space).
	req.Header.Set("X-Delivery-Id", "bad id with spaces")
	w := httptest.NewRecorder()
	recv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}
