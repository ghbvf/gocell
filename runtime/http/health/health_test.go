package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubCell is a minimal Cell implementation for testing.
type stubCell struct {
	*cell.BaseCell
}

func newStubCell(id string) *stubCell {
	return &stubCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:   id,
			Type: cell.CellTypeCore,
		}),
	}
}

func TestLivezHandler(t *testing.T) {
	tests := []struct {
		name       string
		startCells bool
		wantStatus int
		wantBody   string
	}{
		{
			name:       "all healthy",
			startCells: true,
			wantStatus: http.StatusOK,
			wantBody:   "healthy",
		},
		{
			name:       "healthy when not started",
			startCells: false,
			wantStatus: http.StatusOK,
			wantBody:   "healthy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
			c := newStubCell("cell-1")
			require.NoError(t, asm.Register(c))

			if tt.startCells {
				require.NoError(t, asm.Start(context.Background()))
				defer func() { _ = asm.Stop(context.Background()) }()
			}

			h := New(asm)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			h.LivezHandler().ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)

			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, tt.wantBody, body["status"])
			_, hasChecks := body["checks"]
			assert.False(t, hasChecks, "/healthz must not expose readiness details")
		})
	}
}

func TestReadyzHandler(t *testing.T) {
	tests := []struct {
		name         string
		startCells   bool
		checkerErr   error
		wantStatus   int
		wantBodyStat string
	}{
		{
			name:         "healthy with passing checker",
			startCells:   true,
			checkerErr:   nil,
			wantStatus:   http.StatusOK,
			wantBodyStat: "healthy",
		},
		{
			name:         "unhealthy when checker fails",
			startCells:   true,
			checkerErr:   fmt.Errorf("db unreachable"),
			wantStatus:   http.StatusServiceUnavailable,
			wantBodyStat: "unhealthy",
		},
		{
			name:         "unhealthy when cell not started",
			startCells:   false,
			checkerErr:   nil,
			wantStatus:   http.StatusServiceUnavailable,
			wantBodyStat: "unhealthy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
			c := newStubCell("cell-1")
			require.NoError(t, asm.Register(c))

			if tt.startCells {
				require.NoError(t, asm.Start(context.Background()))
				defer func() { _ = asm.Stop(context.Background()) }()
			}

			h := New(asm)
			h.RegisterChecker("db", func(_ context.Context) error { return tt.checkerErr })

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
			h.ReadyzHandler().ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)

			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, tt.wantBodyStat, body["status"])

			// Verify namespace separation: cells and dependencies are in distinct maps.
			cells, ok := body["cells"].(map[string]any)
			require.True(t, ok, "response must contain cells map")
			_, hasCellCheck := cells["cell-1"]
			assert.True(t, hasCellCheck, "should include cell-1 in cells")

			deps, ok := body["dependencies"].(map[string]any)
			require.True(t, ok, "response must contain dependencies map")
			_, hasDBCheck := deps["db"]
			assert.True(t, hasDBCheck, "should include db in dependencies")
		})
	}
}

func TestReadyzHandler_MultipleCheckers(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm)
	h.RegisterChecker("rabbitmq", func(_ context.Context) error { return nil })
	h.RegisterChecker("postgres", func(_ context.Context) error { return fmt.Errorf("connection refused") })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "unhealthy", body["status"])

	deps, ok := body["dependencies"].(map[string]any)
	require.True(t, ok, "response must contain dependencies map")
	// Dependencies are now map[string]map[string]any
	rabbitmqEntry, ok := deps["rabbitmq"].(map[string]any)
	require.True(t, ok, "rabbitmq entry must be a map")
	assert.Equal(t, "healthy", rabbitmqEntry["status"], "rabbitmq checker should be healthy")

	postgresEntry, ok := deps["postgres"].(map[string]any)
	require.True(t, ok, "postgres entry must be a map")
	assert.Equal(t, "unhealthy", postgresEntry["status"], "postgres checker should be unhealthy")
}

func TestLivezHandler_IsProcessLivenessOnly(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))

	h := New(asm)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	h.LivezHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "healthy", body["status"])
	_, hasChecks := body["checks"]
	assert.False(t, hasChecks, "/healthz must not expose readiness details")
	_, hasCells := body["cells"]
	assert.False(t, hasCells, "/healthz must not expose cell readiness details")
	_, hasDependencies := body["dependencies"]
	assert.False(t, hasDependencies, "/healthz must not expose dependency readiness details")
}

