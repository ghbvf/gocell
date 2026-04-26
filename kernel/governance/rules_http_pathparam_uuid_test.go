package governance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeUUIDHandlerFile writes a minimal Go source file (valid package + imports)
// to dir/handler.go and returns the full path. The source must define a
// function named "h" as the handler. An auth.Mount call is added to correlate
// contractID → h, satisfying fail-closed CH-05.
func writeUUIDHandlerFile(t *testing.T, dir, contractID, src string) string {
	t.Helper()
	path := filepath.Join(dir, "handler.go")
	content := `package x

import (
	"net/http"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/runtime/auth"
)

var spec = wrapper.ContractSpec{ID: "` + contractID + `"}

func setup(mux http.Handler) {
	auth.Mount(mux, auth.Route{Contract: spec, Handler: http.HandlerFunc(h)})
}

` + src
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// makeUUIDContract builds a ContractMeta with specified UUID path params.
func makeUUIDContract(id, contractFile string, uuidParams []string) *metadata.ContractMeta {
	pp := make(map[string]contracts.ParamSchema, len(uuidParams))
	for _, name := range uuidParams {
		pp[name] = contracts.ParamSchema{Type: "string", Format: "uuid"}
	}
	return &metadata.ContractMeta{
		ID:        id,
		Kind:      "http",
		OwnerCell: "testcell",
		Lifecycle: "active",
		File:      contractFile,
		Endpoints: metadata.EndpointsMeta{
			HTTP: &metadata.HTTPTransportMeta{
				Method:        "GET",
				Path:          "/api/v1/test/{id}",
				SuccessStatus: 200,
				PathParams:    pp,
			},
		},
	}
}

func TestCheckHTTPPathParamUUID(t *testing.T) {
	tests := []struct {
		name        string
		handlerSrc  string
		uuidParams  []string // path param names with format:uuid in contract
		wantErrors  []string // expected CH-05 error message substrings
		noHandler   bool     // suppress handler file creation
		noAuthMount bool     // write handler src without auth.Mount boilerplate
	}{
		{
			name: "happy_path: uuid param declared and ParseUUIDPathParam called",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.ParseUUIDPathParam(w, r, "id")
	_ = id; _ = ok
}
`,
			uuidParams: []string{"id"},
		},
		{
			name: "missing: handler uses r.PathValue directly instead of ParseUUIDPathParam",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_ = id
}
`,
			uuidParams: []string{"id"},
			wantErrors: []string{`pathParam "id" has format:uuid but handler does not call httputil.ParseUUIDPathParam`},
		},
		{
			name: "param-name mismatch: contract has userID but handler parses id",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.ParseUUIDPathParam(w, r, "id")
	_ = id; _ = ok
}
`,
			uuidParams: []string{"userID"},
			wantErrors: []string{`pathParam "userID" has format:uuid but handler does not call httputil.ParseUUIDPathParam`},
		},
		{
			name: "multiple uuid params: both must be parsed",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.ParseUUIDPathParam(w, r, "id")
	_ = id; _ = ok
	cmdId, ok2 := httputil.ParseUUIDPathParam(w, r, "cmdId")
	_ = cmdId; _ = ok2
}
`,
			uuidParams: []string{"id", "cmdId"},
		},
		{
			name: "multiple uuid params: one missing",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.ParseUUIDPathParam(w, r, "id")
	_ = id; _ = ok
}
`,
			uuidParams: []string{"id", "cmdId"},
			wantErrors: []string{`pathParam "cmdId" has format:uuid but handler does not call httputil.ParseUUIDPathParam`},
		},
		{
			name: "no uuid params: contract has non-uuid path params only",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	_ = key
}
`,
			uuidParams: []string{}, // no uuid format params
		},
		{
			name:       "skip: no handler file found",
			handlerSrc: "",
			uuidParams: []string{"id"},
			noHandler:  true,
		},
		{
			name: "fail-closed: contract with format=uuid but no auth.Mount in handler file",
			handlerSrc: `
func handleSomething(w http.ResponseWriter, r *http.Request) {
	id, _ := httputil.ParseUUIDPathParam(w, r, "id")
	_ = id
}
`,
			uuidParams: []string{"id"},
			// File-wide scan would have passed; function-level cannot resolve → fail-closed.
			wantErrors:  []string{"auth.Mount correlation failed"},
			noAuthMount: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			const contractID = "http.test.uuid.v1"
			sliceRelDir := "cells/testcell/slices/testslice"

			sliceAbsDir := filepath.Join(root, sliceRelDir)
			require.NoError(t, os.MkdirAll(sliceAbsDir, 0o755))
			if !tc.noHandler && tc.handlerSrc != "" {
				if tc.noAuthMount {
					// Write handler src without auth.Mount boilerplate to test fail-closed.
					path := filepath.Join(sliceAbsDir, "handler.go")
					content := "package x\n\nimport \"net/http\"\nimport \"github.com/ghbvf/gocell/pkg/httputil\"\n\n" + tc.handlerSrc
					require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
				} else {
					writeUUIDHandlerFile(t, sliceAbsDir, contractID, tc.handlerSrc)
				}
			}

			project := makeProject(contractID, sliceRelDir)
			if tc.noHandler {
				project = &metadata.ProjectMeta{
					Cells:      map[string]*metadata.CellMeta{},
					Slices:     map[string]*metadata.SliceMeta{},
					Contracts:  map[string]*metadata.ContractMeta{},
					Journeys:   map[string]*metadata.JourneyMeta{},
					Assemblies: map[string]*metadata.AssemblyMeta{},
				}
			}

			// Build a contract with non-uuid params for the "no uuid params" case.
			var c *metadata.ContractMeta
			if len(tc.uuidParams) == 0 && !tc.noHandler {
				c = &metadata.ContractMeta{
					ID:        contractID,
					Kind:      "http",
					OwnerCell: "testcell",
					Lifecycle: "active",
					File:      "contracts/http/test/uuid/v1/contract.yaml",
					Endpoints: metadata.EndpointsMeta{
						HTTP: &metadata.HTTPTransportMeta{
							Method:        "GET",
							Path:          "/api/v1/config/{key}",
							SuccessStatus: 200,
							PathParams: map[string]contracts.ParamSchema{
								"key": {Type: "string"}, // no format:uuid
							},
						},
					},
				}
			} else {
				c = makeUUIDContract(contractID, "contracts/http/test/uuid/v1/contract.yaml", tc.uuidParams)
			}

			validator := NewValidator(project, root)
			results := validator.CheckHTTPPathParamUUID([]*metadata.ContractMeta{c}, root)

			var errs []ValidationResult
			for _, r := range results {
				if r.Severity == SeverityError {
					errs = append(errs, r)
				}
			}

			require.Len(t, errs, len(tc.wantErrors), "error count mismatch")
			for i, want := range tc.wantErrors {
				assert.Contains(t, errs[i].Message, want)
			}

			if tc.noHandler {
				assert.Empty(t, results, "expected no findings when no handler exists")
			}
		})
	}
}

