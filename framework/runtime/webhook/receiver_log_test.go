package webhook_test

// receiver_log_test.go covers the C4/F7 finding: the signature-verification
// failure log must carry the classified "reason" field that doc.go promises
// ("the real reason is in the server-side slog 'reason' field"). Prior to the
// fix the metric carried {reason} but the slog record did not, so the doc and
// the code disagreed.

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"sync"
	"testing"

	kwh "github.com/ghbvf/gocell/framework/kernel/webhook"

	"github.com/ghbvf/gocell/framework/pkg/testutil/slogcapture"
)

// capturingHandler is a minimal slog.Handler that records every Record it
// receives. It deliberately does NOT use slog.NewJSONHandler/NewTextHandler
// (those are banned outside the logging package by SLOG-HANDLER-SEALED-FUNNEL-01)
// — it captures structured attrs directly.
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

// attr returns the string value of the named attr in r, and whether it existed.
func attr(r slog.Record, key string) (string, bool) {
	var val string
	var found bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			val = a.Value.String()
			found = true
			return false
		}
		return true
	})
	return val, found
}

// TestReceiver_LogsSignatureFailureReason asserts the verification-failure log
// record carries the classified reason field (doc.go promise; C4/F7). Not
// parallel: it swaps the process-global slog default.
func TestReceiver_LogsSignatureFailureReason(t *testing.T) {
	h := &capturingHandler{}
	slogcapture.InstallDefault(t, slog.New(h))

	// Bad signature → ReasonBadSignature classification.
	req := signedRequest(t, []byte(`{"k":"v"}`), "d1")
	req.Header.Set("X-Signature", "v1,AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	recv := newMetricReceiver(t, testStore(t), fixedNow, kwh.Metrics{})

	recv.ServeHTTP(httptest.NewRecorder(), req)

	h.mu.Lock()
	defer h.mu.Unlock()
	var found bool
	for _, r := range h.records {
		if r.Message != "webhook receiver: signature verification failed" {
			continue
		}
		found = true
		reason, ok := attr(r, "reason")
		if !ok {
			t.Fatalf("signature-failure log is missing the 'reason' field (doc.go promises it); attrs present did not include reason")
		}
		if reason != "bad_signature" {
			t.Errorf("log reason = %q, want %q", reason, "bad_signature")
		}
	}
	if !found {
		t.Fatal("no signature-verification-failed log record captured")
	}
}
