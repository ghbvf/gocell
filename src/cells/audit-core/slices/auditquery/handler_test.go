package auditquery

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/cells/audit-core/internal/mem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleQuery_InvalidTimeFormat(t *testing.T) {
	repo := mem.NewAuditRepository()
	svc := NewService(repo, slog.Default())
	h := NewHandler(svc)

	tests := []struct {
		name       string
		query      string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "invalid from parameter",
			query:      "?from=not-a-date",
			wantStatus: http.StatusBadRequest,
			wantCode:   "ERR_VALIDATION_INVALID_TIME_FORMAT",
		},
		{
			name:       "invalid to parameter",
			query:      "?to=yesterday",
			wantStatus: http.StatusBadRequest,
			wantCode:   "ERR_VALIDATION_INVALID_TIME_FORMAT",
		},
		{
			name:       "valid RFC3339 from",
			query:      "?from=2024-01-01T00:00:00Z",
			wantStatus: http.StatusOK,
		},
		{
			name:       "no time params",
			query:      "",
			wantStatus: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/entries"+tc.query, nil)
			h.HandleQuery(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantCode != "" {
				var resp struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				assert.Equal(t, tc.wantCode, resp.Error.Code)
			}
		})
	}
}
