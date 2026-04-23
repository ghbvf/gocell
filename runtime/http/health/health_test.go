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

func TestReadyzVerboseQueryParsing(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantVal bool
	}{
		{name: "absent", url: "/readyz", wantVal: false},
		{name: "bare flag", url: "/readyz?verbose", wantVal: true},
		{name: "empty value", url: "/readyz?verbose=", wantVal: true},
		{name: "one", url: "/readyz?verbose=1", wantVal: true},
		{name: "true", url: "/readyz?verbose=true", wantVal: true},
		{name: "TRUE mixed case", url: "/readyz?verbose=TRUE", wantVal: true},
		{name: "false", url: "/readyz?verbose=false", wantVal: false},
		{name: "zero", url: "/readyz?verbose=0", wantVal: false},
		{name: "two", url: "/readyz?verbose=2", wantVal: false},
		{name: "yes not supported", url: "/readyz?verbose=yes", wantVal: false},
		{name: "unknown not supported", url: "/readyz?verbose=debug", wantVal: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			assert.Equal(t, tt.wantVal, readyzVerbose(req))
		})
	}
}

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

func TestReadyz_VerboseToken_CorrectHeader(t *testing.T) {
	h := newStartedHandler(t)
	h.SetVerboseToken("secret-token")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	req.Header.Set("X-Readyz-Token", "secret-token")
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	_, hasCells := body["cells"]
	assert.True(t, hasCells, "correct token should expose verbose details")
}

func TestReadyz_VerboseToken_WrongHeader(t *testing.T) {
	h := newStartedHandler(t)
	h.SetVerboseToken("secret-token")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	req.Header.Set("X-Readyz-Token", "wrong")
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	_, hasCells := body["cells"]
	assert.False(t, hasCells, "wrong token should suppress verbose details")
}

func TestReadyz_VerboseToken_MissingHeader(t *testing.T) {
	h := newStartedHandler(t)
	h.SetVerboseToken("secret-token")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	// No X-Readyz-Token header.
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	_, hasCells := body["cells"]
	assert.False(t, hasCells, "missing token should suppress verbose details")
}

func TestReadyz_VerboseToken_NotConfigured(t *testing.T) {
	h := newStartedHandler(t)
	// No SetVerboseToken call — backward compatible.

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	_, hasCells := body["cells"]
	assert.True(t, hasCells, "no token configured should allow verbose (backward compat)")
}

func TestReadyz_VerboseToken_ResetToEmpty(t *testing.T) {
	// Setting a token then resetting to empty must restore backward-compat
	// behavior (verbose allowed unconditionally). Guards against a future
	// regression where SetVerboseToken treats "" as a no-op.
	h := newStartedHandler(t)
	h.SetVerboseToken("secret-token")
	h.SetVerboseToken("")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	// No X-Readyz-Token header — token was cleared.
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	_, hasCells := body["cells"]
	assert.True(t, hasCells, "empty token after reset should restore backward-compat verbose")
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

// TestReadyz_ParallelFasterThanSerial verifies that /readyz runs checkers in
// parallel. With 3 checkers that each sleep 100 ms, the total wall-clock time
// must be well below 300 ms (serial cost).
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
	// Tolerance 250ms (serial = ~300ms): CI scheduler jitter + errgroup bookkeeping can add 30-50ms over theoretical 100ms.
	assert.Less(t, elapsed, 250*time.Millisecond,
		"3 parallel 100-ms probes must finish in < 250ms (serial would be ~300ms); got %v", elapsed)
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
