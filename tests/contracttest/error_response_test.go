package contracttest

import (
	"path/filepath"
	"runtime"
	"testing"
)

// errorTestContractsRoot returns the path to testdata contracts for error response tests.
func errorTestContractsRoot(t testing.TB) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("contracttest: runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "testdata", "contracts")
}

// TestValidateErrorResponse exercises ValidateErrorResponse against the
// http.test.errresp.v1 contract. The contract ID is a compile-time literal
// (CONTRACTTEST-LOADBYID-LITERAL-01 requirement); cases that need a different
// contract live in their own test function below.
func TestValidateErrorResponse(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     []byte
		wantFail bool
		wantMsg  string
	}{
		{
			name:     "valid 401 body against schema",
			status:   401,
			body:     []byte(`{"error":{"code":"ERR_AUTH_INVALID_TOKEN","message":"token expired","details":[]}}`),
			wantFail: false,
		},
		{
			name:     "invalid body missing code",
			status:   401,
			body:     []byte(`{"error":{"message":"token expired","details":[]}}`),
			wantFail: true,
		},
		{
			name:     "status with no entry in contract",
			status:   500,
			body:     []byte(`{"error":{"code":"ERR_INTERNAL","message":"oops","details":[]}}`),
			wantFail: true,
			wantMsg:  "no response declared for status 500",
		},
		{
			name:     "status with empty schemaRef",
			status:   429,
			body:     []byte(`{}`),
			wantFail: true,
			wantMsg:  "empty schemaRef",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			c := LoadByID(t, errorTestContractsRoot(t), "http.test.errresp.v1")
			mockT := &mockTB{}
			c.ValidateErrorResponse(mockT, tt.status, tt.body)
			if tt.wantFail && !mockT.failed {
				t.Errorf("expected ValidateErrorResponse to fail but it passed")
			}
			if !tt.wantFail && mockT.failed {
				t.Errorf("expected ValidateErrorResponse to pass but it failed")
			}
			if tt.wantMsg != "" && !mockT.containsMsg(tt.wantMsg) {
				t.Errorf("expected error containing %q, got %v", tt.wantMsg, mockT.msgs)
			}
		})
	}
}

// TestValidateErrorResponse_NoHTTPEndpoint covers the single edge case of a
// contract without an endpoints.http block. Lives in its own function so the
// LoadByID argument can be a compile-time literal (CONTRACTTEST-LOADBYID-LITERAL-01).
func TestValidateErrorResponse_NoHTTPEndpoint(t *testing.T) {
	c := LoadByID(t, errorTestContractsRoot(t), "http.test.nohttp.v1")
	mockT := &mockTB{}
	c.ValidateErrorResponse(mockT, 401,
		[]byte(`{"error":{"code":"ERR_AUTH_INVALID_TOKEN","message":"unauthorized","details":[]}}`))
	if !mockT.failed {
		t.Errorf("expected ValidateErrorResponse to fail but it passed")
	}
	if !mockT.containsMsg("no endpoints.http") {
		t.Errorf("expected error containing %q, got %v", "no endpoints.http", mockT.msgs)
	}
}
