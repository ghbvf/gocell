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
//	    c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), "http.auth.user.create.v1")
//	    c.ValidateRequest(t, []byte(`{"username":"alice","email":"a@b.com","password":"s"}`))
//	    c.ValidateResponse(t, []byte(`{"data":{"id":"u-1","username":"alice",...}}`))
//	    c.MustRejectRequest(t, []byte(`{"extra":"field"}`))
//	}
package contracttest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tests/contracttest/internal/fixtureload"
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
	extraSchemas   map[string]*jsonschema.Schema // keyed by extra ref name
}

// HTTPTransport holds optional transport metadata for migrated HTTP contracts.
// It is an alias of metadata.HTTPTransportMeta to preserve the public API.
type HTTPTransport = metadata.HTTPTransportMeta

// HTTPResponseEntry describes a declared error response for a specific HTTP status code.
// It is an alias of metadata.HTTPResponseMeta to preserve the public API.
type HTTPResponseEntry = metadata.HTTPResponseMeta

// contractYAML is a local struct for parsing contract.yaml without depending
// on the full metadata.ProjectMeta parser.
type contractYAML struct {
	ID               string                  `yaml:"id"`
	Kind             string                  `yaml:"kind"`
	OwnerCell        string                  `yaml:"ownerCell"`
	ConsistencyLevel string                  `yaml:"consistencyLevel"`
	Endpoints        endpointsYAML           `yaml:"endpoints"`
	SchemaRefs       metadata.SchemaRefsMeta `yaml:"schemaRefs"`
}

type endpointsYAML struct {
	HTTP *metadata.HTTPTransportMeta `yaml:"http,omitempty"`
}

// ContractsRoot returns the absolute path to the contracts/ directory,
// derived from the source location of this package.
func ContractsRoot(t testing.TB) string {
	t.Helper()
	return filepath.Join(projectRoot(t), "contracts")
}

// ExampleContractsRoot returns the absolute path to an example's contracts/
// directory. The example name is the directory under examples/.
func ExampleContractsRoot(t testing.TB, example string) string {
	t.Helper()
	return filepath.Join(projectRoot(t), "examples", example, "contracts")
}

func projectRoot(t testing.TB) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("contracttest: runtime.Caller failed")
	}
	// thisFile = .../tests/contracttest/contracttest.go
	return filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
}

// Load reads contract.yaml from contractDir, compiles all referenced
// JSON Schemas, and returns a Contract ready for validation.
// It calls t.Fatal on any setup error.
func Load(t testing.TB, contractDir string) *Contract {
	t.Helper()

	yamlPath := filepath.Join(contractDir, "contract.yaml")
	data, err := fixtureload.LoadFixture(yamlPath)
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

	c.extraSchemas = make(map[string]*jsonschema.Schema)
	for key, filename := range cy.SchemaRefs.Extra {
		c.extraSchemas[key] = compileSchemaFile(t, contractDir, filename)
	}

	return c
}

// LoadFromString builds a Contract from inline JSON schema strings, bypassing
// the fixture filesystem. Useful for unit tests that want to assert schema
// behavior without creating testdata files. Empty schema strings produce a
// nil schema (the corresponding Validate* call becomes a no-op).
func LoadFromString(t testing.TB, contractID, requestSchema, responseSchema string) *Contract {
	t.Helper()
	c := &Contract{
		ID:   contractID,
		Kind: "http",
	}
	if requestSchema != "" {
		c.requestSchema = compileSchemaFromString(t, contractID+"/request", requestSchema)
	}
	if responseSchema != "" {
		c.responseSchema = compileSchemaFromString(t, contractID+"/response", responseSchema)
	}
	c.extraSchemas = make(map[string]*jsonschema.Schema)
	return c
}

func compileSchemaFromString(t testing.TB, refName, schemaJSON string) *jsonschema.Schema {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(schemaJSON), &doc); err != nil {
		t.Fatalf("contracttest: parse inline schema %q: %v", refName, err)
	}
	compiler := jsonschema.NewCompiler()
	url := "mem:///" + refName
	if err := compiler.AddResource(url, doc); err != nil {
		t.Fatalf("contracttest: add inline schema %q: %v", refName, err)
	}
	schema, err := compiler.Compile(url)
	if err != nil {
		t.Fatalf("contracttest: compile inline schema %q: %v", refName, err)
	}
	return schema
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

