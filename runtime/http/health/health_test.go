package health

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// captureHandler records every slog event passed to it so tests can assert
// on the verbose breakdown that K#08 5xx redaction keeps off the wire.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// snapshot returns a defensive copy of recorded events.
func (h *captureHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]slog.Record, len(h.records))
	copy(out, h.records)
	return out
}

// withSlogCapture redirects slog.Default for the duration of the test and
// returns a handle the test can query to assert on captured events.
func withSlogCapture(t *testing.T) *captureHandler {
	t.Helper()
	h := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

// readyzUnhealthyDeps fetches the verbose-breakdown dependencies map from
// the captured "readyz unhealthy" slog records. Tests assert on this rather
// than on the 503 wire body because:
//   - K#08 5xx redaction strips Details from the public envelope.
//   - PR391-HEALTH-VERBOSE-REDACTION-01 / ADR 202605171200 forbid error text
//     on wire entirely; full (redacted) error lives only in slog channel d.
//
// Multiple "readyz unhealthy" records may accumulate (e.g. when a test
// polls /readyz non-verbose before issuing the verbose request). Non-verbose
// records carry only status/reason; verbose records add cells/dependencies/
// adapters. We return the first record whose dependencies attr is non-nil.
//
// Return type is map[string]slogDependencyEntry (typed) — the slog payload
// shape is owned by readyzResult.logUnhealthy, not arbitrary map[string]any.
func readyzUnhealthyDeps(t *testing.T, capture *captureHandler) map[string]slogDependencyEntry {
	t.Helper()
	const (
		recMsg  = "readyz unhealthy"
		attrKey = "dependencies"
	)
	for _, r := range capture.snapshot() {
		if r.Message != recMsg {
			continue
		}
		var depsAttr slog.Value
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == attrKey {
				depsAttr = a.Value
				return false
			}
			return true
		})
		deps, ok := depsAttr.Any().(map[string]slogDependencyEntry)
		if ok && deps != nil {
			return deps
		}
	}
	t.Fatalf("no verbose %q slog record with non-nil typed %q map; capture had %d records",
		recMsg, attrKey, len(capture.snapshot()))
	return nil
}

// stubCell is a minimal Cell implementation for testing.
type stubCell struct {
	*cell.BaseCell
}

func newStubCell(id string) *stubCell {
	return &stubCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{
			ID:   id,
			Type: "core",
		}),
	}
}

// testVerboseToken is the canonical token used across tests that exercise
// /readyz?verbose. PR-A35 requires a matching token for every verbose
// request, so the old "no SetVerboseToken call" shorthand now produces 401.
const testVerboseToken = "unit-test-token"

// newVerboseRequest builds an *http.Request for the verbose endpoint with
// the canonical test token wired into the header. Tests using verbose
// output should also call h.SetVerboseToken(testVerboseToken).
func newVerboseRequest(url string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set(VerboseAuthHeader, testVerboseToken)
	return req
}

// dataBody unwraps a success envelope `{"data": {...}}` and returns the
// inner payload. Asserts on t rather than returning an error so call sites
// stay short.
func dataBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "response body must be valid JSON")
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, "success response must carry {\"data\":...} envelope; got %s", rec.Body.String())
	return data
}

// errorBody unwraps an error envelope `{"error": {"code":..., ...}}` and
// returns the inner object.
func errorBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "response body must be valid JSON")
	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok, "error response must carry {\"error\":...} envelope; got %s", rec.Body.String())
	return errObj
}

// assertReadyzServiceUnavailable asserts the canonical errcode 503 envelope:
// code/message match the framework public sentinel and details is the empty
// array form (K#08 strips 5xx details to keep server-side context off the
// wire). Verbose breakdown — cells/dependencies/adapters and the readiness
// reason — is emitted to slog instead; tests that need to verify it install
// withSlogCapture and read the "readyz unhealthy" record.
func assertReadyzServiceUnavailable(t *testing.T, errObj map[string]any) {
	t.Helper()
	assert.Equal(t, string(errcode.ErrServiceUnavailable), errObj["code"])
	assert.Equal(t, "service unavailable", errObj["message"])
	details, ok := errObj["details"].([]any)
	require.True(t, ok, "readyz 503 response must carry the canonical details array (errcode 5xx strip): got %v", errObj["details"])
	assert.Empty(t, details, "5xx details must be empty per K#08 redaction policy; verbose breakdown lives in slog")
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
			asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
			c := newStubCell("cell-1")
			require.NoError(t, asm.Register(c))

			if tt.startCells {
				require.NoError(t, asm.Start(context.Background()))
				defer func() { _ = asm.Stop(context.Background()) }()
			}

			h := New(asm, clock.Real())
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			h.LivezHandler().ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)

			data := dataBody(t, rec)
			assert.Equal(t, tt.wantBody, data["status"])
			_, hasChecks := data["checks"]
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
			asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
			c := newStubCell("cell-1")
			require.NoError(t, asm.Register(c))

			if tt.startCells {
				require.NoError(t, asm.Start(context.Background()))
				defer func() { _ = asm.Stop(context.Background()) }()
			}

			h := New(asm, clock.Real())
			h.SetVerboseToken(testVerboseToken)
			require.NoError(t, h.RegisterChecker("db", func(_ context.Context) error { return tt.checkerErr }))

			rec := httptest.NewRecorder()
			req := newVerboseRequest("/readyz?verbose=true")
			h.ReadyzHandler().ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)

			// 200 path keeps verbose breakdown in the data envelope; 503 path
			// only carries the canonical errcode envelope (K#08 5xx strip).
			if tt.wantBodyStat == "healthy" {
				payload := dataBody(t, rec)
				assert.Equal(t, tt.wantBodyStat, payload["status"])
				cells, ok := payload["cells"].(map[string]any)
				require.True(t, ok, "verbose 200 must contain cells map")
				_, hasCellCheck := cells["cell-1"]
				assert.True(t, hasCellCheck, "should include cell-1 in cells")
				deps, ok := payload["dependencies"].(map[string]any)
				require.True(t, ok, "verbose 200 must contain dependencies map")
				_, hasDBCheck := deps["db"]
				assert.True(t, hasDBCheck, "should include db in dependencies")
			} else {
				errObj := errorBody(t, rec)
				assertReadyzServiceUnavailable(t, errObj)
			}
		})
	}
}

