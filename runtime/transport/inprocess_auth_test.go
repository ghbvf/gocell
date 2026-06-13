package transport

import (
	"context"
	"net/http"
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/auth"
)

// TestInProcessTransport_DoesNotBypassAuthChain is the D4 security proof: an
// in-process dispatch runs the SAME listener auth chain as a network call. The
// handler is wrapped in the real auth.ServiceTokenMiddleware; a correctly signed
// request reaches the handler (200), while an unsigned request is rejected (401)
// BEFORE the handler runs. The in-process path replaces the network, not the
// governance stack.
func TestInProcessTransport_DoesNotBypassAuthChain(t *testing.T) {
	t.Parallel()

	ring, err := auth.NewHMACKeyRing([]byte("test-hmac-key-32-bytes-long-xxxxx"), nil)
	if err != nil {
		t.Fatalf("NewHMACKeyRing: %v", err)
	}
	ns, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clock.Real())
	if err != nil {
		t.Fatalf("NewInMemoryNonceStore: %v", err)
	}

	var reached bool
	guarded := auth.ServiceTokenMiddleware(ring, clock.Real(), auth.WithServiceTokenNonceStore(ns))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusOK)
		}))

	tr := NewInProcess(nil)
	if err := tr.Bind(guarded, nil); err != nil {
		t.Fatalf("bind: %v", err)
	}

	tid, err := tenant.ParseTenantID("f47ac10b-58cc-4372-a567-0e02b2c3d479")
	if err != nil {
		t.Fatalf("ParseTenantID: %v", err)
	}
	const path = "/internal/v1/config/app.name"

	t.Run("signed request passes the auth chain", func(t *testing.T) {
		reached = false
		req := newReq(t, path)
		token := auth.GenerateServiceToken(ring, "accesscore", http.MethodGet, path, "", tid, clock.Real().Now())
		req.Header.Set("Authorization", "ServiceToken "+token)
		req.Header.Set(auth.HeaderTenantID, tid.String())

		resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
		if err != nil {
			t.Fatalf("DoContract: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200 (signed token must pass)", resp.StatusCode)
		}
		if !reached {
			t.Error("handler not reached for a validly signed in-process request")
		}
	})

	t.Run("unsigned request rejected before the handler", func(t *testing.T) {
		reached = false
		resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", newReq(t, path))
		if err != nil {
			t.Fatalf("DoContract: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 (unsigned request must be rejected — in-proc must not bypass auth)", resp.StatusCode)
		}
		if reached {
			t.Error("handler was reached for an unsigned request — the in-process path bypassed the auth chain (D4 violation)")
		}
	})
}