// ValidateSchemaRef validates jsonData against the schema referenced by the
// given key name. This covers both well-known refs (request, response, payload,
// headers) and extra refs declared in schemaRefs. Unknown keys fail the test so
// schemaRef typos cannot pass silently.
func (c *Contract) ValidateSchemaRef(t testing.TB, key string, jsonData []byte) {
	t.Helper()
	switch key {
	case "request":
		c.ValidateRequest(t, jsonData)
		return
	case "response":
		c.ValidateResponse(t, jsonData)
		return
	case "payload":
		c.ValidatePayload(t, jsonData)
		return
	case "headers":
		c.ValidateHeaders(t, jsonData)
		return
	}
	schema, ok := c.extraSchemas[key]
	if !ok {
		t.Errorf("contracttest: schemaRef key %q is not declared in contract %q", key, c.ID)
		return
	}
	validateJSON(t, schema, jsonData, key)
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
//
// Rejection sources: missing `required` fields, type/pattern/enum mismatch, or
// extra fields under `additionalProperties: false` / `unevaluatedProperties:
// false`. Request schemas are strict (FMT-20), so extra fields are rejected.
func (c *Contract) MustRejectRequest(t testing.TB, jsonData []byte) {
	t.Helper()
	mustRejectJSON(t, c.requestSchema, jsonData, "request")
}

// MustRejectResponse asserts that jsonData is rejected by the response schema.
//
// Note: per ADR-202605031600, response schemas are lenient by default — extra
// fields are accepted unless the schema explicitly declares
// `additionalProperties: false` or `unevaluatedProperties: false`. To prove
// extra-field rejection, the schema must declare a closed-shape constraint
// (typical for error envelopes and metadata-only payloads). For lenient
// response schemas, MustRejectResponse only catches missing required fields,
// type/pattern/enum mismatch, and similar non-additive violations.
func (c *Contract) MustRejectResponse(t testing.TB, jsonData []byte) {
	t.Helper()
	mustRejectJSON(t, c.responseSchema, jsonData, "response")
}

// MustRejectPayload asserts that jsonData is rejected by the payload schema.
//
// Note: per ADR-202605031600, event payload schemas are lenient by default;
// metadata-only payloads add `unevaluatedProperties: false` to forbid extra
// fields. See MustRejectResponse for the full lenient-schema caveat.
func (c *Contract) MustRejectPayload(t testing.TB, jsonData []byte) {
	t.Helper()
	mustRejectJSON(t, c.payloadSchema, jsonData, "payload")
}

// MustRejectHeaders asserts that jsonData is rejected by the headers schema.
//
// Note: per ADR-202605031600, event headers schemas are lenient by default.
// See MustRejectResponse for the full lenient-schema caveat.
func (c *Contract) MustRejectHeaders(t testing.TB, jsonData []byte) {
	t.Helper()
	mustRejectJSON(t, c.headersSchema, jsonData, "headers")
}

// compileSchemaFile reads and compiles a JSON Schema file referenced relative
// to dir. Returns nil if filename is empty. Calls t.Fatal on errors.
//
// Path traversal allow-list (security boundary — schemaRef values come from
// contract.yaml, which is externally editable):
//   - filename must be a relative path (absolute paths rejected)
//   - resolved fullPath must either:
//     (a) stay within dir (typical: same-directory schema files), OR
//     (b) resolve under <contractsRoot>/shared/... where contractsRoot is the
//     nearest ancestor of dir whose basename is "contracts" (canonical
//     pattern: /<X>/contracts/<kind>/<domain>/<v>/ refs ../shared/ for
//     cross-contract shared schemas like the error response shape)
//
// All other escapes (../../../../etc, /etc/passwd, paths outside any
// contracts/ tree, etc.) fail closed at t.Fatal.
func compileSchemaFile(t testing.TB, dir, filename string) *jsonschema.Schema {
	t.Helper()
	if filename == "" {
		return nil
	}
	if filepath.IsAbs(filename) {
		t.Fatalf("contracttest: schema path %q must be relative; absolute paths rejected", filename)
	}

	cleanDir := filepath.Clean(dir)
	fullPath := filepath.Clean(filepath.Join(cleanDir, filename))

	if !pathWithinAllowList(t, cleanDir, fullPath) {
		t.Fatalf("contracttest: schema path %q escapes allow-list (must stay in dir or under contracts/shared/)", filename)
	}

	data, err := fixtureload.LoadFixture(fullPath)
	if err != nil {
		t.Fatalf("contracttest: read schema %q: %v", fullPath, err)
	}

	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("contracttest: parse schema JSON %q: %v", fullPath, err)
	}

	compiler := jsonschema.NewCompiler()
	url := "file:///" + filepath.Clean(fullPath)
	if err := compiler.AddResource(url, doc); err != nil {
		t.Fatalf("contracttest: add schema resource %q: %v", fullPath, err)
	}

	schema, err := compiler.Compile(url)
	if err != nil {
		t.Fatalf("contracttest: compile schema %q: %v", fullPath, err)
	}

	return schema
}

