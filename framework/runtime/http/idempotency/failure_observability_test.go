package idempotency

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/pkg/testutil/slogcapture"
	"github.com/ghbvf/gocell/framework/pkg/testutil/sloghelper"
)

// errReader is a minimal io.Reader that always fails on Read, used to simulate a
// network/IO error during request body reading (mirrors runtime/webhook
// receiver_test.go).
type errReader struct{ err error }

func (e errReader) Read(_ []byte) (int, error) { return 0, e.err }

// failingBodyWriter wraps a ResponseWriter but fails every Write, used to drive
// the replay-body write-failure path. Header()/WriteHeader delegate to the
// embedded writer so status/headers are still observable.
type failingBodyWriter struct {
	http.ResponseWriter
	writeErr error
}

func (f *failingBodyWriter) Write(_ []byte) (int, error) { return 0, f.writeErr }

// captureSlog redirects the process-global default logger (the middleware logs
// via package-level slog.ErrorContext) to a JSON buffer for the duration of t.
// It goes through slogcapture.InstallDefault — the sole sanctioned global-default
// redirect site (SLOG-CAPTURE-GLOBAL-FUNNEL-01) — and uses a SyncBuffer so reads stay
// race-safe. Tests using it MUST stay serial (no t.Parallel) per InstallDefault's
// contract: a global redirect would race parallel siblings (#1490).
func captureSlog(t *testing.T) *sloghelper.SyncBuffer {
	t.Helper()
	buf := sloghelper.NewSyncBuffer()
	slogcapture.InstallDefault(t, slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return buf
}

// TestServeHTTP_BodyReadError_Returns503AndLogs covers issue #2015: a request
// body read failure must return 503 ERR_SERVICE_UNAVAILABLE (transient, mirrors
// the webhook receiver) instead of 500 ERR_INTERNAL, and emit a correlated slog
// entry — previously this path had no logging at all and returned the wrong
// (server-fault) status. The custom msgBodyReadFailed surfaces only server-side
// (5xx strips the message to a generic "service unavailable" on the wire).
func TestServeHTTP_BodyReadError_Returns503AndLogs(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	obs := &recordingObserver{}
	mw := Middleware(clk, ms, WithMetrics(obs))
	buf := captureSlog(t)

	handler := mw(testHandler(200, "should-not-run"))
	r := requestWithUserCtx("POST", "/orders", "body-err-key", "tenant1", "user1")
	r.Body = io.NopCloser(errReader{err: errors.New("simulated network read error")})

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", rr.Code, rr.Body.String())
	}

	if len(obs.states) != 1 || obs.states[0] != StateBodyReadFailed {
		t.Errorf("metric states: got %v, want [StateBodyReadFailed]", obs.states)
	}

	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal response: %v; body: %s", err, rr.Body.String())
	}
	if got := envelope.Error.Code; got != "ERR_SERVICE_UNAVAILABLE" {
		t.Errorf("code = %q, want ERR_SERVICE_UNAVAILABLE (transient, not server-fault ERR_INTERNAL)", got)
	}

	entry := sloghelper.FindLogEntry(buf.String(), "idempotency: read body failed")
	if entry == nil {
		t.Fatalf("expected a slog entry for the body-read failure; got none.\nlog: %s", buf.String())
	}
	for _, field := range []string{"err", "idempotency_key_hash", "subject", "tenant_id"} {
		if _, ok := entry[field]; !ok {
			t.Errorf("body-read failure log missing correlation field %q; entry=%v", field, entry)
		}
	}
}

// TestReplay_BodyWriteError_LogsWithCorrelation covers issue #2015: when writing
// a replayed response body fails, the log must use slog.ErrorContext and carry
// the idempotency correlation fields — previously it used a context-less
// slog.Error with only an "error" field.
func TestReplay_BodyWriteError_LogsWithCorrelation(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":"order-1"}`)
	})
	handler := mw(inner)

	// First request records the response.
	r1 := requestWithUserCtx("POST", "/orders", "replay-key", "tenant1", "user1")
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, r1)
	if rr1.Code != 201 {
		t.Fatalf("first request status = %d, want 201", rr1.Code)
	}

	// Second request replays; the response body write fails.
	buf := captureSlog(t)
	r2 := requestWithUserCtx("POST", "/orders", "replay-key", "tenant1", "user1")
	fw := &failingBodyWriter{ResponseWriter: httptest.NewRecorder(), writeErr: errors.New("client gone")}
	handler.ServeHTTP(fw, r2)

	if rr := fw.ResponseWriter.(*httptest.ResponseRecorder); rr.Header().Get(headerIdempotencyReplayed) != "true" {
		t.Fatalf("expected replay (Idempotency-Replayed: true); headers=%v", rr.Header())
	}

	entry := sloghelper.FindLogEntry(buf.String(), "idempotency: replay response body write failed")
	if entry == nil {
		t.Fatalf("expected a slog entry for the replay-write failure; got none.\nlog: %s", buf.String())
	}
	for _, field := range []string{"err", "idempotency_key_hash", "subject", "tenant_id"} {
		if _, ok := entry[field]; !ok {
			t.Errorf("replay-write failure log missing correlation field %q; entry=%v", field, entry)
		}
	}
}
