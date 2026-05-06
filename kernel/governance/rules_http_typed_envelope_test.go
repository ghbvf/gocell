package governance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// writeTypesGen creates a synthetic generated/contracts/<segments>/types_gen.go
// under root with the given source as the body. Mirrors the layout that
// CheckHTTPTypedResponseEnvelope expects to see on disk for the contract id.
func writeTypesGen(t *testing.T, root, contractID, body string) {
	t.Helper()
	path := typedEnvelopeTypesGenPath(root, contractID)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

func TestCheckHTTPTypedResponseEnvelope_DeclaredAndImplementedMatch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// Synthetic types_gen.go: success 200 + 401/404 errors.
	writeTypesGen(t, root, "http.test.get.v1", `package get

type Get200JSONResponse struct{}
type Get401ErrorResponse struct{}
type Get404ErrorResponse struct{}
`)

	contract := &metadata.ContractMeta{
		ID:        "http.test.get.v1",
		Kind:      "http",
		OwnerCell: "test-cell",
		Lifecycle: "active",
		Codegen:   true,
		Endpoints: metadata.EndpointsMeta{
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "GET",
				Path:          "/api/v1/test",
				SuccessStatus: 200,
				Responses: map[int]metadata.HTTPResponseMeta{
					401: {SchemaRef: "../shared.json"},
					404: {SchemaRef: "../shared.json"},
				},
			},
		},
		File: "contracts/http/test/get/v1/contract.yaml",
	}

	v := NewValidator(&metadata.ProjectMeta{}, "", clock.Real())
	results := v.CheckHTTPTypedResponseEnvelope([]*metadata.ContractMeta{contract}, root)

	assert.Empty(t, results, "fully aligned contract must produce no findings")
}

func TestCheckHTTPTypedResponseEnvelope_DeclaredButNotImplemented(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// Generated types_gen.go missing the 503 typed struct.
	writeTypesGen(t, root, "http.test.list.v1", `package list

type List200JSONResponse struct{}
type List401ErrorResponse struct{}
`)

	contract := &metadata.ContractMeta{
		ID:        "http.test.list.v1",
		Kind:      "http",
		OwnerCell: "test-cell",
		Lifecycle: "active",
		Codegen:   true,
		Endpoints: metadata.EndpointsMeta{
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "GET",
				Path:          "/api/v1/items",
				SuccessStatus: 200,
				Responses: map[int]metadata.HTTPResponseMeta{
					401: {SchemaRef: "../shared.json"},
					503: {SchemaRef: "../shared.json"}, // declared but no struct
				},
			},
		},
		File: "contracts/http/test/list/v1/contract.yaml",
	}

	v := NewValidator(&metadata.ProjectMeta{}, "", clock.Real())
	results := v.CheckHTTPTypedResponseEnvelope([]*metadata.ContractMeta{contract}, root)

	require.Len(t, results, 1, "exactly one finding for missing 503 typed struct")
	assert.Equal(t, CodeContractHealthTypedEnvelope, results[0].Code)
	assert.Equal(t, SeverityError, results[0].Severity)
	assert.Contains(t, results[0].Message, "503")
	assert.Contains(t, results[0].Message, "no matching typed response struct")
	assert.Equal(t, "endpoints.http.responses[503]", results[0].Field)
}

func TestCheckHTTPTypedResponseEnvelope_OrphanStruct(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// Generated types_gen.go has a 403 struct that contract.yaml does not declare.
	writeTypesGen(t, root, "http.test.delete.v1", `package del

type Delete204NoContentResponse struct{}
type Delete401ErrorResponse struct{}
type Delete403ErrorResponse struct{} // orphan — contract.yaml has no 403
`)

	contract := &metadata.ContractMeta{
		ID:        "http.test.delete.v1",
		Kind:      "http",
		OwnerCell: "test-cell",
		Lifecycle: "active",
		Codegen:   true,
		Endpoints: metadata.EndpointsMeta{
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "DELETE",
				Path:          "/api/v1/items/{id}",
				SuccessStatus: 204,
				NoContent:     true,
				Responses: map[int]metadata.HTTPResponseMeta{
					401: {SchemaRef: "../shared.json"},
				},
			},
		},
		File: "contracts/http/test/delete/v1/contract.yaml",
	}

	v := NewValidator(&metadata.ProjectMeta{}, "", clock.Real())
	results := v.CheckHTTPTypedResponseEnvelope([]*metadata.ContractMeta{contract}, root)

	require.Len(t, results, 1, "exactly one finding for orphan 403 struct")
	assert.Equal(t, CodeContractHealthTypedEnvelope, results[0].Code)
	assert.Equal(t, SeverityError, results[0].Severity)
	assert.Contains(t, results[0].Message, "403")
	assert.Contains(t, results[0].Message, "orphan struct")
}