// pathWithinAllowList reports whether fullPath either stays inside cleanDir
// or resolves under <contractsRoot>/shared/ where contractsRoot is the
// nearest ancestor of cleanDir whose basename is "contracts". Symlinks in
// fullPath are resolved via filepath.EvalSymlinks before the prefix check
// so a symlink under contracts/shared/ pointing outside the allow-list
// fails closed (purely lexical HasPrefix would accept the symlink itself).
func pathWithinAllowList(_ testing.TB, cleanDir, fullPath string) bool {
	resolved, err := evalSymlinkOrSelf(fullPath)
	if err != nil {
		return false
	}
	if resolved == cleanDir || strings.HasPrefix(resolved, cleanDir+string(filepath.Separator)) {
		return true
	}
	contractsRoot, ok := findContractsRoot(cleanDir)
	if !ok {
		return false
	}
	sharedRoot, err := evalSymlinkOrSelf(filepath.Join(contractsRoot, "shared"))
	if err != nil {
		return false
	}
	if resolved == sharedRoot {
		return true
	}
	return strings.HasPrefix(resolved, sharedRoot+string(filepath.Separator))
}

// evalSymlinkOrSelf returns filepath.EvalSymlinks(p) when p exists, or
// resolves the deepest existing ancestor and re-attaches the missing
// trailing segments when p does not (a non-existent leaf must not be a
// symlink-bypass loophole — an attacker can plant a symlink at any
// existing parent dir even if the target file hasn't been created yet).
// Returns error for a broken symlink chain on an existing path; we treat
// that as fail-closed.
func evalSymlinkOrSelf(p string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	// Path doesn't exist; walk up to deepest existing ancestor and
	// EvalSymlinks that, then append the unresolved tail.
	ancestor := p
	var tail []string
	for {
		parent, leaf := filepath.Split(strings.TrimSuffix(ancestor, string(filepath.Separator)))
		if parent == "" || parent == ancestor {
			return p, nil // walked to root without finding existing ancestor
		}
		ancestor = strings.TrimSuffix(parent, string(filepath.Separator))
		if ancestor == "" {
			ancestor = string(filepath.Separator)
		}
		tail = append([]string{leaf}, tail...)
		resolvedAncestor, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			return filepath.Join(append([]string{resolvedAncestor}, tail...)...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
}

// findContractsRoot walks up from cleanDir until a directory whose basename
// is "contracts" is found. Returns its path and true on success.
func findContractsRoot(cleanDir string) (string, bool) {
	cur := cleanDir
	for {
		if filepath.Base(cur) == "contracts" {
			return cur, true
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		cur = parent
	}
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

func newHTTPTransport(meta *metadata.HTTPTransportMeta) *HTTPTransport {
	return meta
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
	schema := compileSchemaFile(t, c.Dir, entry.SchemaRef)
	validateJSON(t, schema, body, fmt.Sprintf("error response %d", status))
}
