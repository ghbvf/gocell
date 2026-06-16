package transport

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/kernel/wrapper"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/errcode/errcodetest"
)

// newReq builds a GET request with the given internal path.
func newReq(t *testing.T, path string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://in-proc"+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	return req
}

// TestInProcessTransport_DoContractBeforeBind_FailsFast asserts that calling
// DoContract on an unbound transport returns a typed errcode (not a nil-handler
// panic). In correct wiring the bind happens at bootstrap phase5 before serving,
// so this path is a misordered-wiring programmer error and must fail fast.
func TestInProcessTransport_DoContractBeforeBind_FailsFast(t *testing.T) {
	t.Parallel()

	tr := NewInProcess(nil)
	//nolint:bodyclose // the before-bind path returns a nil response (asserted below); there is no body to close.
	resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", newReq(t, "/internal/v1/config/x"))
	if resp != nil {
		t.Fatalf("expected nil response before bind, got %v", resp)
	}
	errcodetest.AssertCode(t, err, errcode.ErrInternal)
}

// TestInProcessTransport_ZeroValue_FailsFast asserts that a forged zero-value
// InProcessTransport{} (NOT minted via NewInProcess — its inner bindable
// dispatcher is nil) cannot bind a rogue handler and cannot dispatch: both Bind
// and DoContract fail-fast. This closes the bind-authority gap that an exported
// concrete with a usable zero value would open.
func TestInProcessTransport_ZeroValue_FailsFast(t *testing.T) {
	t.Parallel()

	var zero InProcessTransport // not minted; d == nil
	err := zero.Bind(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil)
	errcodetest.AssertCode(t, err, errcode.ErrInternal)

	//nolint:bodyclose // the un-minted fail-fast path returns a nil response (asserted below); no body to close.
	resp, derr := zero.DoContract(context.Background(), "c", newReq(t, "/internal/v1/config/x"))
	if resp != nil {
		t.Fatalf("expected nil response from un-minted transport, got %v", resp)
	}
	errcodetest.AssertCode(t, derr, errcode.ErrInternal)
}

// TestMetrics_Record_FailsClosedOnUnregisteredMode asserts Record never emits an
// unregistered (forged/zero) mode OR outcome, so the closed label sets cannot be
// polluted with transport_mode="unknown" / outcome="unknown".
func TestMetrics_Record_FailsClosedOnUnregisteredMode(t *testing.T) {
	t.Parallel()

	m, cp := newTestMetrics(t)
	m.Record(context.Background(), TransportMode{}, OutcomeSuccess()) // forged mode — must NOT record
	m.Record(context.Background(), ModeInProc(), TransportOutcome{})  // forged outcome — must NOT record
	m.Record(context.Background(), ModeInProc(), OutcomeSuccess())    // both registered — records

	if got := cp.count(TransportModeUnknown); got != 0 {
		t.Errorf("unregistered mode recorded %d times, want 0 (fail-closed)", got)
	}
	if got := cp.countOutcome(TransportOutcomeUnknown); got != 0 {
		t.Errorf("unregistered outcome recorded %d times, want 0 (fail-closed)", got)
	}
	if got := cp.count("in_proc"); got != 1 {
		t.Errorf("in_proc recorded %d times, want 1", got)
	}
	if got := cp.countOutcome("success"); got != 1 {
		t.Errorf("outcome=success recorded %d times, want 1", got)
	}
}

// TestInProcessTransport_Bind_WriteOnce verifies first Bind succeeds and a second
// Bind is rejected (WriteOnce), and that concurrent Bind has exactly one winner.
func TestInProcessTransport_Bind_WriteOnce(t *testing.T) {
	t.Parallel()

	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	t.Run("second bind rejected", func(t *testing.T) {
		t.Parallel()
		tr := NewInProcess(nil)
		if err := tr.Bind(h, nil); err != nil {
			t.Fatalf("first bind: unexpected error %v", err)
		}
		if err := tr.Bind(h, nil); err == nil {
			t.Fatal("second bind must be rejected (WriteOnce)")
		}
	})

	t.Run("concurrent bind single winner", func(t *testing.T) {
		t.Parallel()
		tr := NewInProcess(nil)
		const n = 16
		var wg sync.WaitGroup
		wins := make([]bool, n)
		wg.Add(n)
		for i := 0; i < n; i++ {
			go func(i int) {
				defer wg.Done()
				wins[i] = tr.Bind(h, nil) == nil
			}(i)
		}
		wg.Wait()
		won := 0
		for _, w := range wins {
			if w {
				won++
			}
		}
		if won != 1 {
			t.Errorf("exactly one concurrent Bind must win, got %d", won)
		}
	})
}

