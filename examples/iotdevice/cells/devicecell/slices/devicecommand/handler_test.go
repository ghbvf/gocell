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

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

func TestToCommandResponse_ZeroEntry(t *testing.T) {
	var got commandResponse
	assert.NotPanics(t, func() { got = toCommandResponse(command.Entry{}) })
	assert.Zero(t, got.ID)
}

// setupCommandHandler creates a Handler and seeds a device so that command operations succeed.
func setupCommandHandler() (*Handler, *mem.DeviceRepository, *commandtest.InMemQueue) {
	devRepo := mem.NewDeviceRepository()
	q := commandtest.NewInMemQueue()
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	svc, err := NewService(q, devRepo, codec, slog.Default(), query.RunModeProd)
	if err != nil {
		panic(err)
	}

	_ = devRepo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "sensor-a", Status: "online",
	})

	return NewHandler(svc), devRepo, q
}

// setupCommandMux creates a TestMux with policies registered via
// Secured, used by trust boundary tests so that policy checks run as in production.
func setupCommandMux() (http.Handler, *mem.DeviceRepository, *commandtest.InMemQueue) {
	h, devRepo, q := setupCommandHandler()
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/devices", func(sub cell.RouteMux) { h.RegisterRoutes(sub) })
	return mux, devRepo, q
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
				assert.Equal(t, "default", data["commandType"])
				assert.InDelta(t, float64(0), data["attempt"], 0)
				assert.NotEmpty(t, data["createdAt"])
				_, hasCompleted := data["completedAt"]
				assert.False(t, hasCompleted, "completedAt should be omitted for pending command")
			},
		},
		{
			name:       "valid enqueue with commandType returns 201",
			deviceID:   "dev-1",
			body:       `{"payload":"v2.0","commandType":"firmware-update"}`,
			wantStatus: http.StatusCreated,
			checkBody: func(t *testing.T, body []byte) {
				var envelope map[string]any
				require.NoError(t, json.Unmarshal(body, &envelope))
				data := envelope["data"].(map[string]any)
				assert.Equal(t, "firmware-update", data["commandType"])
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
			req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/"+tc.deviceID+"/commands", strings.NewReader(tc.body))
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

func TestHandleEnqueue_RoutePolicy(t *testing.T) {
	tests := []struct {
		name       string
		subject    string
		roles      []string
		wantStatus int
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
			name:       "device role denied",
			subject:    "dev-99",
			roles:      []string{"device"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "no roles denied",
			subject:    "user-1",
			roles:      nil,
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mux, _, _ := setupCommandMux()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/dev-1/commands", strings.NewReader(`{"payload":"reboot"}`))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(auth.TestContext(tc.subject, tc.roles))
			mux.ServeHTTP(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
		})
	}
}

func TestHandleDequeue_InvalidLimit(t *testing.T) {
	h, _, _ := setupCommandHandler()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices/dev-1/commands?limit=abc", nil)
	req.SetPathValue("id", "dev-1")
	req = req.WithContext(auth.TestContext("dev-1", nil))
	h.HandleDequeue(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_VALIDATION_FAILED")
}

func TestHandleDequeue_ExceedsMaxLimit(t *testing.T) {
	h, _, _ := setupCommandHandler()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/devices/dev-1/commands?limit=501", nil)
	req.SetPathValue("id", "dev-1")
	req = req.WithContext(auth.TestContext("dev-1", nil))
	h.HandleDequeue(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_PAGE_SIZE_EXCEEDED")
}

func TestHandleDequeue(t *testing.T) {
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
			h, _, q := setupCommandHandler()
			ctx := context.Background()
			now := time.Now()
			for i := range tc.seedCmds {
				id := "cmd-" + string(rune('a'+i))
				_ = q.Enqueue(ctx, command.NewEntry(id, "dev-1", "reboot", []byte("p"), command.Timeouts{}, now.Add(time.Duration(i)*time.Second)), command.EnqueueOptions{})
			}

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/devices/"+tc.deviceID+"/commands", nil)
			req.SetPathValue("id", tc.deviceID)
			// Device authenticates as itself (self-access).
			req = req.WithContext(auth.TestContext(tc.deviceID, nil))
			h.HandleDequeue(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantStatus == http.StatusOK {
				var resp map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				data, ok := resp["data"].([]any)
				require.True(t, ok, "response should have data array")
				assert.Len(t, data, tc.wantLen)
				assert.Equal(t, false, resp["hasMore"])
				assert.Equal(t, "", resp["nextCursor"])
				for _, item := range data {
					m := item.(map[string]any)
					assert.Equal(t, "sent", m["status"])
					assert.NotEmpty(t, m["sentAt"])
				}
			}
		})
	}
}

func TestHandleDequeue_ClaimBatches(t *testing.T) {
	h, _, q := setupCommandHandler()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		id := "cmd-" + string(rune('a'+i))
		require.NoError(t, q.Enqueue(ctx, command.NewEntry(id, "dev-1", "reboot", []byte("p"), command.Timeouts{}, base.Add(time.Duration(i)*time.Hour)), command.EnqueueOptions{}))
	}

	var allIDs []string

	for page := 0; page < 3; page++ {
		url := "/api/v1/devices/dev-1/commands?limit=3"
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.SetPathValue("id", "dev-1")
		req = req.WithContext(auth.TestContext("dev-1", nil))
		h.HandleDequeue(w, req)

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

		if len(data) == 0 {
			break
		}
	}

	// All 7 commands collected, no duplicates
	assert.Len(t, allIDs, 7)
	seen := make(map[string]bool)
	for _, id := range allIDs {
		assert.False(t, seen[id], "duplicate ID: %s", id)
		seen[id] = true
	}
}

func TestHandleScanActive_InvalidCursor(t *testing.T) {
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
			req := httptest.NewRequest(http.MethodGet, "/internal/v1/devicecommands?deviceId=dev-1&cursor="+tc.cursor, nil)
			req = req.WithContext(auth.TestContext(auth.ServiceNameInternal, []string{auth.RoleInternalAdmin}))
			h.HandleScanActive(w, req)

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
			name:       "ack sent command returns 200",
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
			h, _, q := setupCommandHandler()
			if tc.seedCmd {
				ctx := context.Background()
				_ = q.Enqueue(ctx, command.NewEntry(tc.cmdID, tc.deviceID, "reboot", []byte("x"), command.Timeouts{}, time.Now()), command.EnqueueOptions{})
				_, _ = q.Dequeue(ctx, tc.deviceID, 1, command.DefaultLeaseDuration)
			}

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/"+tc.deviceID+"/commands/"+tc.cmdID+"/ack", strings.NewReader(`{"reason":"success"}`))
			req.Header.Set("Content-Type", "application/json")
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
				// Ack now returns the full command entry DTO with terminal status.
				assert.Equal(t, "succeeded", data["status"])
				assert.NotNil(t, data["completedAt"])
			}
		})
	}
}

