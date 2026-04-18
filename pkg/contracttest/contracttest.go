// Package contracttest provides schema-driven contract validation helpers
// for use in contract_test.go files across GoCell cells.
//
// It loads contract.yaml files, compiles referenced JSON Schemas, and
// validates JSON payloads against them. This replaces the previous t.Skip
// stubs with real schema enforcement.
//
// Usage:
//
//	func TestHttpAuthUserCreateV1Serve(t *testing.T) {
//	    c := contracttest.LoadByID(t, contracttest.ContractsRoot(), "http.auth.user.create.v1")
//	    c.ValidateRequest(t, []byte(`{"username":"alice","email":"a@b.com","password":"s"}`))
//	    c.ValidateResponse(t, []byte(`{"data":{"id":"u-1","username":"alice",...}}`))
//	    c.MustRejectRequest(t, []byte(`{"extra":"field"}`))
//	}
package contracttest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// Contract holds a loaded contract with compiled JSON schemas.
type Contract struct {
	ID               string
	Kind             string
	OwnerCell        string
	ConsistencyLevel string
	Dir              string // absolute path to the contract version directory
	HTTP             *HTTPTransport

	requestSchema  *jsonschema.Schema
	responseSchema *jsonschema.Schema
	payloadSchema  *jsonschema.Schema
	headersSchema  *jsonschema.Schema
}

// HTTPTransport holds optional transport metadata for migrated HTTP contracts.
type HTTPTransport struct {
	Method        string
	Path          string
	SuccessStatus int
	NoContent     bool
	Responses     map[int]HTTPResponseEntry
}

// HTTPResponseEntry describes a declared error response for a specific HTTP status code.
type HTTPResponseEntry struct {
	Description string
	SchemaRef   string
}

// contractYAML is a local struct for parsing contract.yaml without
// importing kernel/metadata (avoids coupling).
type contractYAML struct {
	ID               string         `yaml:"id"`
	Kind             string         `yaml:"kind"`
	OwnerCell        string         `yaml:"ownerCell"`
	ConsistencyLevel string         `yaml:"consistencyLevel"`
	Endpoints        endpointsYAML  `yaml:"endpoints"`
	SchemaRefs       schemaRefsYAML `yaml:"schemaRefs"`
}

type endpointsYAML struct {
	HTTP *httpTransportYAML `yaml:"http,omitempty"`
}

type httpTransportYAML struct {
	Method        string                        `yaml:"method"`
	Path          string                        `yaml:"path"`
	SuccessStatus int                           `yaml:"successStatus"`
	NoContent     bool                          `yaml:"noContent"`
	Responses     map[int]httpResponseEntryYAML `yaml:"responses,omitempty"`
}

type httpResponseEntryYAML struct {
	Description string `yaml:"description"`
	SchemaRef   string `yaml:"schemaRef"`
}

type schemaRefsYAML struct {
	Request  string `yaml:"request,omitempty"`
	Response string `yaml:"response,omitempty"`
	Payload  string `yaml:"payload,omitempty"`
	Headers  string `yaml:"headers,omitempty"`
}

// ContractsRoot returns the absolute path to the contracts/ directory,
// derived from the source location of this package.
func ContractsRoot() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		panic("contracttest: runtime.Caller failed")
	}
	// thisFile = .../pkg/contracttest/contracttest.go
	// walk up to project root, then append contracts/
	srcDir := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	return filepath.Join(srcDir, "contracts")
}

// Load reads contract.yaml from contractDir, compiles all referenced
// JSON Schemas, and returns a Contract ready for validation.
// It calls t.Fatal on any setup error.
func Load(t testing.TB, contractDir string) *Contract {
	t.Helper()

	yamlPath := filepath.Join(contractDir, "contract.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatalf("contracttest.Load: read contract.yaml: %v", err)
	}

	var cy contractYAML
	if err := yaml.Unmarshal(data, &cy); err != nil {
		t.Fatalf("contracttest.Load: parse contract.yaml: %v", err)
	}

	c := &Contract{
		ID:               cy.ID,
		Kind:             cy.Kind,
		OwnerCell:        cy.OwnerCell,
		ConsistencyLevel: cy.ConsistencyLevel,
		Dir:              contractDir,
		HTTP:             newHTTPTransport(cy.Endpoints.HTTP),
	}

	c.requestSchema = compileSchemaFile(t, contractDir, cy.SchemaRefs.Request)
	c.responseSchema = compileSchemaFile(t, contractDir, cy.SchemaRefs.Response)
	c.payloadSchema = compileSchemaFile(t, contractDir, cy.SchemaRefs.Payload)
	c.headersSchema = compileSchemaFile(t, contractDir, cy.SchemaRefs.Headers)

	return c
}

