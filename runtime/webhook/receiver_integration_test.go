//go:build integration

package webhook_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kwh "github.com/ghbvf/gocell/kernel/webhook"
	rtwh "github.com/ghbvf/gocell/runtime/webhook"
)

// buildIntegrationServer constructs a real httptest.Server with the full
// BuildRouteGroups pipeline and a simple chi-like mux backed by stdlib
// http.ServeMux that routes through the registered handler.
func buildIntegrationServer(t *testing.T, handler kwh.WebhookReceiveHandler) (*httptest.Server, kwh.Signer) {
	t.Helper()

	clk := clockmock.New(fixedNow)
	store := testStore(t)
	claimer := idempotency.NewInMemClaimer(clk)

	if handler == nil {
		handler = func(_ context.Context, _ kwh.Delivery) error { return nil }
	}

	reqs := []cell.WebhookReceiverRequest{
		{
			Spec:    testSpec(),
			Handler: handler,
		},
	}

	groups, err := rtwh.BuildRouteGroups(reqs, store, claimer, clk)
	if err != nil {
		t.Fatalf("BuildRouteGroups: %v", err)
	}

	// Wire route groups into a stdlib ServeMux via the stubMux adapter.
	// Pass g.Prefix so Mount can qualify the "/" argument from BuildRouteGroups.
	mux := http.NewServeMux()
	for _, g := range groups {
		stub := &serveMuxAdapter{mux: mux, prefix: g.Prefix}
		if err := g.Register(stub); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// Return the signer for the test source so callers can produce valid requests.
	src := testSource(t)
	signer, err := kwh.NewHMACSigner(src)
	if err != nil {
		t.Fatalf("NewHMACSigner: %v", err)
	}
	return srv, signer
}

// serveMuxAdapter adapts cell.RouteMux (Mount) to stdlib *http.ServeMux.
// prefix is the RouteGroup.Prefix (e.g. "/webhooks/test") that was in effect
// when Register was called — BuildRouteGroups now mounts at "/" so the adapter
// must prefix-qualify the registration itself.
type serveMuxAdapter struct {
	mux    *http.ServeMux
	prefix string // RouteGroup.Prefix for this adapter; empty = root
}

func (s *serveMuxAdapter) Handle(pattern string, h http.Handler) {
	s.mux.Handle(pattern, h)
}
func (s *serveMuxAdapter) Route(pattern string, fn func(cell.RouteMux)) {
	sub := &serveMuxAdapter{mux: s.mux}
	fn(sub)
}
func (s *serveMuxAdapter) Mount(pattern string, h http.Handler) {
	// BuildRouteGroups calls mux.Mount("/", handler) so the Receiver is mounted
	// under the adapter's prefix. Compose: effective = prefix + pattern.
	effective := s.prefix
	if effective == "" {
		effective = pattern
	} else if pattern != "/" {
		effective = effective + pattern
	}
	effective = strings.TrimSuffix(effective, "/")
	s.mux.Handle(effective+"/", http.StripPrefix(effective, h))
	s.mux.Handle(effective, h)
}
func (s *serveMuxAdapter) Group(fn func(cell.RouteMux)) { fn(s) }
func (s *serveMuxAdapter) With(_ ...func(http.Handler) http.Handler) cell.RouteMux {
	return s
}

// sendSignedRequest sends a signed webhook delivery to srv and returns the response.
func sendSignedRequest(t *testing.T, srv *httptest.Server, signer kwh.Signer, body []byte, deliveryID string) *http.Response {
	t.Helper()
	did, err := kwh.NewDeliveryID(deliveryID)
	if err != nil {
		t.Fatalf("NewDeliveryID: %v", err)
	}
	headers, err := signer.Sign(body, fixedNow, did)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, srv.URL+testPathPattern, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-Delivery-Id", string(headers.DeliveryID))
	req.Header.Set("X-Timestamp", headers.Timestamp)
	req.Header.Set("X-Signature", headers.Signature)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestIntegration_EndToEnd_ValidDelivery(t *testing.T) {
	handlerCalled := 0
	srv, signer := buildIntegrationServer(t, func(_ context.Context, d kwh.Delivery) error {
		handlerCalled++
		return nil
	})

	body := []byte(`{"event":"integration-test"}`)
	resp := sendSignedRequest(t, srv, signer, body, "int-msg-001")

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, raw)
	}
	if handlerCalled != 1 {
		t.Errorf("handler called %d times, want 1", handlerCalled)
	}

	// Verify response shape.
	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result["status"] != "accepted" {
		t.Errorf("response.status = %q, want accepted", result["status"])
	}
}

