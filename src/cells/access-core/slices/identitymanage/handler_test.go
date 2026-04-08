package identitymanage

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

func setup() http.Handler {
	svc := NewService(mem.NewUserRepository(), mem.NewSessionRepository(), eventbus.New(), slog.Default())
	mux := celltest.NewTestMux()
	NewHandler(svc).RegisterRoutes(mux)
	return mux
}

func TestHandler(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			name:       "POST / valid user returns 201",
			method:     http.MethodPost,
			path:       "/",
			body:       `{"username":"alice","email":"a@b.com","password":"secret123"}`,
			wantStatus: http.StatusCreated,
			checkBody: func(t *testing.T, body []byte) {
				var resp map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(body, &resp))
				assert.Contains(t, string(resp["data"]), "alice")
			},
		},
		{
			name:       "POST / invalid body returns 400",
			method:     http.MethodPost,
			path:       "/",
			body:       `{bad json`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "GET /{id} nonexistent returns 404",
			method:     http.MethodGet,
			path:       "/no-such-id",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := setup()
			var req *http.Request
			if tc.body != "" {
				req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			} else {
				req = httptest.NewRequest(tc.method, tc.path, nil)
			}
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

func TestHandler_CreateThenGetThenDelete(t *testing.T) {
	r := setup()

	// Create
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"username":"bob","email":"b@c.com","password":"pass1234"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := created.Data.ID
	require.NotEmpty(t, id)

	// Get
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/"+id, nil))
	assert.Equal(t, http.StatusOK, w.Code)

	// Delete
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/"+id, nil))
	assert.Equal(t, http.StatusNoContent, w.Code)
}