func TestReadyzHandler_MultipleCheckers(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, clock.Real())
	h.SetVerboseToken(testVerboseToken)
	require.NoError(t, h.RegisterChecker("rabbitmq", func(_ context.Context) error { return nil }))
	require.NoError(t, h.RegisterChecker("postgres", func(_ context.Context) error { return fmt.Errorf("connection refused") }))

	capture := withSlogCapture(t)
	rec := httptest.NewRecorder()
	req := newVerboseRequest("/readyz?verbose")
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	errObj := errorBody(t, rec)
	assertReadyzServiceUnavailable(t, errObj)

	deps := readyzUnhealthyDeps(t, capture)
	rabbitmqEntry, ok := deps["rabbitmq"]
	require.True(t, ok, "rabbitmq entry must be present")
	assert.Equal(t, "healthy", rabbitmqEntry.Status, "rabbitmq checker should be healthy")
	postgresEntry, ok := deps["postgres"]
	require.True(t, ok, "postgres entry must be present")
	assert.Equal(t, "unhealthy", postgresEntry.Status, "postgres checker should be unhealthy")
}

func TestLivezHandler_IsProcessLivenessOnly(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))

	h := New(asm, clock.Real())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	h.LivezHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	data := dataBody(t, rec)
	assert.Equal(t, "healthy", data["status"])
	_, hasChecks := data["checks"]
	assert.False(t, hasChecks, "/healthz must not expose readiness details")
	_, hasCells := data["cells"]
	assert.False(t, hasCells, "/healthz must not expose cell readiness details")
	_, hasDependencies := data["dependencies"]
	assert.False(t, hasDependencies, "/healthz must not expose dependency readiness details")
}

func TestReadyzHandler_DefaultOutputIsAggregateOnly(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, clock.Real())
	require.NoError(t, h.RegisterChecker("db", func(_ context.Context) error { return nil }))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	data := dataBody(t, rec)
	assert.Equal(t, "healthy", data["status"])
	_, hasCells := data["cells"]
	assert.False(t, hasCells, "default /readyz output must not expose cells")
	_, hasDependencies := data["dependencies"]
	assert.False(t, hasDependencies, "default /readyz output must not expose dependencies")
}

func TestReadyzHandler_VerboseOutputIncludesDetails(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, clock.Real())
	h.SetVerboseToken(testVerboseToken)
	require.NoError(t, h.RegisterChecker("db", func(_ context.Context) error { return nil }))

	rec := httptest.NewRecorder()
	req := newVerboseRequest("/readyz?verbose=true")
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	data := dataBody(t, rec)
	assert.Equal(t, "healthy", data["status"])
	cells, ok := data["cells"].(map[string]any)
	require.True(t, ok, "verbose readyz output must contain cells")
	assert.Equal(t, "healthy", cells["cell-1"])
	deps, ok := data["dependencies"].(map[string]any)
	require.True(t, ok, "verbose readyz output must contain dependencies")
	dbEntry, ok := deps["db"].(map[string]any)
	require.True(t, ok, "db dependency must be a map")
	assert.Equal(t, "healthy", dbEntry["status"])
	// Wire shape is frozen to {status, duration_ms} per ADR 202605171200 §3
	// (channel a vs d separation); no error_msg / error key on wire.
	_, hasErr := dbEntry["error"]
	assert.False(t, hasErr, "wire dependency entry must not carry error key (channel d slog only)")
	_, hasErrMsg := dbEntry["error_msg"]
	assert.False(t, hasErrMsg, "wire dependency entry must not carry error_msg key (channel d slog only)")
}

func TestReadyzHandler_VerboseOutput_IncludesAdapterInfo(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, clock.Real())
	h.SetVerboseToken(testVerboseToken)
	h.SetAdapterInfo(map[string]string{
		"mode":    "in-memory",
		"storage": "in-memory",
	})

	rec := httptest.NewRecorder()
	req := newVerboseRequest("/readyz?verbose=true")
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	data := dataBody(t, rec)
	adapters, ok := data["adapters"].(map[string]any)
	require.True(t, ok, "verbose readyz output must contain adapters")
	assert.Equal(t, "in-memory", adapters["mode"])
	assert.Equal(t, "in-memory", adapters["storage"])
}

func TestReadyzHandler_VerboseOutput_UsesAdapterInfoSnapshot(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-adapter-snapshot", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, clock.Real())
	h.SetVerboseToken(testVerboseToken)
	info := map[string]string{
		"mode":    "in-memory",
		"storage": "postgres",
	}
	h.SetAdapterInfo(info)
	info["storage"] = "mutated-before-read"

	result := h.computeReadyz(true)
	info["mode"] = "mutated"
	h.SetAdapterInfo(map[string]string{"mode": "new-map"})

	rec := httptest.NewRecorder()
	result.writeTo(context.Background(), rec)

	data := dataBody(t, rec)
	adapters, ok := data["adapters"].(map[string]any)
	require.True(t, ok, "verbose readyz output must contain adapters")
	assert.Equal(t, "in-memory", adapters["mode"])
	assert.Equal(t, "postgres", adapters["storage"])
}

func TestReadyzHandler_VerboseOutput_OmitsAdapterInfo_WhenNotSet(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, clock.Real())
	h.SetVerboseToken(testVerboseToken)
	// No SetAdapterInfo call.

	rec := httptest.NewRecorder()
	req := newVerboseRequest("/readyz?verbose=true")
	h.ReadyzHandler().ServeHTTP(rec, req)

	data := dataBody(t, rec)
	_, hasAdapters := data["adapters"]
	assert.False(t, hasAdapters, "verbose readyz output should not contain adapters when not set")
}