func TestReadyzHandler_DefaultOutputIsAggregateOnly(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm)
	h.RegisterChecker("db", func(_ context.Context) error { return nil })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "healthy", body["status"])
	_, hasCells := body["cells"]
	assert.False(t, hasCells, "default /readyz output must not expose cells")
	_, hasDependencies := body["dependencies"]
	assert.False(t, hasDependencies, "default /readyz output must not expose dependencies")
}

func TestReadyzHandler_VerboseOutputIncludesDetails(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm)
	h.RegisterChecker("db", func(_ context.Context) error { return nil })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "healthy", body["status"])
	cells, ok := body["cells"].(map[string]any)
	require.True(t, ok, "verbose readyz output must contain cells")
	assert.Equal(t, "healthy", cells["cell-1"])
	deps, ok := body["dependencies"].(map[string]any)
	require.True(t, ok, "verbose readyz output must contain dependencies")
	dbEntry, ok := deps["db"].(map[string]any)
	require.True(t, ok, "db dependency must be a map")
	assert.Equal(t, "healthy", dbEntry["status"])
}

func TestReadyzHandler_VerboseOutput_IncludesAdapterInfo(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm)
	h.SetAdapterInfo(map[string]string{
		"mode":    "in-memory",
		"storage": "in-memory",
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	adapters, ok := body["adapters"].(map[string]any)
	require.True(t, ok, "verbose readyz output must contain adapters")
	assert.Equal(t, "in-memory", adapters["mode"])
	assert.Equal(t, "in-memory", adapters["storage"])
}

func TestReadyzHandler_VerboseOutput_OmitsAdapterInfo_WhenNotSet(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm)
	// No SetAdapterInfo call.

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	_, hasAdapters := body["adapters"]
	assert.False(t, hasAdapters, "verbose readyz output should not contain adapters when not set")
}

func TestReadyzHandler_DefaultOutput_UnhealthyAggregate(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm)
	h.RegisterChecker("db", func(_ context.Context) error { return fmt.Errorf("connection refused") })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "unhealthy", body["status"])
	_, hasCells := body["cells"]
	assert.False(t, hasCells, "non-verbose unhealthy /readyz must not expose cells")
	_, hasDependencies := body["dependencies"]
	assert.False(t, hasDependencies, "non-verbose unhealthy /readyz must not expose dependencies")
}

// ?verbose query parameter parsing is exercised via probequery.Verbose; see
// runtime/http/health/probequery/verbose_test.go for the canonical table-test.

func TestRegisterChecker_DuplicatePanics(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
	h := New(asm)
	h.RegisterChecker("db", func(_ context.Context) error { return nil })

	assert.PanicsWithValue(t, `health: duplicate checker name "db"`, func() {
		h.RegisterChecker("db", func(_ context.Context) error { return nil })
	})
}

func TestReadyz_ShuttingDown_Returns503(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm)

	// Before shutdown: should be healthy.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Mark shutting down.
	h.SetShuttingDown()

	// After shutdown: should be 503.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "shutting_down")
}

func TestSetShuttingDown_Idempotent(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm)
	h.SetShuttingDown()
	h.SetShuttingDown() // second call must not panic

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// --- Verbose token protection (READYZ-VERBOSE-TOKEN-01) ---

func newStartedHandler(t *testing.T) *Handler {
	t.Helper()
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })
	h := New(asm)
	h.RegisterChecker("db", func(_ context.Context) error { return nil })
	return h
}

// Verbose-mode access control (token gating) lives in
// runtime/bootstrap.PolicyVerboseToken, attached as middleware to the readyz
// cell.RouteGroup. The handler itself only honours the ?verbose query param;
// see TestReadyz_VerboseQueryParam* below for query-parsing coverage.