// TestInProcessTransport_Bind_NilHandler_FailsFast asserts Bind rejects a nil
// handler (a programmer error) rather than publishing a transport that would
// nil-panic on the first dispatch.
func TestInProcessTransport_Bind_NilHandler_FailsFast(t *testing.T) {
	t.Parallel()

	tr := NewInProcess(nil)
	if err := tr.Bind(nil, nil); err == nil {
		t.Fatal("Bind(nil handler) must return an error")
	}
	errcodetest.AssertCode(t, tr.Bind(nil, nil), errcode.ErrInternal)
}

// TestInProcessTransport_DoContract_5xxMarksSpanError asserts a dispatched 5xx
// marks the span StatusError + records the status attribute, so an in-process
// call is never a silently-successful span (ADR D4: transparent ≠ undiagnosable).
func TestInProcessTransport_DoContract_5xxMarksSpanError(t *testing.T) {
	t.Parallel()

	rec := &recordingTracer{}
	tr := NewInProcess(nil)
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	if err := tr.Bind(h, rec); err != nil {
		t.Fatalf("bind: %v", err)
	}

	resp, err := tr.DoContract(context.Background(), "c", newReq(t, "/internal/v1/config/k"))
	if err != nil {
		t.Fatalf("DoContract: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	status, set := rec.spanStatus()
	if !set || status != wrapper.StatusError {
		t.Errorf("span status set=%v code=%v, want StatusError on 5xx", set, status)
	}
	if !rec.hasAttr("http.status_code", int64(http.StatusBadGateway)) {
		t.Error("span must carry http.status_code=502")
	}
}

// TestInProcessTransport_DoContract_DispatchesToHandler proves the dispatched
// request reaches the bound handler in-memory WITH its headers intact (the
// transport replaces the network, not the request), and that the recorder's
// response round-trips status + body back to the caller.
func TestInProcessTransport_DoContract_DispatchesToHandler(t *testing.T) {
	t.Parallel()

	var sawAuth, sawTenant, sawPath string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawTenant = r.Header.Get("X-Tenant-ID")
		sawPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"data":{"key":"k","value":"v"}}`)
	})
	tr := NewInProcess(nil)
	if err := tr.Bind(h, nil); err != nil {
		t.Fatalf("bind: %v", err)
	}

	req := newReq(t, "/internal/v1/config/k")
	req.Header.Set("Authorization", "ServiceToken abc")
	req.Header.Set("X-Tenant-ID", "tenant-1")

	resp, err := tr.DoContract(context.Background(), "http.config.internal.get.v1", req)
	if err != nil {
		t.Fatalf("DoContract: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if sawAuth != "ServiceToken abc" {
		t.Errorf("handler saw Authorization = %q, want %q (auth chain must not be bypassed)", sawAuth, "ServiceToken abc")
	}
	if sawTenant != "tenant-1" {
		t.Errorf("handler saw X-Tenant-ID = %q, want %q", sawTenant, "tenant-1")
	}
	if sawPath != "/internal/v1/config/k" {
		t.Errorf("handler saw path = %q, want %q", sawPath, "/internal/v1/config/k")
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"value":"v"`) {
		t.Errorf("response body = %q, want it to contain the data envelope", string(body))
	}
}

// TestInProcessTransport_DoContract_PropagatesCtx asserts the DoContract ctx is
// applied to the dispatched request so cancellation / values reach the handler.
func TestInProcessTransport_DoContract_PropagatesCtx(t *testing.T) {
	t.Parallel()

	type ctxKey struct{}
	var sawVal any
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawVal = r.Context().Value(ctxKey{})
		w.WriteHeader(http.StatusOK)
	})
	tr := NewInProcess(nil)
	if err := tr.Bind(h, nil); err != nil {
		t.Fatalf("bind: %v", err)
	}

	ctx := context.WithValue(context.Background(), ctxKey{}, "carried")
	resp, err := tr.DoContract(ctx, "c", newReq(t, "/internal/v1/config/k"))
	if err != nil {
		t.Fatalf("DoContract: %v", err)
	}
	_ = resp.Body.Close()
	if sawVal != "carried" {
		t.Errorf("handler ctx value = %v, want %q (DoContract ctx must reach the handler)", sawVal, "carried")
	}
}

