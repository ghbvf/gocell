package httputil_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

func TestParseUUIDPathParam(t *testing.T) {
	t.Parallel()

	const validUUID = "0e8d6e9a-3a6f-4b1f-9c1e-2a3b4c5d6e7f"

	tests := []struct {
		name       string
		paramName  string
		raw        string
		wantOK     bool
		wantStatus int
		wantValue  string
		wantCode   string
	}{
		{
			name:      "valid lowercase",
			paramName: "id",
			raw:       validUUID,
			wantOK:    true,
			wantValue: validUUID,
		},
		{
			name:      "valid uppercase normalized to lowercase",
			paramName: "id",
			raw:       strings.ToUpper(validUUID),
			wantOK:    true,
			wantValue: validUUID,
		},
		{
			name:       "empty string",
			paramName:  "id",
			raw:        "",
			wantOK:     false,
			wantStatus: http.StatusBadRequest,
			wantCode:   string(errcode.ErrValidationInvalidUUID),
		},
		{
			name:       "malformed",
			paramName:  "userID",
			raw:        "not-a-uuid",
			wantOK:     false,
			wantStatus: http.StatusBadRequest,
			wantCode:   string(errcode.ErrValidationInvalidUUID),
		},
		{
			name:       "leading whitespace",
			paramName:  "id",
			raw:        " " + validUUID,
			wantOK:     false,
			wantStatus: http.StatusBadRequest,
			wantCode:   string(errcode.ErrValidationInvalidUUID),
		},
		{
			name:      "missing hyphens",
			paramName: "id",
			raw:       strings.ReplaceAll(validUUID, "-", ""),
			wantOK:    true, // google/uuid accepts compact form; canonicalizes to dashed lowercase
			wantValue: validUUID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.SetPathValue(tt.paramName, tt.raw)
			rec := httptest.NewRecorder()

			got, ok := httputil.ParseUUIDPathParam(rec, req, tt.paramName)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (rec.Code=%d body=%s)", ok, tt.wantOK, rec.Code, rec.Body.String())
			}
			if !ok {
				if rec.Code != tt.wantStatus {
					t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
				}
				var body struct {
					Error struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode body: %v (raw=%s)", err, rec.Body.String())
				}
				if body.Error.Code != tt.wantCode {
					t.Fatalf("error.code = %q, want %q", body.Error.Code, tt.wantCode)
				}
				if !strings.Contains(body.Error.Message, tt.paramName) {
					t.Fatalf("error.message = %q, want substring %q", body.Error.Message, tt.paramName)
				}
				return
			}
			if got != tt.wantValue {
				t.Fatalf("value = %q, want %q", got, tt.wantValue)
			}
		})
	}
}
