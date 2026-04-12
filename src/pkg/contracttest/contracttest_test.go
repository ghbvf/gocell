package contracttest

import (
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
)

func testdataRoot() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "testdata", "contracts")
}

func TestLoad_HTTPContract(t *testing.T) {
	dir := filepath.Join(testdataRoot(), "http", "test", "valid", "v1")
	c := Load(t, dir)

	if c.ID != "http.test.valid.v1" {
		t.Errorf("ID = %q, want %q", c.ID, "http.test.valid.v1")
	}
	if c.Kind != "http" {
		t.Errorf("Kind = %q, want %q", c.Kind, "http")
	}
	if c.OwnerCell != "test-cell" {
		t.Errorf("OwnerCell = %q, want %q", c.OwnerCell, "test-cell")
	}
	if c.requestSchema == nil {
		t.Fatal("requestSchema should be compiled, got nil")
	}
	if c.responseSchema == nil {
		t.Fatal("responseSchema should be compiled, got nil")
	}
	if c.HTTP == nil {
		t.Fatal("HTTP transport metadata should be loaded, got nil")
	}
	if c.HTTP.Method != "POST" {
		t.Errorf("HTTP.Method = %q, want %q", c.HTTP.Method, "POST")
	}
	if c.HTTP.Path != "/api/v1/test/valid" {
		t.Errorf("HTTP.Path = %q, want %q", c.HTTP.Path, "/api/v1/test/valid")
	}
	if c.HTTP.SuccessStatus != 200 {
		t.Errorf("HTTP.SuccessStatus = %d, want %d", c.HTTP.SuccessStatus, 200)
	}
	if c.HTTP.NoContent {
		t.Error("HTTP.NoContent = true, want false")
	}
}

func TestLoad_EventContract(t *testing.T) {
	dir := filepath.Join(testdataRoot(), "event", "test", "valid", "v1")
	c := Load(t, dir)

	if c.ID != "event.test.valid.v1" {
		t.Errorf("ID = %q, want %q", c.ID, "event.test.valid.v1")
	}
	if c.Kind != "event" {
		t.Errorf("Kind = %q, want %q", c.Kind, "event")
	}
	if c.payloadSchema == nil {
		t.Fatal("payloadSchema should be compiled, got nil")
	}
	if c.headersSchema == nil {
		t.Fatal("headersSchema should be compiled, got nil")
	}
}

func TestLoadByID(t *testing.T) {
	c := LoadByID(t, testdataRoot(), "http.test.valid.v1")
	if c.ID != "http.test.valid.v1" {
		t.Errorf("ID = %q, want %q", c.ID, "http.test.valid.v1")
	}
}

func TestValidateRequest_Valid(t *testing.T) {
	c := LoadByID(t, testdataRoot(), "http.test.valid.v1")
	c.ValidateRequest(t, []byte(`{"username":"alice","email":"alice@example.com"}`))
}

func TestValidateRequest_Invalid(t *testing.T) {
	c := LoadByID(t, testdataRoot(), "http.test.valid.v1")

	// Use a sub-test so the expected failure doesn't fail the parent.
	mockT := &mockTB{}
	c.ValidateRequest(mockT, []byte(`{"username":"alice"}`)) // missing required "email"
	if !mockT.failed {
		t.Error("expected validation to fail for missing required field, but it passed")
	}
}

func TestValidateResponse_Valid(t *testing.T) {
	c := LoadByID(t, testdataRoot(), "http.test.valid.v1")
	c.ValidateResponse(t, []byte(`{"data":{"id":"1","username":"alice"}}`))
}

func TestValidateResponse_Invalid(t *testing.T) {
	c := LoadByID(t, testdataRoot(), "http.test.valid.v1")

	mockT := &mockTB{}
	c.ValidateResponse(mockT, []byte(`{"wrong":"shape"}`)) // missing required "data"
	if !mockT.failed {
		t.Error("expected validation to fail for missing required field, but it passed")
	}
}

func TestValidateHTTPResponseRecorder_Valid(t *testing.T) {
	c := LoadByID(t, testdataRoot(), "http.test.valid.v1")
	recorder := httptest.NewRecorder()
	recorder.WriteHeader(200)
	_, _ = recorder.Write([]byte(`{"data":{"id":"1","username":"alice"}}`))
	c.ValidateHTTPResponseRecorder(t, recorder)
}

func TestValidateHTTPResponseRecorder_InvalidStatus(t *testing.T) {
	c := LoadByID(t, testdataRoot(), "http.test.valid.v1")
	recorder := httptest.NewRecorder()
	recorder.WriteHeader(201)
	_, _ = recorder.Write([]byte(`{"data":{"id":"1","username":"alice"}}`))

	mockT := &mockTB{}
	c.ValidateHTTPResponseRecorder(mockT, recorder)
	if !mockT.failed {
		t.Error("expected status mismatch to fail validation, but it passed")
	}
}

func TestValidateHTTPResponseRecorder_RequiresBodyForSchema(t *testing.T) {
	c := LoadByID(t, testdataRoot(), "http.test.valid.v1")
	recorder := httptest.NewRecorder()
	recorder.WriteHeader(200)

	mockT := &mockTB{}
	c.ValidateHTTPResponseRecorder(mockT, recorder)
	if !mockT.failed {
		t.Error("expected empty body to fail validation when response schema exists")
	}
}

