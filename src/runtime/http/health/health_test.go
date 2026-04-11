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
			name:       "unhealthy when not started",
			startCells: false,
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "unhealthy",
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
			checks := body["checks"].(map[string]any)
			_, hasCellCheck := checks["cell-1"]
			assert.True(t, hasCellCheck, "should include cell-1 in checks")
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
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
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