func TestReadyzHandler_DefaultOutput_UnhealthyAggregate(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, clock.Real())
	require.NoError(t, h.RegisterChecker("db", func(_ context.Context) error { return fmt.Errorf("connection refused") }))

	capture := withSlogCapture(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	errObj := errorBody(t, rec)
	assertReadyzServiceUnavailable(t, errObj)

	// Non-verbose 503: status/reason ride on slog; cells/dependencies/adapters
	// are intentionally suppressed at this level so high-frequency k8s probes
	// don't spam log backends. logUnhealthy is the only diagnostic surface
	// for non-verbose 503 — verify it actually emitted with the right fields.
	const recMsg = "readyz unhealthy"
	var found bool
	for _, r := range capture.snapshot() {
		if r.Message != recMsg {
			continue
		}
		found = true
		var statusVal, reasonVal slog.Value
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "status":
				statusVal = a.Value
			case "reason":
				reasonVal = a.Value
			}
			return true
		})
		assert.Equal(t, "unhealthy", statusVal.String(), "non-verbose 503 slog must carry status")
		assert.Equal(t, "readiness_failed", reasonVal.String(), "non-verbose 503 slog must carry reason")
	}
	require.True(t, found, "non-verbose 503 must still emit %q slog record (status/reason only)", recMsg)
	// Empty-array contract above subsumes the prior negative assertions:
	// non-verbose unhealthy 503 cannot expose cells/dependencies because the
	// canonical envelope mandates an empty details array regardless of
	// verbose mode (K#08 5xx redaction is unconditional).
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

func TestRegisterChecker_DuplicateReturnsError(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	h := New(asm, clock.Real())
	require.NoError(t, h.RegisterChecker("db", func(_ context.Context) error { return nil }))

	err := h.RegisterChecker("db", func(_ context.Context) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), `duplicate checker name "db"`)
}

func TestRegisterChecker_NilCheckerReturnsError(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	h := New(asm, clock.Real())

	err := h.RegisterChecker("db", nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), `nil checker for "db"`)
}

func TestMustRegisterChecker_PanicsOnError(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	h := New(asm, clock.Real())

	require.Panics(t, func() {
		h.MustRegisterChecker("db", nil)
	})
}

func TestReadyz_ShuttingDown_Returns503(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, clock.Real())

	// Before shutdown: should be healthy.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Mark shutting down.
	h.SetShuttingDown()

	// After shutdown: should be 503 with the shared public 503 code and a
	// low-cardinality reason that preserves drain semantics.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	errObj := errorBody(t, rec)
	assertReadyzServiceUnavailable(t, errObj)
}