func TestReadyz_VerboseQueryParam_RendersVerboseBody(t *testing.T) {
	h := newStartedHandler(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	_, hasCells := body["cells"]
	assert.True(t, hasCells, "verbose=true must render the verbose cells block")
}

func TestReadyz_VerboseQueryParam_AbsentHidesVerboseBody(t *testing.T) {
	h := newStartedHandler(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	_, hasCells := body["cells"]
	assert.False(t, hasCells, "no ?verbose query must hide the verbose cells block")
}

func TestEmptyAssembly(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "empty", DurabilityMode: cell.DurabilityDemo})
	h := New(asm)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	h.LivezHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "healthy", body["status"])
}

// --- New parallel / deadline / panic tests (PR-A4 phase 5a) ---

// serialBaseline is the expected wall-clock time for running 3 probes of
// 100 ms each sequentially. Used to bound the parallelism semantic assertion.
const serialBaseline = 300 * time.Millisecond

// TestReadyz_ParallelFasterThanSerial verifies that /readyz runs checkers in
// parallel. With 3 checkers that each sleep 100 ms, the total wall-clock time
// must be well below 300 ms (serial cost).
//
// Two assertions bound the parallelism invariant from both sides:
//   - semantic:     parallel must be meaningfully faster than serial (>50ms faster)
//   - performance:  parallel must be < 250ms on typical CI hardware
//
// If CI scheduler jitter makes the second assertion flaky, fall back to semantic-only
// by wrapping in testing.Short() (or increase tolerance) — don't remove the semantic check.
func TestReadyz_ParallelFasterThanSerial(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-parallel", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	// Use a generous deadline so these tests do not time out.
	h := New(asm, WithDeadline(2*time.Second))
	for _, name := range []string{"probe-a", "probe-b", "probe-c"} {
		h.RegisterChecker(name, func(_ context.Context) error {
			time.Sleep(100 * time.Millisecond)
			return nil
		})
	}

	start := time.Now()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)
	elapsed := time.Since(start)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Semantic assertion: parallel execution must be at least 50ms faster than
	// serial. This proves parallelism actually occurred, independent of absolute
	// timing. This check must never be removed.
	assert.Less(t, elapsed, serialBaseline-50*time.Millisecond,
		"3 parallel 100-ms probes must be at least 50ms faster than serial (%v); got %v", serialBaseline, elapsed)

	// Performance assertion: absolute upper bound on typical CI hardware.
	// If this flaps on resource-constrained CI, wrap in testing.Short() to
	// skip it in short mode — but keep the semantic assertion above.
	if !testing.Short() {
		assert.Less(t, elapsed, 250*time.Millisecond,
			"3 parallel 100-ms probes must finish in < 250ms (serial would be ~300ms); got %v", elapsed)
	}
}

