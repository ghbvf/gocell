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

// writeUUIDHandlerFile writes a minimal Go source file (valid package + import)
// to dir/handler.go and returns the full path.
func writeUUIDHandlerFile(t *testing.T, dir, src string) string {
	t.Helper()
	path := filepath.Join(dir, "handler.go")
	content := "package x\n\nimport \"net/http\"\nimport \"github.com/ghbvf/gocell/pkg/httputil\"\n\n" + src
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
		name       string
		handlerSrc string
		uuidParams []string // path param names with format:uuid in contract
		wantErrors []string // expected CH-05 error message substrings
		noHandler  bool     // suppress handler file creation
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
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			const contractID = "http.test.uuid.v1"
			sliceRelDir := "cells/testcell/slices/testslice"

			sliceAbsDir := filepath.Join(root, sliceRelDir)
			require.NoError(t, os.MkdirAll(sliceAbsDir, 0o755))
			if !tc.noHandler && tc.handlerSrc != "" {
				writeUUIDHandlerFile(t, sliceAbsDir, tc.handlerSrc)
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