func TestSetShuttingDown_Idempotent(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, clock.Real())
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
	asm := assembly.New(assembly.Config{ID: "test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })
	h := New(asm, clock.Real())
	require.NoError(t, h.RegisterChecker("db", func(_ context.Context) error { return nil }))
	return h
}

// TestReadyz_VerboseToken_CorrectHeader is kept as a minimal sanity check
// distinct from the table-driven TestReadyz_VerboseToken_StrictDeny — it
// double-confirms the happy path uses the same VerboseAuthHeader constant.
func TestReadyz_VerboseToken_CorrectHeader(t *testing.T) {
	h := newStartedHandler(t)
	h.SetVerboseToken("secret-token")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	req.Header.Set(VerboseAuthHeader, "secret-token")
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	data := dataBody(t, rec)
	_, hasCells := data["cells"]
	assert.True(t, hasCells, "correct token should expose verbose details")
}

// TestReadyz_VerboseToken_StrictDeny covers PR-A35's strict-401 semantics.
// The previous "silent downgrade to 200" behavior is gone: any ?verbose
// request that does not carry a matching token is answered with 401 and an
// errcode-shaped body. A bare /readyz request (no ?verbose) is always
// answered with the plain aggregate 200 regardless of token state — this
// protects Kubernetes readinessProbes, which never pass ?verbose.
func TestReadyz_VerboseToken_StrictDeny(t *testing.T) {
	const configured = "secret-token"
	tests := []struct {
		name            string
		tokenConfigured string // passed to SetVerboseToken before the request
		verboseDisabled bool   // applied via WithVerboseDisabled option
		sendVerbose     bool   // attach ?verbose=true query
		sendHeader      string // value for X-Readyz-Token; empty means omit
		wantStatus      int
		wantVerboseBody bool // verbose payload present (cells + dependencies)
		wantDeniedBody  bool // errcode-shaped denial payload
	}{
		{
			name:            "correct token returns verbose",
			tokenConfigured: configured,
			sendVerbose:     true,
			sendHeader:      configured,
			wantStatus:      http.StatusOK,
			wantVerboseBody: true,
		},
		{
			name:            "wrong token returns 401",
			tokenConfigured: configured,
			sendVerbose:     true,
			sendHeader:      "wrong",
			wantStatus:      http.StatusUnauthorized,
			wantDeniedBody:  true,
		},
		{
			name:            "missing header returns 401",
			tokenConfigured: configured,
			sendVerbose:     true,
			sendHeader:      "",
			wantStatus:      http.StatusUnauthorized,
			wantDeniedBody:  true,
		},
		{
			name:            "no token configured denies verbose (fail-closed)",
			tokenConfigured: "",
			sendVerbose:     true,
			wantStatus:      http.StatusUnauthorized,
			wantDeniedBody:  true,
		},
		{
			name:            "bare readyz stays 200 even with verbose disabled via missing token",
			tokenConfigured: "",
			sendVerbose:     false,
			wantStatus:      http.StatusOK,
			wantVerboseBody: false,
		},
		{
			name:            "bare readyz stays 200 when token configured",
			tokenConfigured: configured,
			sendVerbose:     false,
			wantStatus:      http.StatusOK,
			wantVerboseBody: false,
		},
		{
			name:            "WithVerboseDisabled answers verbose with plain 200",
			tokenConfigured: configured,
			verboseDisabled: true,
			sendVerbose:     true,
			sendHeader:      "anything",
			wantStatus:      http.StatusOK,
			wantVerboseBody: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asm := assembly.New(assembly.Config{ID: "test-verbose-deny", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
			c := newStubCell("cell-1")
			require.NoError(t, asm.Register(c))
			require.NoError(t, asm.Start(context.Background()))
			t.Cleanup(func() { _ = asm.Stop(context.Background()) })

			var opts []Option
			if tt.verboseDisabled {
				opts = append(opts, WithVerboseDisabled())
			}
			h := New(asm, clock.Real(), opts...)
			require.NoError(t, h.RegisterChecker("db", func(_ context.Context) error { return nil }))
			if tt.tokenConfigured != "" {
				h.SetVerboseToken(tt.tokenConfigured)
			}

			url := "/readyz"
			if tt.sendVerbose {
				url = "/readyz?verbose=true"
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			if tt.sendHeader != "" {
				req.Header.Set(VerboseAuthHeader, tt.sendHeader)
			}
			rec := httptest.NewRecorder()
			h.ReadyzHandler().ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)

			if tt.wantDeniedBody {
				errField := errorBody(t, rec)
				assert.Equal(t, string(errcode.ErrReadyzVerboseDenied), errField["code"])
				assert.Contains(t, errField["message"].(string), "X-Readyz-Token")
				_, hasDetails := errField["details"].([]any)
				assert.True(t, hasDetails,
					"denied envelope must include the standard details array (may be empty)")
				return
			}

			// Non-denied paths always come back as 200 under the data envelope.
			data := dataBody(t, rec)
			if tt.wantVerboseBody {
				_, hasCells := data["cells"]
				assert.True(t, hasCells, "verbose response must include cells under data")
			} else {
				_, hasCells := data["cells"]
				assert.False(t, hasCells, "non-verbose response must not include cells under data")
			}
		})
	}
}

func TestEmptyAssembly(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "empty", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	h := New(asm, clock.Real())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	h.LivezHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	data := dataBody(t, rec)
	assert.Equal(t, "healthy", data["status"])
}

// --- New parallel / deadline / panic tests (PR-A4 phase 5a) ---

// serialBaseline is the expected wall-clock time for running 3 probes of
// 100 ms each sequentially. Used to bound the parallelism semantic assertion.
const serialBaseline = 300 * time.Millisecond

// healthDeadlineShort is used for deadline/uncooperative probe tests.
const healthDeadlineShort = 80 * time.Millisecond

// healthReturnMaxElapsed bounds the handler-return-by-deadline assertions.
const healthReturnMaxElapsed = 200 * time.Millisecond

// healthSerial50 is the 50ms semantic slack for the parallelism test.
const healthSerial50 = testtime.MediumPoll

// healthParallelMax is the absolute wall-clock upper bound for the parallel test.
const healthParallelMax = 250 * time.Millisecond

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
	asm := assembly.New(assembly.Config{ID: "test-parallel", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	// Use a generous deadline so these tests do not time out.
	h := New(asm, clock.Real(), WithDeadline(testtime.D2s))
	for _, name := range []string{"probe-a", "probe-b", "probe-c"} {
		require.NoError(t, h.RegisterChecker(name, func(_ context.Context) error {
			time.Sleep(testtime.D100ms) //archtest:allow:test-sleep slow handler fixture; sleep IS the test parameter
			return nil
		}))
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
	assert.Less(t, elapsed, serialBaseline-healthSerial50,
		"3 parallel 100-ms probes must be at least 50ms faster than serial (%v); got %v", serialBaseline, elapsed)

	// Performance assertion: absolute upper bound on typical CI hardware.
	// If this flaps on resource-constrained CI, wrap in testing.Short() to
	// skip it in short mode — but keep the semantic assertion above.
	if !testing.Short() {
		assert.Less(t, elapsed, healthParallelMax,
			"3 parallel 100-ms probes must finish in < 250ms (serial would be ~300ms); got %v", elapsed)
	}
}

// TestReadyz_DeadlineExceeded verifies that a probe which exceeds the deadline
// is reported as status="timeout" with an error containing "deadline exceeded",
// and the aggregate returns 503.
func TestReadyz_DeadlineExceeded(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-deadline", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, clock.Real(), WithDeadline(testtime.MediumPoll))
	h.SetVerboseToken(testVerboseToken)
	require.NoError(t, h.RegisterChecker("slow", func(ctx context.Context) error {
		select {
		case <-time.After(testtime.D500ms):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}))

	capture := withSlogCapture(t)
	rec := httptest.NewRecorder()
	req := newVerboseRequest("/readyz?verbose=true")
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	errObj := errorBody(t, rec)
	assertReadyzServiceUnavailable(t, errObj)

	deps := readyzUnhealthyDeps(t, capture)
	slowEntry, ok := deps["slow"]
	require.True(t, ok, "slow entry must be present")
	assert.Equal(t, "timeout", slowEntry.Status, "exceeded-deadline probe must be status=timeout")
	require.NotEmpty(t, slowEntry.ErrorMsg, "timeout probe must include ErrorMsg in slog channel d")
	assert.Contains(t, string(slowEntry.ErrorMsg), "deadline exceeded",
		"ErrorMsg must mention 'deadline exceeded'")
}

// TestReadyz_IndependentOfRequestCtx verifies that /readyz probes are NOT
// canceled when the HTTP request context is canceled (e.g. kubelet disconnect).
// The probe ctx must derive from context.Background(), not r.Context().
func TestReadyz_IndependentOfRequestCtx(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-indep", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	probeDone := make(chan struct{})
	h := New(asm, clock.Real(), WithDeadline(testtime.D2s))
	require.NoError(t, h.RegisterChecker("slow-probe", func(ctx context.Context) error {
		// Probe takes 100 ms but the HTTP request ctx will be canceled
		// almost immediately — probe must NOT be affected.
		time.Sleep(testtime.D100ms) //archtest:allow:test-sleep slow handler fixture; sleep IS the test parameter
		close(probeDone)
		return nil
	}))

	// Use a cancellable request ctx and cancel it before the probe finishes.
	reqCtx, reqCancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil).WithContext(reqCtx)
	rec := httptest.NewRecorder()

	// Cancel request ctx after a very short time (before probe finishes).
	go func() {
		time.Sleep(testtime.D10ms) //archtest:allow:test-sleep goroutine timing fixture: controls cancel order
		reqCancel()
	}()

	h.ReadyzHandler().ServeHTTP(rec, req)

	// Probe must still complete even though the request ctx was canceled.
	select {
	case <-probeDone:
		// expected
	case <-time.After(testtime.D500ms):
		t.Fatal("probe was canceled by request ctx; must use background ctx")
	}
	// Aggregate result must be healthy (probe returned nil after sleeping).
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestReadyz_ProbePanic_Caught verifies that a panic inside a checker is
// recovered, the checker reports status=unhealthy, and the HTTP handler
// does not crash.
func TestReadyz_ProbePanic_Caught(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-panic", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, clock.Real(), WithDeadline(testtime.D2s))
	h.SetVerboseToken(testVerboseToken)
	require.NoError(t, h.RegisterChecker("panicking", func(_ context.Context) error {
		panic("something went very wrong")
	}))

	capture := withSlogCapture(t)
	rec := httptest.NewRecorder()
	req := newVerboseRequest("/readyz?verbose=true")

	// Must not crash the process.
	require.NotPanics(t, func() {
		h.ReadyzHandler().ServeHTTP(rec, req)
	})

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	errObj := errorBody(t, rec)
	assertReadyzServiceUnavailable(t, errObj)

	deps := readyzUnhealthyDeps(t, capture)
	panicEntry, ok := deps["panicking"]
	require.True(t, ok, "panicking entry must be present")
	assert.Equal(t, "unhealthy", panicEntry.Status)
}

// TestReadyz_VerboseError_SecretOmittedFromWire_RedactedInSlog enforces the
// four-channel split (ADR 202605171200): for 503 verbose responses,
// dependencies are stripped from the wire envelope entirely (K#08 5xx
// details := []); the full redacted error lives only in channel d
// ops-diagnostics slog. So the wire-side assertions are:
//
//   - the canonical errcode 503 envelope is intact (code + message + empty details)
//   - the wire body MUST NOT contain the raw secret sentinel
//   - the wire body MUST NOT contain the redaction mask either (channel d
//     ownership is exclusive — if the mask appeared on wire, error text leaked)
//
// And the slog-side assertion is:
//
//   - typed channel d dependencies[name].ErrorMsg carries the redacted error
//     via the newRedactedErrorMsg funnel
//
// Supersedes the pre-ADR pair (TestReadyz_VerboseError_LongErrTruncated +
// TestReadyz_VerboseError_RedactsBeforeTruncating): truncation on wire is
// gone (wire has no error text to truncate), and redaction-before-truncate
// is now redaction-into-typed-newtype enforced by archtest funnel.
func TestReadyz_VerboseError_SecretOmittedFromWire_RedactedInSlog(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-redact-verbose", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	const leakSentinel = "health-verbose-leak-sentinel-8f2b"
	h := New(asm, clock.Real())
	h.SetVerboseToken(testVerboseToken)
	require.NoError(t, h.RegisterChecker("sensitive", func(_ context.Context) error {
		return fmt.Errorf("postgres probe failed: password=%s token=%s", leakSentinel, leakSentinel)
	}))

	capture := withSlogCapture(t)
	rec := httptest.NewRecorder()
	req := newVerboseRequest("/readyz?verbose=true")
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	// Wire body: must not contain sentinel or redaction mask. (Channel a/b/c
	// envelope contains the JSON keys "error" / "code" / "message" / "details"
	// by construction; assertions below target leak surfaces, not envelope keys.)
	wireBody := rec.Body.String()
	assert.NotContains(t, wireBody, leakSentinel,
		"verbose readyz wire body must not leak raw probe secrets")
	assert.NotContains(t, wireBody, "<REDACTED>",
		"verbose readyz wire body must not contain redaction mask either — channel d "+
			"ownership is exclusive; mask on wire would mean error text leaked from slog into wire")

	errObj := errorBody(t, rec)
	assertReadyzServiceUnavailable(t, errObj)

	// slog channel d: full redacted error text via typed redactedErrorMsg funnel.
	deps := readyzUnhealthyDeps(t, capture)
	sensitiveEntry, ok := deps["sensitive"]
	require.True(t, ok, "sensitive entry must be present in slog dependencies")
	errMsg := string(sensitiveEntry.ErrorMsg)
	assert.Contains(t, errMsg, "<REDACTED>",
		"slog channel d ErrorMsg must contain the redaction mask")
	assert.NotContains(t, errMsg, leakSentinel,
		"slog channel d ErrorMsg must have the raw sentinel masked")
}

// TestReadyz_VerbosePanicSecret_RedactedInSlog is the panic-path analog of
// TestReadyz_VerboseError_SecretOmittedFromWire_RedactedInSlog: a probe
// panic carrying a secret-shaped string must (a) not leak to wire, and
// (b) reach slog channel d only after passing through the redaction funnel.
func TestReadyz_VerbosePanicSecret_RedactedInSlog(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-panic-redact", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	const leakSentinel = "panic-leak-sentinel-c91d"
	h := New(asm, clock.Real(), WithDeadline(testtime.D2s))
	h.SetVerboseToken(testVerboseToken)
	require.NoError(t, h.RegisterChecker("panicking", func(_ context.Context) error {
		// Use api-key / password — keys present in pkg/redaction.sensitiveKeyPattern.
		// Demonstrates that the funnel applies to panic-derived errors the same way
		// it does to ordinary probe errors.
		panic(fmt.Sprintf("api-key=%s password=%s", leakSentinel, leakSentinel))
	}))

	capture := withSlogCapture(t)
	rec := httptest.NewRecorder()
	req := newVerboseRequest("/readyz?verbose=true")
	require.NotPanics(t, func() {
		h.ReadyzHandler().ServeHTTP(rec, req)
	})

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	wireBody := rec.Body.String()
	assert.NotContains(t, wireBody, leakSentinel,
		"panic-derived probe error must not leak to wire body")
	assert.NotContains(t, wireBody, "<REDACTED>",
		"wire body must not contain redaction mask either — channel d ownership is exclusive")

	deps := readyzUnhealthyDeps(t, capture)
	panicEntry, ok := deps["panicking"]
	require.True(t, ok, "panicking entry must be present in slog dependencies")
	assert.Equal(t, "unhealthy", panicEntry.Status)
	errMsg := string(panicEntry.ErrorMsg)
	assert.Contains(t, errMsg, "<REDACTED>",
		"slog channel d ErrorMsg must contain the redaction mask for panic-derived errors")
	assert.NotContains(t, errMsg, leakSentinel,
		"slog channel d ErrorMsg must have the raw panic secret masked")
}

// TestReadyz_UncooperativeChecker_WrapperReturnsOnDeadline verifies the
// PR-A35 structural guarantee: wrapCtxSafe in RegisterChecker ensures the
// outer Checker returns as soon as the aggregator's deadline fires, even if
// the inner probe ignores ctx. The inner goroutine continues running in the
// background; the aggregator is no longer entangled with its lifetime.
func TestReadyz_UncooperativeChecker_WrapperReturnsOnDeadline(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-uncooperative", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, clock.Real(), WithVerboseDisabled(), WithDeadline(healthDeadlineShort))

	// Uncooperative probe: blocks on a channel that only the test closes on
	// cleanup. Without wrapCtxSafe this would hold runProbesParallel open
	// past h.deadline; with the wrapper the outer Checker returns on
	// ctx.Done while the inner fn keeps running until the test ends.
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	require.NoError(t, h.RegisterChecker("stuck", func(_ context.Context) error {
		<-unblock
		return nil
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose", nil)

	start := time.Now()
	h.ReadyzHandler()(rr, req)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, healthReturnMaxElapsed,
		"handler must return within ~deadline (80ms) even with uncooperative probe; got %v", elapsed)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)

	// WithVerboseDisabled answers verbose requests with the plain aggregate
	// body (no dependencies map); we only assert the aggregate status here.
	errObj := errorBody(t, rr)
	assertReadyzServiceUnavailable(t, errObj)
}

// TestReadyz_UncooperativeChecker_VerboseReportsTimeout covers the verbose
// branch of the uncooperative-probe contract. When wrapCtxSafe's outer
// Checker returns ctx.Err() (DeadlineExceeded) the probe result must be
// tagged "timeout" in the verbose body so dashboards can distinguish
// "probe overran" from domain-level "probe failed". Regression guard for
// F4 — the previous sweep lost this coverage when the earlier test was
// flipped to WithVerboseDisabled.
func TestReadyz_UncooperativeChecker_VerboseReportsTimeout(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-uncooperative-verbose", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, clock.Real(), WithDeadline(healthDeadlineShort))
	h.SetVerboseToken(testVerboseToken)

	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	require.NoError(t, h.RegisterChecker("stuck", func(_ context.Context) error {
		<-unblock
		return nil
	}))

	capture := withSlogCapture(t)
	rr := httptest.NewRecorder()
	req := newVerboseRequest("/readyz?verbose=true")
	start := time.Now()
	h.ReadyzHandler()(rr, req)
	elapsed := time.Since(start)

	assert.Less(t, elapsed, healthReturnMaxElapsed,
		"handler must return within ~deadline even with uncooperative probe; got %v", elapsed)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)

	errObj := errorBody(t, rr)
	assertReadyzServiceUnavailable(t, errObj)

	deps := readyzUnhealthyDeps(t, capture)
	stuck, ok := deps["stuck"]
	require.True(t, ok, "stuck probe must be present in verbose dependencies")
	assert.Equal(t, "timeout", stuck.Status,
		"uncooperative probe must be surfaced as status=timeout (not unhealthy)")
	require.NotEmpty(t, stuck.ErrorMsg, "timeout probe must include ErrorMsg in slog channel d")
	assert.Contains(t, string(stuck.ErrorMsg), "deadline",
		"timeout probe ErrorMsg must mention deadline; got %q", stuck.ErrorMsg)
}

// TestWriteJSON_WriteError verifies that writeJSON logs an slog.Error when the
// ResponseWriter.Write call fails (e.g. because the connection was reset).
// This covers the slog.Any("error", err) branch on line 621 of health.go.
func TestWriteJSON_WriteError(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-write-err", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, clock.Real())

	// failWriter returns an error from every Write call so json.Encoder.Encode
	// surfaces the error into the slog.Error branch.
	fw := &failWriter{
		header: make(http.Header),
	}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	// ServeHTTP must not panic even though Write fails.
	require.NotPanics(t, func() {
		h.LivezHandler().ServeHTTP(fw, req)
	})
}

// failWriter is an http.ResponseWriter whose Write always returns an error.
type failWriter struct {
	header http.Header
	code   int
}

func (f *failWriter) Header() http.Header         { return f.header }
func (f *failWriter) WriteHeader(code int)        { f.code = code }
func (f *failWriter) Write(_ []byte) (int, error) { return 0, fmt.Errorf("simulated write failure") }

// TestReadyz_VerboseDependencies_StructuredOutput verifies the typed slog
// dependency format per ADR 202605171200 channel d: each entry is a
// slogDependencyEntry struct with Status, DurationMs, and ErrorMsg fields.
// Healthy probes have empty ErrorMsg (omitted on JSON serialization via
// the json:",omitempty" tag); unhealthy probes carry the redacted error.
func TestReadyz_VerboseDependencies_StructuredOutput(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-structured", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, clock.Real())
	h.SetVerboseToken(testVerboseToken)
	require.NoError(t, h.RegisterChecker("ok-probe", func(_ context.Context) error { return nil }))
	require.NoError(t, h.RegisterChecker("fail-probe", func(_ context.Context) error { return fmt.Errorf("disk full") }))

	capture := withSlogCapture(t)
	rec := httptest.NewRecorder()
	req := newVerboseRequest("/readyz?verbose=true")
	h.ReadyzHandler().ServeHTTP(rec, req)

	// 503 wire envelope is canonical (empty details); typed verbose breakdown
	// rides on the slog "readyz unhealthy" record per K#08 + ADR 202605171200.
	errObj := errorBody(t, rec)
	assertReadyzServiceUnavailable(t, errObj)

	deps := readyzUnhealthyDeps(t, capture)

	okEntry, ok := deps["ok-probe"]
	require.True(t, ok, "ok-probe must be present")
	assert.Equal(t, "healthy", okEntry.Status)
	assert.Empty(t, okEntry.ErrorMsg, "healthy probe must have empty ErrorMsg")

	failEntry, ok := deps["fail-probe"]
	require.True(t, ok, "fail-probe must be present")
	assert.Equal(t, "unhealthy", failEntry.Status)
	assert.NotEmpty(t, failEntry.ErrorMsg, "unhealthy probe must include ErrorMsg")
	assert.Contains(t, string(failEntry.ErrorMsg), "disk full")
}

// --- Three-state (healthy / degraded / unhealthy) tests (PR-A49 B4) ---

// TestReadyz_DegradedReturns200WithStatusField verifies that a probe returning
// a wrapped cell.ErrDegraded produces HTTP 200 with body status="degraded".
// degraded must NOT trigger pod eviction (fail-open semantic).
func TestReadyz_DegradedReturns200WithStatusField(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-degraded", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := newStubCell("configcore")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })

	h := New(asm, clock.Real())
	h.SetVerboseToken(testVerboseToken)
	require.NoError(t, h.RegisterChecker("outbox-failopen-rate.configcore", func(_ context.Context) error {
		return fmt.Errorf("drop ratio exceeded: %w", cell.ErrDegraded)
	}))

	rec := httptest.NewRecorder()
	req := newVerboseRequest("/readyz?verbose=true")
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "degraded must return HTTP 200, not 503")

	data := dataBody(t, rec)
	assert.Equal(t, "degraded", data["status"], "body status must be 'degraded'")
}

// TestReadyz_UnhealthyTrumpsDegraded verifies that when both a degraded checker
// and an unhealthy checker are registered, the aggregate result is "unhealthy"
// and the response is HTTP 503.
func TestReadyz_UnhealthyTrumpsDegraded(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-unhealthy-trumps", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := newStubCell("configcore")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })

	h := New(asm, clock.Real())
	h.SetVerboseToken(testVerboseToken)
	require.NoError(t, h.RegisterChecker("degraded-probe", func(_ context.Context) error {
		return fmt.Errorf("soft degradation: %w", cell.ErrDegraded)
	}))
	require.NoError(t, h.RegisterChecker("unhealthy-probe", func(_ context.Context) error {
		return fmt.Errorf("db unreachable")
	}))

	rec := httptest.NewRecorder()
	req := newVerboseRequest("/readyz?verbose=true")
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "unhealthy must trump degraded → 503")

	errObj := errorBody(t, rec)
	assertReadyzServiceUnavailable(t, errObj)
}

// stubDegradedCell is a minimal Cell that always reports HealthStatus.Status="degraded".
// Used by TestReadyz_DegradedAggregatesFromCellHealth to exercise the E2E path
// through ReadyzHandler → aggregateCellHealth → assembly.Health() → cell.Health().
type stubDegradedCell struct {
	*cell.BaseCell
}

func newStubDegradedCell(id string) *stubDegradedCell {
	return &stubDegradedCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{
			ID:   id,
			Type: "core",
		}),
	}
}

// Health overrides BaseCell.Health() to always return "degraded", simulating
// a cell that is started but operating in a degraded state.
func (s *stubDegradedCell) Health() cell.HealthStatus {
	return cell.HealthStatus{Status: "degraded"}
}

type stubPanickingHealthCell struct {
	*cell.BaseCell
}

func newStubPanickingHealthCell(id string) *stubPanickingHealthCell {
	return &stubPanickingHealthCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{
			ID:   id,
			Type: "core",
		}),
	}
}

