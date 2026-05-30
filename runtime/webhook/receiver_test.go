package webhook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

type nopReceipt struct{}

func (nopReceipt) Commit(_ context.Context) error                  { return nil }
func (nopReceipt) Release(_ context.Context) error                 { return nil }
func (nopReceipt) Extend(_ context.Context, _ time.Duration) error { return nil }

// ---- NewReceiver construction tests ----

func TestNewReceiver_NilDeps(t *testing.T) {
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
		t.Run(tt.name, func(t *testing.T) {
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

	// Verifier clock is 10 minutes ahead → outside ±300s window.
	laterClk := clockmock.New(fixedNow.Add(10 * time.Minute))
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