// LoadByID resolves a contract ID to its directory path and loads it.
// The ID "http.auth.user.create.v1" is converted to the path
// contractsRoot/http/auth/user/create/v1/.
func LoadByID(t testing.TB, contractsRoot string, contractID string) *Contract {
	t.Helper()
	segments := strings.Split(contractID, ".")
	contractDir := filepath.Join(append([]string{contractsRoot}, segments...)...)
	return Load(t, contractDir)
}

// ValidateRequest validates jsonData against the request schema.
// No-op if the contract has no request schema.
func (c *Contract) ValidateRequest(t testing.TB, jsonData []byte) {
	t.Helper()
	validateJSON(t, c.requestSchema, jsonData, "request")
}

// ValidateResponse validates jsonData against the response schema.
func (c *Contract) ValidateResponse(t testing.TB, jsonData []byte) {
	t.Helper()
	validateJSON(t, c.responseSchema, jsonData, "response")
}

// ValidatePayload validates jsonData against the payload schema.
func (c *Contract) ValidatePayload(t testing.TB, jsonData []byte) {
	t.Helper()
	validateJSON(t, c.payloadSchema, jsonData, "payload")
}

// ValidateHeaders validates jsonData against the headers schema.
func (c *Contract) ValidateHeaders(t testing.TB, jsonData []byte) {
	t.Helper()
	validateJSON(t, c.headersSchema, jsonData, "headers")
}

// ValidateHTTPResponseRecorder validates an HTTP provider response against the
// migrated transport metadata and, when applicable, the response schema.
func (c *Contract) ValidateHTTPResponseRecorder(t testing.TB, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if c.HTTP == nil {
		t.Errorf("contracttest: contract %q has no endpoints.http metadata", c.ID)
		return
	}
	if recorder == nil {
		t.Errorf("contracttest: ValidateHTTPResponseRecorder: nil recorder")
		return
	}
	if recorder.Code != c.HTTP.SuccessStatus {
		t.Errorf("contracttest: expected HTTP status %d, got %d", c.HTTP.SuccessStatus, recorder.Code)
	}
	body := recorder.Body.Bytes()
	if c.HTTP.NoContent {
		if len(body) != 0 {
			t.Errorf("contracttest: expected empty body for no-content contract %q, got %s", c.ID, string(body))
		}
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		if c.responseSchema != nil {
			t.Errorf("contracttest: expected response body for contract %q", c.ID)
		}
		return
	}
	c.ValidateResponse(t, body)
}

// MustRejectRequest asserts that jsonData is rejected by the request schema.
// This proves the schema is not trivially permissive.
func (c *Contract) MustRejectRequest(t testing.TB, jsonData []byte) {
	t.Helper()
	mustRejectJSON(t, c.requestSchema, jsonData, "request")
}

// MustRejectResponse asserts that jsonData is rejected by the response schema.
func (c *Contract) MustRejectResponse(t testing.TB, jsonData []byte) {
	t.Helper()
	mustRejectJSON(t, c.responseSchema, jsonData, "response")
}

// MustRejectPayload asserts that jsonData is rejected by the payload schema.
func (c *Contract) MustRejectPayload(t testing.TB, jsonData []byte) {
	t.Helper()
	mustRejectJSON(t, c.payloadSchema, jsonData, "payload")
}

// MustRejectHeaders asserts that jsonData is rejected by the headers schema.
func (c *Contract) MustRejectHeaders(t testing.TB, jsonData []byte) {
	t.Helper()
	mustRejectJSON(t, c.headersSchema, jsonData, "headers")
}

// compileSchemaFile reads and compiles a JSON Schema file.
// Returns nil if filename is empty. Calls t.Fatal on errors.
func compileSchemaFile(t testing.TB, dir, filename string) *jsonschema.Schema {
	t.Helper()
	if filename == "" {
		return nil
	}

	fullPath := filepath.Join(dir, filename)
	if !strings.HasPrefix(filepath.Clean(fullPath), filepath.Clean(dir)) {
		t.Fatalf("contracttest: schema path %q escapes contract directory", filename)
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("contracttest: read schema %q: %v", fullPath, err)
	}

	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("contracttest: parse schema JSON %q: %v", fullPath, err)
	}

	compiler := jsonschema.NewCompiler()
	url := "file:///" + filepath.Base(filename)
	if err := compiler.AddResource(url, doc); err != nil {
		t.Fatalf("contracttest: add schema resource %q: %v", fullPath, err)
	}

	schema, err := compiler.Compile(url)
	if err != nil {
		t.Fatalf("contracttest: compile schema %q: %v", fullPath, err)
	}

	return schema
}