func (s *stubPanickingHealthCell) Health() cell.HealthStatus {
	panic("cell health panic")
}

func TestReadyz_ComputationPanic_UsesServiceUnavailableCode(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-health-panic", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := newStubPanickingHealthCell("panic-cell")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })

	h := New(asm, clock.Real())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	require.NotPanics(t, func() {
		h.ReadyzHandler().ServeHTTP(rec, req)
	})

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	errObj := errorBody(t, rec)
	assertReadyzServiceUnavailable(t, errObj)
}

// TestReadyz_DegradedAggregatesFromCellHealth verifies the E2E path:
// when a cell's Health() returns HealthStatus.Status="degraded" and no probe
// checkers are registered, ReadyzHandler must respond HTTP 200 with body
// status="degraded" (not "unhealthy" / 503).
func TestReadyz_DegradedAggregatesFromCellHealth(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-cell-degraded", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := newStubDegradedCell("degraded-cell")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })

	h := New(asm, clock.Real())
	h.SetVerboseToken(testVerboseToken)
	// No probe checkers — only cell Health() contributes to the aggregate.

	rec := httptest.NewRecorder()
	req := newVerboseRequest("/readyz?verbose=true")
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "degraded cell must produce HTTP 200, not 503")
	data := dataBody(t, rec)
	assert.Equal(t, "degraded", data["status"],
		"ReadyzHandler must aggregate cell HealthStatus='degraded' into body status='degraded'")
}