// TestInProcessTransport_DoContract_RecordsInProcMode asserts the in-proc call is
// observable as transport_mode=in_proc on the metric (D4: trace/metrics must
// distinguish in-proc vs remote). A recording tracer also receives the span attr.
func TestInProcessTransport_DoContract_RecordsInProcMode(t *testing.T) {
	t.Parallel()

	m, cp := newTestMetrics(t)
	rec := &recordingTracer{}
	tr := NewInProcess(m)
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	if err := tr.Bind(h, rec); err != nil {
		t.Fatalf("bind: %v", err)
	}

	resp, err := tr.DoContract(context.Background(), "c", newReq(t, "/internal/v1/config/k"))
	if err != nil {
		t.Fatalf("DoContract: %v", err)
	}
	_ = resp.Body.Close()

	if got := cp.count("in_proc"); got != 1 {
		t.Errorf("in_proc metric count = %d, want 1", got)
	}
	if !rec.hasAttr("transport_mode", "in_proc") {
		t.Errorf("span attributes missing transport_mode=in_proc")
	}
}

// --- test doubles ---

// newTestMetrics builds a *Metrics backed by a counting provider and returns
// both so a test can assert recorded label values.
func newTestMetrics(t *testing.T) (*Metrics, *countingProvider) {
	t.Helper()
	cp := &countingProvider{counts: map[string]int{}, outcomeCounts: map[string]int{}}
	m, err := NewMetrics(cp)
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	return m, cp
}

// countingProvider embeds the kernel NopProvider and overrides CounterVec to
// capture transport_mode + outcome label values.
type countingProvider struct {
	kernelmetrics.NopProvider
	mu            sync.Mutex
	counts        map[string]int // by transport_mode
	outcomeCounts map[string]int // by outcome
}

func (cp *countingProvider) CounterVec(opts kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	base, err := kernelmetrics.NopProvider{}.CounterVec(opts)
	if err != nil {
		return nil, err
	}
	return &countingCounterVec{CounterVec: base, cp: cp}, nil
}

func (cp *countingProvider) count(mode string) int {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return cp.counts[mode]
}

func (cp *countingProvider) countOutcome(outcome string) int {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return cp.outcomeCounts[outcome]
}

type countingCounterVec struct {
	kernelmetrics.CounterVec
	cp *countingProvider
}

func (c *countingCounterVec) With(l kernelmetrics.Labels) kernelmetrics.Counter {
	return &countingCounter{cp: c.cp, mode: l["transport_mode"], outcome: l["outcome"]}
}

type countingCounter struct {
	cp      *countingProvider
	mode    string
	outcome string
}

func (c *countingCounter) Inc(context.Context) {
	c.cp.mu.Lock()
	defer c.cp.mu.Unlock()
	c.cp.counts[c.mode]++
	c.cp.outcomeCounts[c.outcome]++
}
func (c *countingCounter) Add(context.Context, float64) {}

// recordingTracer is a test wrapper.Tracer capturing span attributes + status.
type recordingTracer struct {
	mu        sync.Mutex
	attrs     []wrapper.Attr
	statusSet bool
	status    wrapper.StatusCode
}

func (rt *recordingTracer) spanStatus() (wrapper.StatusCode, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.status, rt.statusSet
}

func (rt *recordingTracer) Start(ctx context.Context, _ string, attrs ...wrapper.Attr) (context.Context, wrapper.Span) {
	rt.mu.Lock()
	rt.attrs = append(rt.attrs, attrs...)
	rt.mu.Unlock()
	return ctx, &recordingSpan{rt: rt}
}

func (rt *recordingTracer) hasAttr(key string, val any) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, a := range rt.attrs {
		if a.Key == key && a.Value == val {
			return true
		}
	}
	return false
}

type recordingSpan struct{ rt *recordingTracer }

func (s *recordingSpan) SetAttributes(attrs ...wrapper.Attr) {
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	s.rt.attrs = append(s.rt.attrs, attrs...)
}
func (s *recordingSpan) RecordError(error) {}
func (s *recordingSpan) SetStatus(code wrapper.StatusCode, _ string) {
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	s.rt.statusSet = true
	s.rt.status = code
}
func (s *recordingSpan) End() {}