func TestIntegration_IdempotentReplay(t *testing.T) {
	handlerCalled := 0
	srv, signer := buildIntegrationServer(t, func(_ context.Context, _ kwh.Delivery) error {
		handlerCalled++
		return nil
	})

	body := []byte(`{"event":"replay-test"}`)
	deliveryID := "int-replay-001"

	// First request — handler must be called once, response 200.
	resp1 := sendSignedRequest(t, srv, signer, body, deliveryID)
	if resp1.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp1.Body)
		t.Fatalf("first request: status = %d, body = %s", resp1.StatusCode, raw)
	}
	if handlerCalled != 1 {
		t.Fatalf("first request: handler called %d times, want 1", handlerCalled)
	}

	// Second request with the same delivery ID — handler must NOT be called again;
	// response is 200 (idempotent acknowledged).
	resp2 := sendSignedRequest(t, srv, signer, body, deliveryID)
	if resp2.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp2.Body)
		t.Fatalf("second request: status = %d, body = %s", resp2.StatusCode, raw)
	}
	if handlerCalled != 1 {
		t.Errorf("second request: handler called %d times total, want 1 (idempotent)", handlerCalled)
	}
}

func TestIntegration_MissingHeader_Returns400(t *testing.T) {
	srv, signer := buildIntegrationServer(t, nil)
	body := []byte(`{}`)

	did, _ := kwh.NewDeliveryID("int-miss-hdr")
	headers, _ := signer.Sign(body, fixedNow, did)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+testPathPattern, bytes.NewReader(body))
	req.Header.Set("X-Delivery-Id", string(headers.DeliveryID))
	// Intentionally omit X-Timestamp and X-Signature.

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, raw)
	}
}

func TestIntegration_BadSignature_Returns401(t *testing.T) {
	srv, signer := buildIntegrationServer(t, nil)
	body := []byte(`{}`)

	did, _ := kwh.NewDeliveryID("int-bad-sig")
	headers, _ := signer.Sign(body, fixedNow, did)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+testPathPattern, bytes.NewReader(body))
	req.Header.Set("X-Delivery-Id", string(headers.DeliveryID))
	req.Header.Set("X-Timestamp", headers.Timestamp)
	req.Header.Set("X-Signature", "v1,AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") // forged

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, raw)
	}
}

func TestIntegration_StatusCodeCoverage(t *testing.T) {
	tests := []struct {
		name       string
		deliveryID string
		mutate     func(*http.Request, kwh.Headers)
		wantStatus int
	}{
		{
			name:       "valid delivery -> 200",
			deliveryID: "cov-200",
			wantStatus: http.StatusOK,
		},
		{
			name:       "missing signature header -> 400",
			deliveryID: "cov-400",
			mutate: func(r *http.Request, _ kwh.Headers) {
				r.Header.Del("X-Signature")
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid signature -> 401",
			deliveryID: "cov-401",
			mutate: func(r *http.Request, _ kwh.Headers) {
				r.Header.Set("X-Signature", "v1,AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
			},
			wantStatus: http.StatusUnauthorized,
		},
	}

	srv, signer := buildIntegrationServer(t, nil)
	body := []byte(`{"event":"cov"}`)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			did, _ := kwh.NewDeliveryID(tt.deliveryID)
			headers, _ := signer.Sign(body, fixedNow, did)

			req, _ := http.NewRequest(http.MethodPost, srv.URL+testPathPattern, bytes.NewReader(body))
			req.Header.Set("X-Delivery-Id", string(headers.DeliveryID))
			req.Header.Set("X-Timestamp", headers.Timestamp)
			req.Header.Set("X-Signature", headers.Signature)
			if tt.mutate != nil {
				tt.mutate(req, headers)
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, tt.wantStatus, raw)
			}
		})
	}
}