// TestReadyz_VerboseExposesDegradedDependency verifies that when a probe returns
// a wrapped cell.ErrDegraded, the verbose body dependency entry has status="degraded".
// Per ADR 202605171200 wire shape is frozen — error text never appears on wire,
// even for degraded probes (no behavioral difference between healthy/degraded
// at the wire shape level; only the status string changes).
func TestReadyz_VerboseExposesDegradedDependency(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-verbose-degraded", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Start(context.Background()))
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })

	h := New(asm, clock.Real())
	h.SetVerboseToken(testVerboseToken)
	require.NoError(t, h.RegisterChecker("outbox-failopen-rate.configcore", func(_ context.Context) error {
		return fmt.Errorf("drop ratio exceeded: %w", cell.ErrDegraded)
	}))

	rec := httptest.NewRecorder()
	req := newVerboseRequest("/readyz?verbose=true")
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "degraded probe must produce HTTP 200")

	data := dataBody(t, rec)
	deps, ok := data["dependencies"].(map[string]any)
	require.True(t, ok, "verbose body must contain dependencies map")
	entry, ok := deps["outbox-failopen-rate.configcore"].(map[string]any)
	require.True(t, ok, "outbox-failopen-rate.configcore must be present in dependencies")
	assert.Equal(t, "degraded", entry["status"],
		"verbose dependency entry status must be 'degraded'")
	// Wire shape per ADR 202605171200 §3: error text belongs to channel d (slog),
	// not the wire body — even degraded probes carry only status + duration on wire.
	_, hasErr := entry["error"]
	assert.False(t, hasErr, "wire dependency entry must not carry error key (channel d slog only)")
	_, hasErrMsg := entry["error_msg"]
	assert.False(t, hasErrMsg, "wire dependency entry must not carry error_msg key (channel d slog only)")
}

