package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

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
			asm := assembly.New(assembly.Config{ID: "test"})
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
			asm := assembly.New(assembly.Config{ID: "test"})
			c := newStubCell("cell-1")
			require.NoError(t, asm.Register(c))

			if tt.startCells {
				require.NoError(t, asm.Start(context.Background()))
				defer func() { _ = asm.Stop(context.Background()) }()
			}

			h := New(asm)
			h.RegisterChecker("db", func() error { return tt.checkerErr })

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
	asm := assembly.New(assembly.Config{ID: "test"})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm)
	h.RegisterChecker("rabbitmq", func() error { return nil })
	h.RegisterChecker("postgres", func() error { return fmt.Errorf("connection refused") })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose", nil)
	h.ReadyzHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "unhealthy", body["status"])

	deps, ok := body["dependencies"].(map[string]any)
	require.True(t, ok, "response must contain dependencies map")
	assert.Equal(t, "healthy", deps["rabbitmq"], "rabbitmq checker should be healthy")
	assert.Equal(t, "unhealthy", deps["postgres"], "postgres checker should be unhealthy")
}

func TestLivezHandler_IsProcessLivenessOnly(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test"})
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
	asm := assembly.New(assembly.Config{ID: "test"})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm)
	h.RegisterChecker("db", func() error { return nil })

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
	asm := assembly.New(assembly.Config{ID: "test"})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm)
	h.RegisterChecker("db", func() error { return nil })

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
	assert.Equal(t, "healthy", deps["db"])
}

func TestReadyzHandler_DefaultOutput_UnhealthyAggregate(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test"})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	h := New(asm)
	h.RegisterChecker("db", func() error { return fmt.Errorf("connection refused") })

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
	asm := assembly.New(assembly.Config{ID: "test"})
	h := New(asm)
	h.RegisterChecker("db", func() error { return nil })

	assert.PanicsWithValue(t, `health: duplicate checker name "db"`, func() {
		h.RegisterChecker("db", func() error { return nil })
	})
}

func TestEmptyAssembly(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "empty"})
	h := New(asm)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	h.LivezHandler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "healthy", body["status"])
}
