package deviceregister

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/device-cell/internal/mem"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeviceRegisterResponse_Fields(t *testing.T) {
	device := &domain.Device{ID: "dev-1", Name: "sensor-a", Status: "online"}
	resp := toDeviceRegisterResponse(device)

	assert.Equal(t, "dev-1", resp.ID)
	assert.Equal(t, "sensor-a", resp.Name)
	assert.Equal(t, "online", resp.Status)

	b, err := json.Marshal(resp)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"id"`)
	assert.Contains(t, s, `"name"`)
	assert.Contains(t, s, `"status"`)
}

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
				var envelope map[string]any
				require.NoError(t, json.Unmarshal(body, &envelope))
				data, ok := envelope["data"].(map[string]any)
				require.True(t, ok, "response should have data envelope")
				assert.NotEmpty(t, data["id"])
				assert.Equal(t, "sensor-a", data["name"])
				assert.Equal(t, "online", data["status"])

				// Verify camelCase JSON keys (#27n).
				assert.Contains(t, data, "id", "key must be camelCase")
				assert.Contains(t, data, "name", "key must be camelCase")
				assert.Contains(t, data, "status", "key must be camelCase")
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
		{
			name:       "unknown field returns 400",
			body:       `{"name":"x","extra":"y"}`,
			wantStatus: http.StatusBadRequest,
			checkBody: func(t *testing.T, body []byte) {
				var resp struct {
					Error struct {
						Details map[string]any `json:"details"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(body, &resp))
				assert.Equal(t, "unknown field", resp.Error.Details["reason"])
				assert.Equal(t, "extra", resp.Error.Details["field"])
			},
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