// TestReadyz_HealthyAllAcrossBoard verifies the sanity check: all healthy → HTTP 200
// with status="healthy".
func TestReadyz_HealthyAllAcrossBoard(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test-all-healthy", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })

	h := New(asm, clock.Real())
	require.NoError(t, h.RegisterChecker("db", func(_ context.Context) error { return nil }))
	require.NoError(t, h.RegisterChecker("cache", func(_ context.Context) error { return nil }))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	data := dataBody(t, rec)
	assert.Equal(t, "healthy", data["status"])
}

// TestRunOneProbe_DegradedSentinelMappedToDegraded is a unit test that directly
// calls runOneProbe with a checker returning a wrapped cell.ErrDegraded and
// verifies ProbeResult.Status == "degraded".
func TestRunOneProbe_DegradedSentinelMappedToDegraded(t *testing.T) {
	checker := func(_ context.Context) error {
		return fmt.Errorf("drop ratio exceeded: %w", cell.ErrDegraded)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D5s)
	defer cancel()

	pr := runOneProbe(ctx, checker, testtime.D5s, clock.Real())

	assert.Equal(t, "degraded", pr.Status,
		"checker returning wrapped cell.ErrDegraded must produce ProbeResult.Status=degraded")
	require.NotNil(t, pr.Err, "degraded probe must carry non-nil Err")
	assert.Contains(t, pr.Err.Error(), "drop ratio exceeded")
}