// TestCheckHTTPPathParamUUID_FunctionNarrowing verifies CH-05 Finding 7:
// when two contracts share a handler file and auth.Mount correlates each
// contract to a specific function, CH-05 must walk only that function's body.
// If ParseUUIDPathParam is called in the wrong function (for the wrong
// contract), the check must still report missing.
func TestCheckHTTPPathParamUUID_FunctionNarrowing(t *testing.T) {
	root := t.TempDir()
	sliceRelDir := "cells/testcell/slices/testslice"
	sliceAbsDir := filepath.Join(root, sliceRelDir)
	require.NoError(t, os.MkdirAll(sliceAbsDir, 0o755))

	const contractA = "http.test.uuid.a.v1"
	const contractB = "http.test.uuid.b.v1"

	// The handler file has TWO auth.Mount calls:
	//   contractA → handleA (contains both ParseUUIDPathParam calls)
	//   contractB → handleB (contains NO ParseUUIDPathParam calls)
	// CH-05 must detect that contractB's required "userID" param is not
	// parsed in handleB, even though it IS parsed in handleA.
	src := `
var specA = wrapper.ContractSpec{ID: "` + contractA + `"}
var specB = wrapper.ContractSpec{ID: "` + contractB + `"}

func setup(mux http.Handler) {
	auth.Mount(mux, auth.Route{Contract: specA, Handler: http.HandlerFunc(handleA)})
	auth.Mount(mux, auth.Route{Contract: specB, Handler: http.HandlerFunc(handleB)})
}

func handleA(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.ParseUUIDPathParam(w, r, "id")
	_ = id; _ = ok
	userID, ok2 := httputil.ParseUUIDPathParam(w, r, "userID")
	_ = userID; _ = ok2
}

func handleB(w http.ResponseWriter, r *http.Request) {
	// intentionally missing: httputil.ParseUUIDPathParam(w, r, "userID")
}
`
	// Write handler file with all necessary imports.
	handlerPath := filepath.Join(sliceAbsDir, "handler.go")
	content := `package x

import (
	"net/http"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/runtime/auth"
)
` + src
	require.NoError(t, os.WriteFile(handlerPath, []byte(content), 0o644))

	// Two slices serving two contracts in the same handler.go.
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"testcell": {ID: "testcell", File: "cells/testcell/cell.yaml"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"testcell/testslice": {
				ID:            "testslice",
				BelongsToCell: "testcell",
				File:          sliceRelDir + "/slice.yaml",
				ContractUsages: []metadata.ContractUsage{
					{Contract: contractA, Role: "serve"},
					{Contract: contractB, Role: "serve"},
				},
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	cA := makeUUIDContract(contractA, "contracts/http/test/uuid/a/v1/contract.yaml", []string{"id", "userID"})
	cB := makeUUIDContract(contractB, "contracts/http/test/uuid/b/v1/contract.yaml", []string{"userID"})

	validator := NewValidator(project, root)
	results := validator.CheckHTTPPathParamUUID([]*metadata.ContractMeta{cA, cB}, root)

	var errMsgs []string
	for _, r := range results {
		if r.Severity == SeverityError {
			errMsgs = append(errMsgs, r.Message)
		}
	}

	// contractA's handleA has both "id" and "userID" — no finding.
	for _, msg := range errMsgs {
		assert.NotContains(t, msg, contractA,
			"contractA's handleA parses both params: no finding expected")
	}

	// contractB's handleB is missing ParseUUIDPathParam for "userID".
	found := false
	for _, msg := range errMsgs {
		if assert.Contains(t, msg, contractB) && assert.Contains(t, msg, `"userID"`) {
			found = true
			break
		}
	}
	_ = found
	require.NotEmpty(t, errMsgs, "contractB must produce a CH-05 finding for missing userID")
}
