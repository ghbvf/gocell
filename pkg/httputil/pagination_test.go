package httputil_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/query"
)

func TestParsePageRequestOrWrite(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantOK     bool
		wantLimit  int
		wantStatus int
	}{
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

			pr, ok := httputil.ParsePageRequestOrWrite(w, r)

			if ok != tc.wantOK {
				t.Errorf("ok=%v, want %v", ok, tc.wantOK)
			}
			if tc.wantOK && pr.Limit != tc.wantLimit {
				t.Errorf("Limit=%d, want %d", pr.Limit, tc.wantLimit)
			}
			if !tc.wantOK && w.Code != tc.wantStatus {
				t.Errorf("HTTP status=%d, want %d", w.Code, tc.wantStatus)
			}
			if tc.wantOK && w.Code != 0 && w.Code != http.StatusOK {
				t.Errorf("unexpected write on ok=true: HTTP status=%d", w.Code)
			}
		})
	}
}