// TestRankStatus verifies the rank ordering used by the three-state aggregator.
func TestRankStatus(t *testing.T) {
	tests := []struct {
		status string
		want   int
	}{
		{"healthy", 0},
		{"degraded", 1},
		{"unhealthy", 2},
		{"timeout", 2},
		{"unknown", 2},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			assert.Equal(t, tt.want, rankStatus(tt.status))
		})
	}
}

// TestStatusFromRank verifies the inverse of rankStatus.
func TestStatusFromRank(t *testing.T) {
	assert.Equal(t, "healthy", statusFromRank(0))
	assert.Equal(t, "degraded", statusFromRank(1))
	assert.Equal(t, "unhealthy", statusFromRank(2))
	assert.Equal(t, "unhealthy", statusFromRank(99))
}

// TestVerboseDecision_DefaultDenies verifies that the /readyz?verbose endpoint
// returns HTTP 401 when no verbose token is configured and verbose is not
// explicitly disabled. This is the SEC-FAIL-CLOSED-04 (health verbose) fix:
// the previous fail-open default silently rendered the verbose body when token=""
// and disabled=false, leaking internal health details to unauthenticated callers.
//
// TDD phase-1 red-light: the current verboseDecision returns (true, false) in the
// "no token, not disabled" branch, so this test will FAIL until phase-2 changes
// the branch to return (false, true).
func TestVerboseDecision_DefaultDenies(t *testing.T) {
	t.Parallel()

	// Build a minimal assembly with one started cell so /readyz returns healthy.
	asm := assembly.New(assembly.Config{ID: "test-sec", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := newStubCell("sec-cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm, clock.Real())
	// Deliberately do NOT call h.SetVerboseToken(...) and do NOT call
	// h.SetVerboseDisabled(). This is the "default" state where operators have
	// not configured verbose behavior at all.

	rec := httptest.NewRecorder()
	// Request verbose output without a token header.
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)

	// Phase-2 expectation: 401 with ErrReadyzVerboseDenied envelope.
	// Phase-1 current: 200 with verbose body (fail-open) → test FAILS.
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"readyz?verbose without token configuration must return 401; "+
			"operators must explicitly configure a token or disable verbose")

	// Verify the error envelope shape.
	errObj := errorBody(t, rec)
	assert.Equal(t, string(errcode.ErrReadyzVerboseDenied), errObj["code"],
		"error code must be ERR_READYZ_VERBOSE_DENIED")
}