func TestHandleAck_RejectsTimeoutReason(t *testing.T) {
	h, _, q := setupCommandHandler()
	ctx := context.Background()
	require.NoError(t, q.Enqueue(ctx,
		command.NewEntry("cmd-timeout", "dev-1", "reboot", []byte("x"), command.Timeouts{}, time.Now()),
		command.EnqueueOptions{}))
	_, err := q.Dequeue(ctx, "dev-1", 1, command.DefaultLeaseDuration)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/devices/dev-1/commands/cmd-timeout/ack", strings.NewReader(`{"reason":"timeout"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "dev-1")
	req.SetPathValue("cmdId", "cmd-timeout")
	req = req.WithContext(auth.TestContext("dev-1", nil))
	h.HandleAck(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	got, err := q.GetCommand(ctx, "cmd-timeout")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, command.StatusSent, got.Status)
}

func TestHandleAck_RejectsFailedAlias(t *testing.T) {
	h, _, q := setupCommandHandler()
	ctx := context.Background()
	require.NoError(t, q.Enqueue(ctx,
		command.NewEntry("cmd-failed-alias", "dev-1", "reboot", []byte("x"), command.Timeouts{}, time.Now()),
		command.EnqueueOptions{}))
	_, err := q.Dequeue(ctx, "dev-1", 1, command.DefaultLeaseDuration)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/devices/dev-1/commands/cmd-failed-alias/ack", strings.NewReader(`{"reason":"failed"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "dev-1")
	req.SetPathValue("cmdId", "cmd-failed-alias")
	req = req.WithContext(auth.TestContext("dev-1", nil))
	h.HandleAck(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	got, err := q.GetCommand(ctx, "cmd-failed-alias")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, command.StatusSent, got.Status)
}

