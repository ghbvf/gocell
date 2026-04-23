package httputil_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/query"
)

type parsePageParamsCase struct {
	name       string
	query      string
	wantOK     bool
	wantLimit  int
	wantStatus int
}

func TestParsePageParamsOrWrite(t *testing.T) {
	tests := []parsePageParamsCase{
		{
			name:      "no params uses default",
			query:     "",
			wantOK:    true,
			wantLimit: query.DefaultPageSize,
		},
		{
			name:      "valid limit",
			query:     "limit=10",
			wantOK:    true,
			wantLimit: 10,
		},
		{
			name:       "limit exceeds max",
			query:      "limit=9999",
			wantOK:     false,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid limit not a number",
			query:      "limit=abc",
			wantOK:     false,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodGet, "/?"+tc.query, nil)

			pr, ok := httputil.ParsePageParamsOrWrite(w, r)

			assertParsePageParamsResult(t, tc, pr, ok, w)
		})
	}
}

func assertParsePageParamsResult(
	t *testing.T,
	tc parsePageParamsCase,
	pr query.PageParams,
	ok bool,
	w *httptest.ResponseRecorder,
) {
	t.Helper()

	if ok != tc.wantOK {
		t.Errorf("ok=%v, want %v", ok, tc.wantOK)
		return
	}
	if tc.wantOK {
		assertParsePageParamsSuccess(t, tc, pr, w)
		return
	}
	assertParsePageParamsError(t, tc, w)
}

func assertParsePageParamsSuccess(
	t *testing.T,
	tc parsePageParamsCase,
	pr query.PageParams,
	w *httptest.ResponseRecorder,
) {
	t.Helper()

	if pr.Limit != tc.wantLimit {
		t.Errorf("Limit=%d, want %d", pr.Limit, tc.wantLimit)
	}
	if w.Code != 0 && w.Code != http.StatusOK {
		t.Errorf("unexpected write on ok=true: HTTP status=%d", w.Code)
	}
}

func assertParsePageParamsError(t *testing.T, tc parsePageParamsCase, w *httptest.ResponseRecorder) {
	t.Helper()

	if w.Code != tc.wantStatus {
		t.Errorf("HTTP status=%d, want %d", w.Code, tc.wantStatus)
	}
	assertJSONErrorEnvelope(t, w)
}

func assertJSONErrorEnvelope(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()

	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Errorf("response body is not JSON: %v", err)
		return
	}
	if _, ok := body["error"]; !ok {
		t.Errorf("response body missing 'error' field: %s", w.Body.String())
	}
}