// TestReadyz_DeadlineExceeded verifies that a probe which exceeds the deadline
// is reported as status="timeout" with an error containing "deadline exceeded",
// and the aggregate returns 503.
func TestReadyz_DeadlineExceeded(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-deadline", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, WithDeadline(50*time.Millisecond))
	h.RegisterChecker("slow", func(ctx context.Context) error {
		select {
		case <-time.After(500 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "unhealthy", body["status"])

	deps, ok := body["dependencies"].(map[string]any)
	require.True(t, ok, "verbose output must contain dependencies")
	slowEntry, ok := deps["slow"].(map[string]any)
	require.True(t, ok, "slow entry must be a map")
	assert.Equal(t, "timeout", slowEntry["status"], "exceeded-deadline probe must be status=timeout")
	errStr, hasErr := slowEntry["error"].(string)
	require.True(t, hasErr, "timeout probe must include error field")
	assert.Contains(t, errStr, "deadline exceeded",
		"error field must mention 'deadline exceeded'")
}

// TestReadyz_IndependentOfRequestCtx verifies that /readyz probes are NOT
// cancelled when the HTTP request context is cancelled (e.g. kubelet disconnect).
// The probe ctx must derive from context.Background(), not r.Context().
func TestReadyz_IndependentOfRequestCtx(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-indep", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	probeDone := make(chan struct{})
	h := New(asm, WithDeadline(2*time.Second))
	h.RegisterChecker("slow-probe", func(ctx context.Context) error {
		// Probe takes 100 ms but the HTTP request ctx will be cancelled
		// almost immediately — probe must NOT be affected.
		time.Sleep(100 * time.Millisecond)
		close(probeDone)
		return nil
	})

	// Use a cancellable request ctx and cancel it before the probe finishes.
	reqCtx, reqCancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil).WithContext(reqCtx)
	rec := httptest.NewRecorder()

	// Cancel request ctx after a very short time (before probe finishes).
	go func() {
		time.Sleep(10 * time.Millisecond)
		reqCancel()
	}()

	h.ReadyzHandler().ServeHTTP(rec, req)

	// Probe must still complete even though the request ctx was cancelled.
	select {
	case <-probeDone:
		// expected
	case <-time.After(500 * time.Millisecond):
		t.Fatal("probe was cancelled by request ctx; must use background ctx")
	}
	// Aggregate result must be healthy (probe returned nil after sleeping).
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestReadyz_ProbePanic_Caught verifies that a panic inside a checker is
// recovered, the checker reports status=unhealthy, and the HTTP handler
// does not crash.
func TestReadyz_ProbePanic_Caught(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-panic", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, WithDeadline(2*time.Second))
	h.RegisterChecker("panicking", func(_ context.Context) error {
		panic("something went very wrong")
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)

	// Must not crash the process.
	require.NotPanics(t, func() {
		h.ReadyzHandler().ServeHTTP(rec, req)
	})

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "unhealthy", body["status"])

	deps, ok := body["dependencies"].(map[string]any)
	require.True(t, ok, "verbose output must contain dependencies")
	panicEntry, ok := deps["panicking"].(map[string]any)
	require.True(t, ok, "panicking entry must be present")
	assert.Equal(t, "unhealthy", panicEntry["status"])
}

// TestTruncateErrMsg verifies the truncateErrMsg helper across boundary cases.
//
// Known limitation: truncation is byte-based (msg[:max]), not rune-based.
// A multi-byte UTF-8 sequence that straddles the 512-byte boundary will be
// split mid-rune, producing an invalid UTF-8 suffix before "...". This is
// an accepted trade-off (bounds response size without extra allocation) and
// is documented here rather than fixed, since the use-site is diagnostic
// output in /readyz?verbose where operators can tolerate mojibake in edge cases.
func TestTruncateErrMsg(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		max     int
		want    string
		wantLen int // expected len(result); -1 means check want exactly
	}{
		{
			name:  "empty string",
			input: "",
			max:   512,
			want:  "",
		},
		{
			name:  "short string below limit",
			input: "connection refused",
			max:   512,
			want:  "connection refused",
		},
		{
			name:    "exactly at limit — no truncation",
			input:   string(make([]byte, 512)),
			max:     512,
			wantLen: 512,
		},
		{
			name:    "one byte over limit — truncated with ellipsis",
			input:   string(make([]byte, 513)),
			max:     512,
			wantLen: 515, // 512 + len("...")
		},
		{
			name:    "long string well over limit",
			input:   string(make([]byte, 1024)),
			max:     512,
			wantLen: 515,
		},
		{
			name:  "truncated suffix is '...'",
			input: "abcdefghij",
			max:   5,
			want:  "abcde...",
		},
		{
			name: "multi-byte UTF-8 within limit — no truncation",
			// "日本語" is 9 bytes (3 bytes per rune); 9 < 512 so no truncation.
			input: "日本語",
			max:   512,
			want:  "日本語",
		},
		{
			name: "multi-byte UTF-8 split at byte boundary — known limitation",
			// 4-byte rune: U+1F600 (😀) = 0xF0 0x9F 0x98 0x80
			// max=3 splits the rune, producing invalid UTF-8 + "..."
			// We only assert the suffix and length, not valid UTF-8.
			input:   "😀extra",
			max:     3,
			wantLen: 6, // 3 bytes from the rune + 3 bytes "..."
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateErrMsg(tt.input, tt.max)
			if tt.want != "" || (tt.want == "" && tt.wantLen == 0 && tt.input == "") {
				// Exact match cases
				assert.Equal(t, tt.want, got)
			}
			if tt.wantLen > 0 {
				assert.Equal(t, tt.wantLen, len(got),
					"expected len=%d, got len=%d (value=%q)", tt.wantLen, len(got), got)
			}
			// Truncated results must end with "..."
			if len(tt.input) > tt.max {
				assert.True(t, len(got) > 3 && got[len(got)-3:] == "...",
					"truncated result must end with '...'; got %q", got)
			}
		})
	}
}

// TestReadyz_VerboseError_LongErrTruncated is an end-to-end HTTP test that
// verifies truncateErrMsg is applied to probe errors in /readyz?verbose output.
// A checker returning a 600-byte error message must produce an "error" field
// in the JSON response that is at most 515 bytes (512 + "...") and ends with "...".
func TestReadyz_VerboseError_LongErrTruncated(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-truncate", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	// Construct a 600-byte error message — well over the 512-byte limit.
	longMsg := string(make([]byte, 600))
	for i := range []byte(longMsg) {
		longMsg = longMsg[:i] + "x" + longMsg[i+1:]
	}
	longMsg = fmt.Sprintf("%0600d", 0) // 600 ASCII digits

	h := New(asm)
	h.RegisterChecker("noisy", func(_ context.Context) error {
		return fmt.Errorf("%s", longMsg)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	deps, ok := body["dependencies"].(map[string]any)
	require.True(t, ok, "verbose output must contain dependencies")
	noisyEntry, ok := deps["noisy"].(map[string]any)
	require.True(t, ok, "noisy entry must be present")

	errField, ok := noisyEntry["error"].(string)
	require.True(t, ok, "error field must be a string")

	const maxWithEllipsis = maxVerboseErrLen + 3 // 512 + len("...")
	assert.LessOrEqual(t, len(errField), maxWithEllipsis,
		"error field must be at most %d bytes; got %d", maxWithEllipsis, len(errField))
	assert.True(t, len(errField) >= 3 && errField[len(errField)-3:] == "...",
		"truncated error must end with '...'; got %q", errField)
}

// TestReadyz_UncooperativeChecker_StillReturnsWithinDeadline covers the
// aggregator-level hard deadline: a probe that ignores ctx.Done must NOT
// block /readyz beyond h.deadline. The probe's goroutine continues running
// in the background (known trade-off, documented in runProbesParallel).
func TestReadyz_UncooperativeChecker_StillReturnsWithinDeadline(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-uncooperative", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, WithDeadline(80*time.Millisecond))

	// Checker that blocks indefinitely while completely ignoring ctx.
	// The cleanup closes the channel so the leaked goroutine can exit when
	// the test ends, avoiding goroutine leaks across test runs.
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	h.RegisterChecker("stuck", func(_ context.Context) error {
		<-unblock
		return nil
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose", nil)

	start := time.Now()
	h.ReadyzHandler()(rr, req)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 200*time.Millisecond,
		"handler must return within ~deadline (80ms) even with uncooperative probe; got %v", elapsed)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)

	var body struct {
		Status       string                    `json:"status"`
		Dependencies map[string]map[string]any `json:"dependencies"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(t, "unhealthy", body.Status)
	stuck, ok := body.Dependencies["stuck"]
	require.True(t, ok, "stuck probe must appear in verbose output")
	assert.Equal(t, "timeout", stuck["status"])
}

// TestReadyz_VerboseDependencies_StructuredOutput verifies the new structured
// dependency format: each entry is a map with "status", "duration_ms" fields
// (and optionally "error" for non-healthy probes).
func TestReadyz_VerboseDependencies_StructuredOutput(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-structured", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm)
	h.RegisterChecker("ok-probe", func(_ context.Context) error { return nil })
	h.RegisterChecker("fail-probe", func(_ context.Context) error { return fmt.Errorf("disk full") })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	deps, ok := body["dependencies"].(map[string]any)
	require.True(t, ok)

	okEntry, ok := deps["ok-probe"].(map[string]any)
	require.True(t, ok, "ok-probe must be a structured map")
	assert.Equal(t, "healthy", okEntry["status"])
	_, hasDur := okEntry["duration_ms"]
	assert.True(t, hasDur, "duration_ms must be present")
	_, hasErr := okEntry["error"]
	assert.False(t, hasErr, "healthy probe must not have error field")

	failEntry, ok := deps["fail-probe"].(map[string]any)
	require.True(t, ok, "fail-probe must be a structured map")
	assert.Equal(t, "unhealthy", failEntry["status"])
	errStr, hasErr := failEntry["error"].(string)
	assert.True(t, hasErr, "unhealthy probe must include error field")
	assert.Contains(t, errStr, "disk full")
}
