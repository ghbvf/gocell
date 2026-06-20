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
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	statuscontract "github.com/ghbvf/gocell/generated/contracts/http/device/status/v1"
)

// testStatusAuthorizer is a test-local PDP for devicestatus handler unit tests.
// It mirrors the device:read baseline in cells/devicecell/authorizer.go;
// deviceAuthorizer is package-private to devicecell, so this package
// implements an equivalent locally.
type testStatusAuthorizer struct{}

func (testStatusAuthorizer) Authorize(ctx context.Context, subject, resource, action string) (authz.Decision, error) {
	p, ok := auth.FromContext(ctx)
	if !ok || p == nil {
		return authz.Deny("test-status-authz: no authenticated principal"), nil
	}
	if action == authz.PermDeviceRead().String() {
		opOrAdmin := p.HasRole("admin") || p.HasRole("role:operator")
		if opOrAdmin || (subject != "" && subject == resource) {
			return authz.Allow(authz.Obligations{})
		}
	}
	return authz.Deny("test-status-authz: denied"), nil
}

// withStatusTestAuth builds a context carrying both a Principal and the status test PDP.
func withStatusTestAuth(subject string, roles []string) context.Context {
	return auth.WithAuthorizer(auth.TestContext(subject, roles), testStatusAuthorizer{})
}

func setupStatusHandler(t testing.TB) (*statuscontract.Handler, *mem.DeviceRepository) {
	t.Helper()
	repo := mem.NewDeviceRepository()
	svc, err := NewService(repo, slog.Default())
	if err != nil {
		t.Fatalf("setupStatusHandler: %v", err)
	}
	return statuscontract.NewHandler(svc, testResolver()), repo
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
			h, repo := setupStatusHandler(t)
			tc.setup(repo)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/devices/"+tc.deviceID+"/status", nil)
			req.SetPathValue("id", tc.deviceID)
			// Inject principal + authorizer: admin can read any device's status.
			req = req.WithContext(withStatusTestAuth("admin-1", []string{"admin"}))
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
	svc, err := NewService(repo, slog.Default())
	require.NoError(t, err)
	h := statuscontract.NewHandler(svc, testResolver())

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetPathValue("id", "dev-ts")
	// Device reads its own status (subject == resource).
	req = req.WithContext(withStatusTestAuth("dev-ts", nil))
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data := resp["data"].(map[string]any)
	assert.Equal(t, "2026-01-02T03:04:05Z", data["lastSeen"])
}
