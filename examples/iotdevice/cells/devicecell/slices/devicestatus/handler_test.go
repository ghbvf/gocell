package devicestatus

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	statuscontract "github.com/ghbvf/gocell/generated/contracts/http/device/status/v1"
	"github.com/ghbvf/gocell/runtime/auth"
)

func setupStatusHandler() (*statuscontract.Handler, *mem.DeviceRepository) {
	repo := mem.NewDeviceRepository()
	svc := NewService(repo, slog.Default())
	return statuscontract.NewHandler(svc, auth.SelfOr("id", "admin")), repo
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

				// Verify camelCase JSON keys (#27n).
				assert.Contains(t, data, "id", "key must be camelCase")
				assert.Contains(t, data, "name", "key must be camelCase")
				assert.Contains(t, data, "status", "key must be camelCase")
				assert.Contains(t, data, "lastSeen", "key must be camelCase")
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
			h.ServeHTTP(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

func TestService_Status_LastSeenRFC3339(t *testing.T) {
	repo := mem.NewDeviceRepository()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	_ = repo.Create(context.Background(), &domain.Device{
		ID: "dev-ts", Name: "ts-test", Status: "online", LastSeen: now,
	})
	svc := NewService(repo, slog.Default())
	h := statuscontract.NewHandler(svc, auth.SelfOr("id", "admin"))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetPathValue("id", "dev-ts")
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, "2026-01-02T03:04:05Z", data["lastSeen"])
}