func TestValidateHTTPResponseRecorder_NoContentAcceptsEmptyBody(t *testing.T) {
	c := &Contract{
		ID:   "http.test.delete.v1",
		Kind: "http",
		HTTP: &HTTPTransport{SuccessStatus: 204, NoContent: true},
	}
	recorder := httptest.NewRecorder()
	recorder.WriteHeader(204)
	c.ValidateHTTPResponseRecorder(t, recorder)
}

func TestValidateHTTPResponseRecorder_NoContentRejectsBody(t *testing.T) {
	c := &Contract{
		ID:   "http.test.delete.v1",
		Kind: "http",
		HTTP: &HTTPTransport{SuccessStatus: 204, NoContent: true},
	}
	recorder := httptest.NewRecorder()
	recorder.WriteHeader(204)
	_, _ = recorder.Write([]byte(`{"unexpected":true}`))

	mockT := &mockTB{}
	c.ValidateHTTPResponseRecorder(mockT, recorder)
	if !mockT.failed {
		t.Error("expected non-empty body to fail no-content validation")
	}
}

func TestValidateHTTPResponseRecorder_RequiresTransportMetadata(t *testing.T) {
	c := LoadByID(t, testdataRoot(), "http.test.valid.v1")
	c.HTTP = nil
	recorder := httptest.NewRecorder()
	recorder.WriteHeader(200)
	_, _ = recorder.Write([]byte(`{"data":{"id":"1","username":"alice"}}`))

	mockT := &mockTB{}
	c.ValidateHTTPResponseRecorder(mockT, recorder)
	if !mockT.failed {
		t.Error("expected missing endpoints.http metadata to fail validation")
	}
}

func TestValidatePayload_Valid(t *testing.T) {
	c := LoadByID(t, testdataRoot(), "event.test.valid.v1")
	c.ValidatePayload(t, []byte(`{"key":"k","value":"v"}`))
}

func TestValidatePayload_Invalid(t *testing.T) {
	c := LoadByID(t, testdataRoot(), "event.test.valid.v1")

	mockT := &mockTB{}
	c.ValidatePayload(mockT, []byte(`{"key":"k"}`)) // missing required "value"
	if !mockT.failed {
		t.Error("expected validation to fail for missing required field, but it passed")
	}
}

func TestValidateHeaders_Valid(t *testing.T) {
	c := LoadByID(t, testdataRoot(), "event.test.valid.v1")
	c.ValidateHeaders(t, []byte(`{"event_id":"evt-123"}`))
}

func TestValidateHeaders_Invalid(t *testing.T) {
	c := LoadByID(t, testdataRoot(), "event.test.valid.v1")

	mockT := &mockTB{}
	c.ValidateHeaders(mockT, []byte(`{}`)) // missing required "event_id"
	if !mockT.failed {
		t.Error("expected validation to fail for missing required field, but it passed")
	}
}

func TestMustRejectRequest(t *testing.T) {
	c := LoadByID(t, testdataRoot(), "http.test.valid.v1")
	// Extra field should be rejected by additionalProperties: false
	c.MustRejectRequest(t, []byte(`{"username":"alice","email":"a@b.com","extra":"bad"}`))
}

func TestMustRejectRequest_PassesOnValid(t *testing.T) {
	c := LoadByID(t, testdataRoot(), "http.test.valid.v1")

	mockT := &mockTB{}
	c.MustRejectRequest(mockT, []byte(`{"username":"alice","email":"alice@example.com"}`))
	if !mockT.failed {
		t.Error("expected MustRejectRequest to fail when schema accepts the data")
	}
}

func TestMustRejectPayload(t *testing.T) {
	c := LoadByID(t, testdataRoot(), "event.test.valid.v1")
	c.MustRejectPayload(t, []byte(`{"key":"k"}`)) // missing required "value"
}

func TestMustRejectHeaders(t *testing.T) {
	c := LoadByID(t, testdataRoot(), "event.test.valid.v1")
	c.MustRejectHeaders(t, []byte(`{}`)) // missing required "event_id"
}

func TestValidateRequest_NoSchema(t *testing.T) {
	// Event contracts have no request schema — validation should be a no-op.
	c := LoadByID(t, testdataRoot(), "event.test.valid.v1")
	c.ValidateRequest(t, []byte(`{"anything":"goes"}`))
}

func TestContractsRoot(t *testing.T) {
	root := ContractsRoot()
	if !filepath.IsAbs(root) {
		t.Errorf("ContractsRoot() returned non-absolute path: %s", root)
	}
	if filepath.Base(root) != "contracts" {
		t.Errorf("ContractsRoot() should end with 'contracts', got: %s", root)
	}
}

// mockTB captures test failure without actually failing the parent test.
type mockTB struct {
	testing.TB
	failed bool
	logs   []string
}

func (m *mockTB) Helper() {}

func (m *mockTB) Errorf(format string, args ...any) {
	m.failed = true
}

func (m *mockTB) Fatalf(format string, args ...any) {
	m.failed = true
	// In a real test this would stop execution; here we just record.
	// Tests using mockTB for Fatal should check after a single call.
	panic("mockTB.Fatalf called")
}

func (m *mockTB) Log(args ...any)                 {}
func (m *mockTB) Logf(format string, args ...any) {}