func TestCommandResponse_CompletedAt_Serialization(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	t.Run("pending command omits completedAt", func(t *testing.T) {
		entry := command.Entry{
			ID:        "cmd-1",
			DeviceID:  "dev-1",
			Payload:   []byte("reboot"),
			Status:    command.StatusPending,
			CreatedAt: now,
		}
		entry.CommandType = "default"
		resp := toCommandResponse(entry)
		b, err := json.Marshal(resp)
		require.NoError(t, err)
		assert.NotContains(t, string(b), `"completedAt"`)
	})

	t.Run("succeeded command includes completedAt", func(t *testing.T) {
		completedAt := now.Add(time.Minute)
		entry := command.Entry{
			ID:          "cmd-1",
			DeviceID:    "dev-1",
			CommandType: "default",
			Payload:     []byte("reboot"),
			Status:      command.StatusSucceeded,
			CreatedAt:   now,
			CompletedAt: &completedAt,
		}
		resp := toCommandResponse(entry)
		b, err := json.Marshal(resp)
		require.NoError(t, err)
		assert.Contains(t, string(b), `"completedAt"`)
	})
}

func TestHandleDequeue_RoutePolicy(t *testing.T) {
	tests := []struct {
		name       string
		deviceID   string
		subject    string
		roles      []string
		wantStatus int
	}{
		{
			name:       "self-access allowed",
			deviceID:   "dev-1",
			subject:    "dev-1",
			wantStatus: http.StatusOK,
		},
		{
			name:       "admin access allowed",
			deviceID:   "dev-1",
			subject:    "operator-1",
			roles:      []string{"admin"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "different device denied",
			deviceID:   "dev-1",
			subject:    "dev-2",
			roles:      []string{"device"},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mux, _, _ := setupCommandMux()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/devices/"+tc.deviceID+"/commands", nil)
			req = req.WithContext(auth.TestContext(tc.subject, tc.roles))
			mux.ServeHTTP(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
		})
	}
}

func TestHandleAck_RoutePolicy(t *testing.T) {
	tests := []struct {
		name       string
		subject    string
		roles      []string
		wantStatus int
	}{
		{
			name:       "self-access allowed",
			subject:    "dev-1",
			wantStatus: http.StatusOK,
		},
		{
			name:       "admin access allowed",
			subject:    "operator-1",
			roles:      []string{"admin"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "different device denied",
			subject:    "dev-2",
			roles:      []string{"device"},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mux, _, q := setupCommandMux()
			ctx := context.Background()
			_ = q.Enqueue(ctx, command.NewEntry("cmd-ack", "dev-1", "reboot", []byte("x"), command.Timeouts{}, time.Now()), command.EnqueueOptions{})
			_, _ = q.Dequeue(ctx, "dev-1", 1, command.DefaultLeaseDuration)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/dev-1/commands/cmd-ack/ack", strings.NewReader(`{"reason":"success"}`))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(auth.TestContext(tc.subject, tc.roles))
			mux.ServeHTTP(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
		})
	}
}
