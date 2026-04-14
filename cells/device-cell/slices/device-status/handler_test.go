package devicestatus

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/device-cell/internal/mem"
)

func setupStatusHandler() (*Handler, *mem.DeviceRepository) {
	repo := mem.NewDeviceRepository()
	svc := NewService(repo, slog.Default())
	return NewHandler(svc), repo
}

func TestHandleGetStatus(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(*mem.DeviceRepository)
		deviceID   string
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			name: "existing device returns 200 with status",
			setup: func(r *mem.DeviceRepository) {
				_ = r.Create(context.Background(), &domain.Device{
					ID: "dev-1", Name: "sensor-a", Status: "online",
				})
			},
			deviceID:   "dev-1",
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var resp map[string]any
				require.NoError(t, json.Unmarshal(body, &resp))
				data, ok := resp["data"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "dev-1", data["id"])
				assert.Equal(t, "sensor-a", data["name"])
				assert.Equal(t, "online", data["status"])
			},
		},
		{
			name:       "non-existent device returns 404",
			setup:      func(_ *mem.DeviceRepository) {},
			deviceID:   "dev-missing",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, repo := setupStatusHandler()
			tc.setup(repo)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/devices/"+tc.deviceID+"/status", nil)
			req.SetPathValue("id", tc.deviceID)
			h.HandleGetStatus(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}
