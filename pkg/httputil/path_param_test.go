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

	tests := []uuidPathParamCase{
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
		{
			// google/uuid.Parse silently accepts brace-wrapped Microsoft GUIDs
			// (length 38). The strict canonical helper rejects them so on-the-wire
			// forms match contract.yaml `pathParams.format: uuid`.
			name:       "brace wrapped rejected",
			paramName:  "id",
			raw:        "{" + validUUID + "}",
			wantOK:     false,
			wantStatus: http.StatusBadRequest,
			wantCode:   string(errcode.ErrValidationInvalidUUID),
		},
		{
			// google/uuid.Parse silently accepts urn:uuid: prefixed form (length 45).
			name:       "urn prefix rejected",
			paramName:  "id",
			raw:        "urn:uuid:" + validUUID,
			wantOK:     false,
			wantStatus: http.StatusBadRequest,
			wantCode:   string(errcode.ErrValidationInvalidUUID),
		},
		{
			// Length 38 (1 leading + 36 + 1 trailing space) collides with the
			// brace-dispatch branch in google/uuid v1.6 and would otherwise pass.
			// Strict canonical helper rejects it via the length 32/36 check.
			name:       "leading and trailing space rejected",
			paramName:  "id",
			raw:        " " + validUUID + " ",
			wantOK:     false,
			wantStatus: http.StatusBadRequest,
			wantCode:   string(errcode.ErrValidationInvalidUUID),
		},
		{
			name:       "trailing whitespace rejected",
			paramName:  "id",
			raw:        validUUID + " ",
			wantOK:     false,
			wantStatus: http.StatusBadRequest,
			wantCode:   string(errcode.ErrValidationInvalidUUID),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runParseUUIDPathParamCase(t, tt)
		})
	}
}

type uuidPathParamCase struct {
	name       string
	paramName  string
	raw        string
	wantOK     bool
	wantStatus int
	wantValue  string
	wantCode   string
}

func runParseUUIDPathParamCase(t *testing.T, tc uuidPathParamCase) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.SetPathValue(tc.paramName, tc.raw)
	rec := httptest.NewRecorder()

	got, ok := httputil.ParseUUIDPathParam(rec, req, tc.paramName)
	if ok != tc.wantOK {
		t.Fatalf("ok = %v, want %v (rec.Code=%d body=%s)", ok, tc.wantOK, rec.Code, rec.Body.String())
	}
	if ok {
		if got != tc.wantValue {
			t.Fatalf("value = %q, want %q", got, tc.wantValue)
		}
		return
	}
	assertUUIDPathParamError(t, rec, tc)
}

func assertUUIDPathParamError(t *testing.T, rec *httptest.ResponseRecorder, tc uuidPathParamCase) {
	t.Helper()
	if rec.Code != tc.wantStatus {
		t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details []struct {
				Key   string `json:"key"`
				Value any    `json:"value"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (raw=%s)", err, rec.Body.String())
	}
	if body.Error.Code != tc.wantCode {
		t.Fatalf("error.code = %q, want %q", body.Error.Code, tc.wantCode)
	}
	// param name is in Details[key="param"], not in Message
	if tc.paramName == "" {
		return
	}
	for _, d := range body.Error.Details {
		if d.Key == "param" && d.Value == tc.paramName {
			return
		}
	}
	t.Fatalf("error.details does not contain param=%q, body=%s", tc.paramName, rec.Body.String())
}
