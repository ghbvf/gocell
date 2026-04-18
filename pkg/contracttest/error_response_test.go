package contracttest

import (
	"path/filepath"
	"runtime"
	"testing"
)

// errorTestContractsRoot returns the path to testdata contracts for error response tests.
func errorTestContractsRoot() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("contracttest: runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "testdata", "contracts")
}

// TestValidateErrorResponse exercises the ValidateErrorResponse helper.
func TestValidateErrorResponse(t *testing.T) {
	tests := []struct {
		name       string
		contractID string
		status     int
		body       []byte
		wantFail   bool
		wantMsg    string
	}{
		{
			name:       "valid 401 body against schema",
			contractID: "http.test.errresp.v1",
			status:     401,
			body:       []byte(`{"error":{"code":"ERR_AUTH_INVALID_TOKEN","message":"token expired","details":{}}}`),
			wantFail:   false,
		},
		{
			name:       "invalid body missing code",
			contractID: "http.test.errresp.v1",
			status:     401,
			body:       []byte(`{"error":{"message":"token expired","details":{}}}`),
			wantFail:   true,
		},
		{
			name:       "status with no entry in contract",
			contractID: "http.test.errresp.v1",
			status:     500,
			body:       []byte(`{"error":{"code":"ERR_INTERNAL","message":"oops","details":{}}}`),
			wantFail:   true,
			wantMsg:    "no response declared for status 500",
		},
		{
			name:       "contract with no endpoints.http",
			contractID: "http.test.nohttp.v1",
			status:     401,
			body:       []byte(`{"error":{"code":"ERR_AUTH_INVALID_TOKEN","message":"unauthorized","details":{}}}`),
			wantFail:   true,
			wantMsg:    "no endpoints.http",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := LoadByID(t, errorTestContractsRoot(), tt.contractID)
			mockT := &mockTB{}
			c.ValidateErrorResponse(mockT, tt.status, tt.body)
			if tt.wantFail && !mockT.failed {
				t.Errorf("expected ValidateErrorResponse to fail but it passed")
			}
			if !tt.wantFail && mockT.failed {
				t.Errorf("expected ValidateErrorResponse to pass but it failed")
			}
		})
	}
}