// validateJSON validates data against a compiled schema.
// No-op if schema is nil.
func validateJSON(t testing.TB, schema *jsonschema.Schema, data []byte, label string) {
	t.Helper()
	if schema == nil {
		return
	}

	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Errorf("contracttest: %s: invalid JSON: %v", label, err)
		return
	}

	if err := schema.Validate(v); err != nil {
		t.Errorf("contracttest: %s schema validation failed:\n%s", label, formatValidationError(err))
	}
}

// mustRejectJSON asserts that data is NOT valid against the schema.
func mustRejectJSON(t testing.TB, schema *jsonschema.Schema, data []byte, label string) {
	t.Helper()
	if schema == nil {
		t.Errorf("contracttest: MustReject%s: no %s schema loaded", label, label)
		return
	}

	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		// Data is not even valid JSON — that counts as "rejected".
		return
	}

	if err := schema.Validate(v); err == nil {
		t.Errorf("contracttest: MustReject%s: expected schema to reject data, but it passed: %s", label, string(data))
	}
}

// formatValidationError formats a jsonschema validation error into a readable string.
func formatValidationError(err error) string {
	if ve, ok := err.(*jsonschema.ValidationError); ok {
		return formatValidationErrorDetail(ve, "")
	}
	return err.Error()
}

func formatValidationErrorDetail(ve *jsonschema.ValidationError, indent string) string {
	var sb strings.Builder
	loc := strings.Join(ve.InstanceLocation, "/")
	if loc == "" {
		loc = "(root)"
	}
	msg := fmt.Sprintf("at %s: %v", loc, ve.ErrorKind)
	sb.WriteString(indent + msg + "\n")
	for _, cause := range ve.Causes {
		sb.WriteString(formatValidationErrorDetail(cause, indent+"  "))
	}
	return sb.String()
}

func newHTTPTransport(meta *httpTransportYAML) *HTTPTransport {
	if meta == nil {
		return nil
	}
	t := &HTTPTransport{
		Method:        meta.Method,
		Path:          meta.Path,
		SuccessStatus: meta.SuccessStatus,
		NoContent:     meta.NoContent,
	}
	if len(meta.Responses) > 0 {
		t.Responses = make(map[int]HTTPResponseEntry, len(meta.Responses))
		for status, entry := range meta.Responses {
			t.Responses[status] = HTTPResponseEntry(entry)
		}
	}
	return t
}

// ValidateErrorResponse validates body against the JSON Schema declared for
// the given HTTP status code in the contract's endpoints.http.responses map.
// It calls t.Errorf if:
//   - the contract has no endpoints.http metadata
//   - no response entry is declared for status
//   - body fails schema validation
func (c *Contract) ValidateErrorResponse(t testing.TB, status int, body []byte) {
	t.Helper()
	if c.HTTP == nil {
		t.Errorf("contracttest: contract %q has no endpoints.http metadata", c.ID)
		return
	}
	entry, ok := c.HTTP.Responses[status]
	if !ok {
		t.Errorf("contracttest: no response declared for status %d in contract %q", status, c.ID)
		return
	}
	if entry.SchemaRef == "" {
		t.Errorf("contracttest: response entry for status %d in contract %q has empty schemaRef", status, c.ID)
		return
	}
	schema := compileSchemaFileAbsolute(t, c.Dir, entry.SchemaRef)
	validateJSON(t, schema, body, fmt.Sprintf("error response %d", status))
}

// compileSchemaFileAbsolute reads and compiles a JSON Schema relative to dir,
// allowing traversal outside dir (needed for shared schemas via relative paths
// like "../../../../shared/errors/...").
func compileSchemaFileAbsolute(t testing.TB, dir, filename string) *jsonschema.Schema {
	t.Helper()
	fullPath := filepath.Join(dir, filename)

	data, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("contracttest: read schema %q: %v", fullPath, err)
	}

	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("contracttest: parse schema JSON %q: %v", fullPath, err)
	}

	compiler := jsonschema.NewCompiler()
	url := "file:///" + filepath.Base(filename)
	if err := compiler.AddResource(url, doc); err != nil {
		t.Fatalf("contracttest: add schema resource %q: %v", fullPath, err)
	}

	schema, err := compiler.Compile(url)
	if err != nil {
		t.Fatalf("contracttest: compile schema %q: %v", fullPath, err)
	}

	return schema
}
