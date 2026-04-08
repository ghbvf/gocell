package devicecommand

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/device-cell/internal/mem"
)

// setupCommandHandler creates a Handler and seeds a device so that command operations succeed.
func setupCommandHandler() (*Handler, *mem.DeviceRepository, *mem.CommandRepository) {
	devRepo := mem.NewDeviceRepository()
	cmdRepo := mem.NewCommandRepository()
	svc := NewService(cmdRepo, devRepo, slog.Default())

	_ = devRepo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "sensor-a", Status: "online",
	})

	return NewHandler(svc), devRepo, cmdRepo
}

func TestHandleEnqueue(t *testing.T) {
	tests := []struct {
		name       string
		deviceID   string
		body       string
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			name:       "valid enqueue returns 201",
			deviceID:   "dev-1",
			body:       `{"payload":"reboot"}`,
			wantStatus: http.StatusCreated,
			checkBody: func(t *testing.T, body []byte) {
				var resp map[string]any
				require.NoError(t, json.Unmarshal(body, &resp))
				assert.NotEmpty(t, resp["id"])
				assert.Equal(t, "dev-1", resp["deviceId"])
				assert.Equal(t, "reboot", resp["payload"])
				assert.Equal(t, "pending", resp["status"])
			},
		},
		{
			name:       "invalid JSON returns 400",
			deviceID:   "dev-1",
			body:       `{bad`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty payload returns 400",
			deviceID:   "dev-1",
			body:       `{"payload":""}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "non-existent device returns 404",
			deviceID:   "dev-missing",
			body:       `{"payload":"reboot"}`,
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := setupCommandHandler()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/devices/"+tc.deviceID+"/commands", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.SetPathValue("id", tc.deviceID)
			h.HandleEnqueue(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

func TestHandleListPending(t *testing.T) {
	tests := []struct {
		name       string
		deviceID   string
		seedCmds   int
		wantStatus int
		wantTotal  int
	}{
		{
			name:       "returns pending commands",
			deviceID:   "dev-1",
			seedCmds:   2,
			wantStatus: http.StatusOK,
			wantTotal:  2,
		},
		{
			name:       "no pending returns empty list",
			deviceID:   "dev-1",
			seedCmds:   0,
			wantStatus: http.StatusOK,
			wantTotal:  0,
		},
		{
			name:       "non-existent device returns 404",
			deviceID:   "dev-missing",
			seedCmds:   0,
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _, cmdRepo := setupCommandHandler()
			ctx := context.Background()
			for i := range tc.seedCmds {
				_ = cmdRepo.Create(ctx, &domain.Command{
					ID: "cmd-" + string(rune('a'+i)), DeviceID: "dev-1",
					Payload: "p", Status: "pending",
				})
			}

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/devices/"+tc.deviceID+"/commands", nil)
			req.SetPathValue("id", tc.deviceID)
			h.HandleListPending(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantStatus == http.StatusOK {
				var resp map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, float64(tc.wantTotal), resp["total"])
			}
		})
	}
}

func TestHandleAck(t *testing.T) {
	tests := []struct {
		name       string
		deviceID   string
		cmdID      string
		seedCmd    bool
		wantStatus int
	}{
		{
			name:       "ack pending command returns 200",
			deviceID:   "dev-1",
			cmdID:      "cmd-ack",
			seedCmd:    true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "ack non-existent command returns error",
			deviceID:   "dev-1",
			cmdID:      "cmd-missing",
			seedCmd:    false,
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _, cmdRepo := setupCommandHandler()
			if tc.seedCmd {
				_ = cmdRepo.Create(context.Background(), &domain.Command{
					ID: tc.cmdID, DeviceID: tc.deviceID, Payload: "reboot", Status: "pending",
				})
			}

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/devices/"+tc.deviceID+"/commands/"+tc.cmdID+"/ack", nil)
			req.SetPathValue("id", tc.deviceID)
			req.SetPathValue("cmdId", tc.cmdID)
			h.HandleAck(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantStatus == http.StatusOK {
				var resp map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, "acked", resp["status"])
			}
		})
	}
}