func TestCheckHTTPTypedResponseEnvelope_NonHTTPSkipped(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	contract := &metadata.ContractMeta{
		ID:      "event.test.created.v1",
		Kind:    "event",
		Codegen: true,
		File:    "contracts/event/test/created/v1/contract.yaml",
	}

	v := NewValidator(&metadata.ProjectMeta{}, "", clock.Real())
	results := v.CheckHTTPTypedResponseEnvelope([]*metadata.ContractMeta{contract}, root)
	assert.Empty(t, results, "event contracts have no typed response envelope")
}

func TestCheckHTTPTypedResponseEnvelope_NonCodegenSkipped(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	contract := &metadata.ContractMeta{
		ID:      "http.legacy.foo.v1",
		Kind:    "http",
		Codegen: false, // legacy hand-written handler — no typed envelope expected
		Endpoints: metadata.EndpointsMeta{
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "GET",
				Path:          "/legacy",
				SuccessStatus: 200,
				Responses: map[int]metadata.HTTPResponseMeta{
					404: {SchemaRef: "../shared.json"},
				},
			},
		},
		File: "contracts/http/legacy/foo/v1/contract.yaml",
	}

	v := NewValidator(&metadata.ProjectMeta{}, "", clock.Real())
	results := v.CheckHTTPTypedResponseEnvelope([]*metadata.ContractMeta{contract}, root)
	assert.Empty(t, results, "non-codegen contracts skip CH-06")
}

func TestCheckHTTPTypedResponseEnvelope_MissingTypesGenFileSkipped(t *testing.T) {
	t.Parallel()
	root := t.TempDir() // empty — no types_gen.go anywhere

	contract := &metadata.ContractMeta{
		ID:      "http.absent.foo.v1",
		Kind:    "http",
		Codegen: true,
		Endpoints: metadata.EndpointsMeta{
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "GET",
				Path:          "/absent",
				SuccessStatus: 200,
				Responses: map[int]metadata.HTTPResponseMeta{
					404: {SchemaRef: "../shared.json"},
				},
			},
		},
		File: "contracts/http/absent/foo/v1/contract.yaml",
	}

	v := NewValidator(&metadata.ProjectMeta{}, "", clock.Real())
	results := v.CheckHTTPTypedResponseEnvelope([]*metadata.ContractMeta{contract}, root)

	assert.Empty(t, results, "missing types_gen.go is owned by `gocell generate --verify`, not CH-06")
}

func TestCheckHTTPTypedResponseEnvelope_InternalSegmentRewritten(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// Contract id has "internal" segment — generated path must use "internalapi"
	// per ContractIDToPackagePath convention to bypass Go's internal package rule.
	writeTypesGen(t, root, "http.internal.foo.v1", `package foo

type Foo200JSONResponse struct{}
type Foo403ErrorResponse struct{}
`)

	contract := &metadata.ContractMeta{
		ID:      "http.internal.foo.v1",
		Kind:    "http",
		Codegen: true,
		Endpoints: metadata.EndpointsMeta{
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "GET",
				Path:          "/internal/v1/foo",
				SuccessStatus: 200,
				Responses: map[int]metadata.HTTPResponseMeta{
					403: {SchemaRef: "../shared.json"},
				},
			},
		},
		File: "examples/iotdevice/contracts/http/internal/foo/v1/contract.yaml",
	}

	v := NewValidator(&metadata.ProjectMeta{}, "", clock.Real())
	results := v.CheckHTTPTypedResponseEnvelope([]*metadata.ContractMeta{contract}, root)

	assert.Empty(t, results, "internal→internalapi path mapping must resolve to the typed-envelope file")
}

func TestTypedResponseStructPattern(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		wantStatus int
		wantMatch  bool
	}{
		{"Get200JSONResponse", 200, true},
		{"Get201JSONResponse", 201, true},
		{"Delete204NoContentResponse", 204, true},
		{"Get404ErrorResponse", 404, true},
		{"Get503ErrorResponse", 503, true},
		{"HandleEnqueue201JSONResponse", 201, true},
		{"Response", 0, false},        // base DTO, not typed envelope
		{"Request", 0, false},         // base DTO
		{"Get200", 0, false},          // missing suffix
		{"GetSomeResponse", 0, false}, // no status digits
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := typedResponseStructPattern.FindStringSubmatch(tc.name)
			if !tc.wantMatch {
				assert.Nil(t, m, "expected no match for %q", tc.name)
				return
			}
			require.NotNil(t, m, "expected match for %q", tc.name)
			assert.Equal(t, intToStr(tc.wantStatus), m[1])
		})
	}
}

func intToStr(i int) string { // local helper avoids strconv import noise.
	if i < 100 || i > 999 {
		return ""
	}
	return string(rune('0'+i/100)) + string(rune('0'+(i/10)%10)) + string(rune('0'+i%10))
}
