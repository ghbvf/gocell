package deviceregister

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/cells/device-cell/internal/mem"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupRegisterHandler() *Handler {
	repo := mem.NewDeviceRepository()
	pub := eventbus.New()
	svc := NewService(repo, pub, slog.Default())
	return NewHandler(svc)
}

func TestHandleRegister(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			name:       "valid request returns 201",
			body:       `{"name":"sensor-a"}`,
			wantStatus: http.StatusCreated,
			checkBody: func(t *testing.T, body []byte) {
				var resp map[string]any
				require.NoError(t, json.Unmarshal(body, &resp))
				assert.NotEmpty(t, resp["id"])
				assert.Equal(t, "sensor-a", resp["name"])
				assert.Equal(t, "online", resp["status"])
			},
		},
		{
			name:       "invalid JSON returns 400",
			body:       `{bad`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty name returns 400",
			body:       `{"name":""}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing name field returns 400",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := setupRegisterHandler()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")

			h.HandleRegister(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}
