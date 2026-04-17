package devicecommand

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/device-cell/internal/mem"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

func TestToCommandResponse_NilInput(t *testing.T) {
	var got CommandResponse
	assert.NotPanics(t, func() { got = toCommandResponse(nil) })
	assert.Zero(t, got.ID)
}

// setupCommandHandler creates a Handler and seeds a device so that command operations succeed.
func setupCommandHandler() (*Handler, *mem.DeviceRepository, *mem.CommandRepository) {
	devRepo := mem.NewDeviceRepository()
	cmdRepo := mem.NewCommandRepository()
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	svc := NewService(cmdRepo, devRepo, codec, slog.Default(), query.RunModeProd)

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
				var envelope map[string]any
				require.NoError(t, json.Unmarshal(body, &envelope))
				data, ok := envelope["data"].(map[string]any)
				require.True(t, ok, "response should have data envelope")
				assert.NotEmpty(t, data["id"])
				assert.Equal(t, "dev-1", data["deviceId"])
				assert.Equal(t, "reboot", data["payload"])
				assert.Equal(t, "pending", data["status"])
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
		{
			name:       "unknown field returns 400",
			deviceID:   "dev-1",
			body:       `{"payload":"reboot","extra":"y"}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := setupCommandHandler()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/devices/"+tc.deviceID+"/commands", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.SetPathValue("id", tc.deviceID)
			req = req.WithContext(auth.TestContext("operator-1", []string{"operator"}))
			h.HandleEnqueue(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

// Trust boundary tests for enqueue (#P1-2)
func TestHandleEnqueue_Authorization(t *testing.T) {
	tests := []struct {
		name       string
		subject    string
		roles      []string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "admin allowed",
			subject:    "admin-user",
			roles:      []string{"admin"},
			wantStatus: http.StatusCreated,
		},
		{
			name:       "operator allowed",
			subject:    "op-1",
			roles:      []string{"operator"},
			wantStatus: http.StatusCreated,
		},
		{
			name:       "device role returns 403",
			subject:    "dev-99",
			roles:      []string{"device"},
			wantStatus: http.StatusForbidden,
			wantCode:   "ERR_AUTH_FORBIDDEN",
		},
		{
			name:       "no roles returns 403",
			subject:    "user-1",
			roles:      nil,
			wantStatus: http.StatusForbidden,
			wantCode:   "ERR_AUTH_FORBIDDEN",
		},
		{
			name:       "no subject returns 401",
			subject:    "",
			wantStatus: http.StatusUnauthorized,
			wantCode:   "ERR_AUTH_UNAUTHORIZED",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := setupCommandHandler()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/devices/dev-1/commands", strings.NewReader(`{"payload":"reboot"}`))
			req.Header.Set("Content-Type", "application/json")
			req.SetPathValue("id", "dev-1")
			if tc.subject != "" {
				req = req.WithContext(auth.TestContext(tc.subject, tc.roles))
			}
			h.HandleEnqueue(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantCode != "" {
				assert.Contains(t, w.Body.String(), tc.wantCode)
			}
		})
	}
}

func TestHandleListPending_InvalidLimit(t *testing.T) {
	h, _, _ := setupCommandHandler()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/devices/dev-1/commands?limit=abc", nil)
	req.SetPathValue("id", "dev-1")
	req = req.WithContext(auth.TestContext("dev-1", nil))
	h.HandleListPending(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_VALIDATION_FAILED")
}

func TestHandleListPending_ExceedsMaxLimit(t *testing.T) {
	h, _, _ := setupCommandHandler()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/devices/dev-1/commands?limit=501", nil)
	req.SetPathValue("id", "dev-1")
	req = req.WithContext(auth.TestContext("dev-1", nil))
	h.HandleListPending(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_PAGE_SIZE_EXCEEDED")
}

func TestHandleListPending(t *testing.T) {
	tests := []struct {
		name       string
		deviceID   string
		seedCmds   int
		wantStatus int
		wantLen    int
	}{
		{
			name:       "returns pending commands",
			deviceID:   "dev-1",
			seedCmds:   2,
			wantStatus: http.StatusOK,
			wantLen:    2,
		},
		{
			name:       "no pending returns empty list",
			deviceID:   "dev-1",
			seedCmds:   0,
			wantStatus: http.StatusOK,
			wantLen:    0,
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
			// Device authenticates as itself (self-access).
			req = req.WithContext(auth.TestContext(tc.deviceID, nil))
			h.HandleListPending(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantStatus == http.StatusOK {
				var resp map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				data, ok := resp["data"].([]any)
				require.True(t, ok, "response should have data array")
				assert.Len(t, data, tc.wantLen)
				assert.Equal(t, false, resp["hasMore"])
			}
		})
	}
}

func TestHandleListPending_Pagination_FullTraversal(t *testing.T) {
	h, _, cmdRepo := setupCommandHandler()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		require.NoError(t, cmdRepo.Create(ctx, &domain.Command{
			ID:        "cmd-" + string(rune('a'+i)),
			DeviceID:  "dev-1",
			Payload:   "p",
			Status:    "pending",
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
		}))
	}

	var allIDs []string
	cursor := ""

	for page := 0; page < 10; page++ {
		url := "/devices/dev-1/commands?limit=3"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.SetPathValue("id", "dev-1")
		req = req.WithContext(auth.TestContext("dev-1", nil))
		h.HandleListPending(w, req)

		require.Equal(t, http.StatusOK, w.Code)
		var resp map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		data := resp["data"].([]any)
		for _, item := range data {
			m := item.(map[string]any)
			id, ok := m["id"].(string)
			require.True(t, ok, "response item should have string 'id' field")
			allIDs = append(allIDs, id)
		}

		hasMore := resp["hasMore"].(bool)
		if !hasMore {
			break
		}
		cursor = resp["nextCursor"].(string)
		require.NotEmpty(t, cursor)
	}

	// All 7 commands collected, no duplicates
	assert.Len(t, allIDs, 7)
	seen := make(map[string]bool)
	for _, id := range allIDs {
		assert.False(t, seen[id], "duplicate ID: %s", id)
		seen[id] = true
	}
}

func TestHandleListPending_InvalidCursor(t *testing.T) {
	codec := testCodec()

	wrongSort := []query.SortColumn{{Name: "other", Direction: query.SortASC}, {Name: "x", Direction: query.SortASC}}
	missingFieldsToken, _ := codec.Encode(query.Cursor{Values: []any{"v1", "v2"}})
	crossContextToken, _ := codec.Encode(query.Cursor{
		Values:  []any{"v1", "v2"},
		Scope:   query.SortScope(wrongSort),
		Context: query.QueryContext("endpoint", "wrong-endpoint"),
	})

	tests := []struct {
		name   string
		cursor string
	}{
		{"garbage token", "not-a-valid-cursor!!!"},
		{"missing scope and context", missingFieldsToken},
		{"cross-context replay", crossContextToken},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := setupCommandHandler()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/devices/dev-1/commands?cursor="+tc.cursor, nil)
			req.SetPathValue("id", "dev-1")
			req = req.WithContext(auth.TestContext("dev-1", nil))
			h.HandleListPending(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), "ERR_CURSOR_INVALID")
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
			// Device authenticates as itself.
			req = req.WithContext(auth.TestContext(tc.deviceID, nil))
			h.HandleAck(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantStatus == http.StatusOK {
				var envelope map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &envelope))
				data, ok := envelope["data"].(map[string]any)
				require.True(t, ok, "response should have data envelope")
				assert.Equal(t, "acked", data["status"])
			}
		})
	}
}

func TestCommandResponse_AckedAt_Serialization(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	t.Run("pending command omits ackedAt", func(t *testing.T) {
		resp := toCommandResponse(&domain.Command{
			ID: "cmd-1", DeviceID: "dev-1", Payload: "reboot",
			Status: "pending", CreatedAt: now, AckedAt: nil,
		})
		b, err := json.Marshal(resp)
		require.NoError(t, err)
		assert.NotContains(t, string(b), `"ackedAt"`)
	})

	t.Run("acked command includes ackedAt", func(t *testing.T) {
		ackedAt := now.Add(time.Minute)
		resp := toCommandResponse(&domain.Command{
			ID: "cmd-1", DeviceID: "dev-1", Payload: "reboot",
			Status: "acked", CreatedAt: now, AckedAt: &ackedAt,
		})
		b, err := json.Marshal(resp)
		require.NoError(t, err)
		assert.Contains(t, string(b), `"ackedAt"`)
	})
}

// Trust boundary tests (#27p)
func TestHandleListPending_DeviceIDOR(t *testing.T) {
	tests := []struct {
		name       string
		deviceID   string
		subject    string
		roles      []string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "self-access allowed",
			deviceID:   "dev-1",
			subject:    "dev-1",
			wantStatus: http.StatusOK,
		},
		{
			name:       "admin bypass allowed",
			deviceID:   "dev-1",
			subject:    "operator-1",
			roles:      []string{"admin"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "different device returns 403",
			deviceID:   "dev-1",
			subject:    "dev-2",
			roles:      []string{"device"},
			wantStatus: http.StatusForbidden,
			wantCode:   "ERR_AUTH_FORBIDDEN",
		},
		{
			name:       "no subject returns 401",
			deviceID:   "dev-1",
			subject:    "",
			wantStatus: http.StatusUnauthorized,
			wantCode:   "ERR_AUTH_UNAUTHORIZED",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := setupCommandHandler()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/devices/"+tc.deviceID+"/commands", nil)
			req.SetPathValue("id", tc.deviceID)
			if tc.subject != "" {
				req = req.WithContext(auth.TestContext(tc.subject, tc.roles))
			}
			h.HandleListPending(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantCode != "" {
				assert.Contains(t, w.Body.String(), tc.wantCode)
			}
		})
	}
}

func TestHandleAck_DeviceIDOR(t *testing.T) {
	tests := []struct {
		name       string
		subject    string
		roles      []string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "self-access allowed",
			subject:    "dev-1",
			wantStatus: http.StatusOK,
		},
		{
			name:       "admin bypass allowed",
			subject:    "operator-1",
			roles:      []string{"admin"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "different device returns 403",
			subject:    "dev-2",
			roles:      []string{"device"},
			wantStatus: http.StatusForbidden,
			wantCode:   "ERR_AUTH_FORBIDDEN",
		},
		{
			name:       "no subject returns 401",
			subject:    "",
			wantStatus: http.StatusUnauthorized,
			wantCode:   "ERR_AUTH_UNAUTHORIZED",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _, cmdRepo := setupCommandHandler()
			_ = cmdRepo.Create(context.Background(), &domain.Command{
				ID: "cmd-idor", DeviceID: "dev-1", Payload: "reboot", Status: "pending",
			})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/devices/dev-1/commands/cmd-idor/ack", nil)
			req.SetPathValue("id", "dev-1")
			req.SetPathValue("cmdId", "cmd-idor")
			if tc.subject != "" {
				req = req.WithContext(auth.TestContext(tc.subject, tc.roles))
			}
			h.HandleAck(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantCode != "" {
				assert.Contains(t, w.Body.String(), tc.wantCode)
			}
		})
	}
}
