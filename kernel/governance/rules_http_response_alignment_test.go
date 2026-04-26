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

// writeHandlerFile writes Go source as handler.go inside dir and returns the
// full path. The source must define a function named "h" as the handler. The
// file is wrapped with a minimal valid package declaration that includes an
// auth.Mount call correlating contractID → h, satisfying fail-closed CH-04/05.
func writeHandlerFile(t *testing.T, dir, contractID, src string) string {
	t.Helper()
	path := filepath.Join(dir, "handler.go")
	content := `package x

import (
	"net/http"
	"github.com/ghbvf/gocell/pkg/errcode"
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

// writeHandlerFileWithHTTPUtil writes Go source as handler.go with httputil imported.
// The source must define a function named "h" as the handler.
func writeHandlerFileWithHTTPUtil(t *testing.T, dir, contractID, src string) string {
	t.Helper()
	path := filepath.Join(dir, "handler.go")
	content := `package x

import (
	"net/http"
	"github.com/ghbvf/gocell/pkg/errcode"
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

// makeContract builds a minimal ContractMeta for CH-04 tests. contractFile is
// relative to projectRoot (e.g. "contracts/http/foo/v1/contract.yaml").
func makeContract(id, contractFile string, responses map[int]contracts.HTTPResponse) *metadata.ContractMeta {
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
				Responses:     responses,
			},
		},
	}
}

// makeProject builds a ProjectMeta where sliceDir/handler.go will be located
// via findHandlerFile. contractID must match the contract the slice serves.
func makeProject(contractID, sliceDir string) *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"testcell": {ID: "testcell", File: "cells/testcell/cell.yaml"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"testcell/testslice": {
				ID:            "testslice",
				BelongsToCell: "testcell",
				File:          sliceDir + "/slice.yaml",
				ContractUsages: []metadata.ContractUsage{
					{Contract: contractID, Role: "serve"},
				},
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

func TestCheckHTTPResponseAlignment(t *testing.T) {
	tests := []struct {
		name        string
		handlerSrc  string
		responses   map[int]contracts.HTTPResponse
		wantErrors  []string // substrings of SeverityError messages
		noHandler   bool     // skip handler file creation → no findings expected
		noAuthMount bool     // write handler src without auth.Mount boilerplate
	}{
		{
			name: "happy_path: handler returns 400 and 404 both declared",
			handlerSrc: `
var _ = http.StatusBadRequest
func h(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusBadRequest)
	w.WriteHeader(http.StatusNotFound)
}
`,
			responses: map[int]contracts.HTTPResponse{
				400: {Description: "bad request", SchemaRef: "err.json"},
				404: {Description: "not found", SchemaRef: "err.json"},
			},
		},
		{
			name: "missing: handler returns 400 contract only declares 401",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusBadRequest)
}
`,
			responses: map[int]contracts.HTTPResponse{
				401: {Description: "unauthorized", SchemaRef: "err.json"},
			},
			wantErrors: []string{"handler returns 400 but contract does not declare it"},
		},
		{
			name: "extra-only: handler returns 401 contract also declares 400 (no finding — extras are not reported)",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusUnauthorized)
}
`,
			responses: map[int]contracts.HTTPResponse{
				400: {Description: "bad request", SchemaRef: "err.json"},
				401: {Description: "unauthorized", SchemaRef: "err.json"},
			},
		},
		{
			name: "5xx: handler returns 500 contract missing it",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusInternalServerError)
}
`,
			responses: map[int]contracts.HTTPResponse{
				400: {Description: "bad request", SchemaRef: "err.json"},
			},
			wantErrors: []string{"handler returns 500 but contract does not declare it"},
		},
		{
			name: "errcode indirect: ErrAuthUserNotFound maps to 404",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	_ = errcode.New(errcode.ErrAuthUserNotFound, "not found")
}
`,
			responses: map[int]contracts.HTTPResponse{
				401: {Description: "unauthorized", SchemaRef: "err.json"},
			},
			wantErrors: []string{"handler returns 404 but contract does not declare it"},
		},
		{
			name: "errcode indirect happy: ErrValidationFailed→400 declared",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	_ = errcode.New(errcode.ErrValidationFailed, "bad input")
}
`,
			responses: map[int]contracts.HTTPResponse{
				400: {Description: "bad request", SchemaRef: "err.json"},
			},
		},
		{
			name:       "skip: contract has no matching slice",
			handlerSrc: "", // irrelevant — noHandler suppresses file creation
			responses: map[int]contracts.HTTPResponse{
				400: {Description: "bad request", SchemaRef: "err.json"},
			},
			noHandler: true,
		},
		{
			name: "non-http contract is skipped",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusBadRequest)
}
`,
			responses: map[int]contracts.HTTPResponse{},
		},
		{
			// Finding 9: errcode.WithDetails wraps an inner errcode.New call;
			// the ast.Inspect recursive walk must find the inner New call's Code.
			name: "errcode.WithDetails wrapping inner New: inner code is found",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	_ = errcode.WithDetails(errcode.New(errcode.ErrAuthUserNotFound, "x"), nil)
}
`,
			responses: map[int]contracts.HTTPResponse{
				404: {Description: "not found", SchemaRef: "err.json"},
			},
		},
		{
			// Finding 9: WithDetails inner code NOT declared → must produce finding.
			name: "errcode.WithDetails inner code missing from contract",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	_ = errcode.WithDetails(errcode.New(errcode.ErrAuthUserNotFound, "x"), nil)
}
`,
			responses: map[int]contracts.HTTPResponse{
				400: {Description: "bad request", SchemaRef: "err.json"},
			},
			wantErrors: []string{"handler returns 404 but contract does not declare it"},
		},
		{
			name: "fail-closed: contract has no auth.Mount correlation in handler file",
			handlerSrc: `
func handleSomething(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusBadRequest)
}
`,
			responses: map[int]contracts.HTTPResponse{
				400: {Description: "bad request", SchemaRef: "err.json"},
			},
			// No auth.Mount for the test contractID → correlation fails → fail-closed error.
			wantErrors: []string{"auth.Mount correlation failed"},
			// The handler source above does not declare auth.Mount for the
			// contractID under test, so writeHandlerFile must NOT add the
			// standard auth.Mount boilerplate. We use a dedicated flag below.
			noAuthMount: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			const contractID = "http.test.v1"
			sliceRelDir := "cells/testcell/slices/testslice"

			// Write slice directory and handler file unless the test wants no handler.
			sliceAbsDir := filepath.Join(root, sliceRelDir)
			require.NoError(t, os.MkdirAll(sliceAbsDir, 0o755))
			if !tc.noHandler && tc.handlerSrc != "" {
				if tc.noAuthMount {
					// Write handler src without auth.Mount boilerplate to test fail-closed.
					path := filepath.Join(sliceAbsDir, "handler.go")
					content := "package x\n\nimport \"net/http\"\nimport \"github.com/ghbvf/gocell/pkg/errcode\"\n\n" + tc.handlerSrc
					require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
				} else {
					writeHandlerFile(t, sliceAbsDir, contractID, tc.handlerSrc)
				}
			}

			project := makeProject(contractID, sliceRelDir)
			if tc.noHandler {
				// No slice registered → findHandlerFile returns "".
				project = &metadata.ProjectMeta{
					Cells:      map[string]*metadata.CellMeta{},
					Slices:     map[string]*metadata.SliceMeta{},
					Contracts:  map[string]*metadata.ContractMeta{},
					Journeys:   map[string]*metadata.JourneyMeta{},
					Assemblies: map[string]*metadata.AssemblyMeta{},
				}
			}

			c := makeContract(contractID, "contracts/http/test/v1/contract.yaml", tc.responses)
			if tc.name == "non-http contract is skipped" {
				c.Kind = "event"
			}

			validator := NewValidator(project, root)
			results := validator.CheckHTTPResponseAlignment([]*metadata.ContractMeta{c}, root)

			var errs []ValidationResult
			for _, r := range results {
				if r.Severity == SeverityError {
					errs = append(errs, r)
				}
			}
			// CH-04 only emits SeverityError findings; extra declarations are
			// intentionally not reported (see CodeContractHealthResponseAlignment doc).
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

// TestErrcodeNameToStatus_AllPairsResolve guards against silent drift between
// the hand-curated `errcodeNameToStatus` table and pkg/errcode/codeToStatus.
// Every entry MUST resolve to a status code >= 400 (else CH-04 silently skips
// it at runtime and slog.Warn fires at init time, hidden in normal CI output).
func TestErrcodeNameToStatus_AllPairsResolve(t *testing.T) {
	for name, status := range errcodeNameToStatus {
		if status < 400 {
			t.Errorf("errcodeNameToStatus[%q] = %d, expected >= 400 (CH-04 only validates 4xx/5xx)", name, status)
		}
	}
	if len(errcodeNameToStatus) == 0 {
		t.Fatal("errcodeNameToStatus is empty — table population failed")
	}
}

// TestCheckHTTPResponseAlignment_HelperWriteStatuses verifies Finding 10:
// CH-04 must detect 4xx/5xx status codes written internally by pkg/httputil
// helpers (ParseUUIDPathParam, WriteDecodeError, ParsePageParamsOrWrite).
func TestCheckHTTPResponseAlignment_HelperWriteStatuses(t *testing.T) {
	tests := []struct {
		name       string
		handlerSrc string
		responses  map[int]contracts.HTTPResponse
		wantErrors []string
	}{
		{
			name: "ParseUUIDPathParam: handler calls it but contract missing 400",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	id, ok := httputil.ParseUUIDPathParam(w, r, "id")
	_ = id; _ = ok
}
`,
			responses: map[int]contracts.HTTPResponse{
				404: {Description: "not found", SchemaRef: "err.json"},
			},
			wantErrors: []string{"handler returns 400 but contract does not declare it"},
		},
		{
			name: "WriteDecodeError: handler declares both 400 and 413, no finding",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	httputil.WriteDecodeError(r.Context(), w, errcode.New(errcode.ErrValidationFailed, "x"))
}
`,
			responses: map[int]contracts.HTTPResponse{
				400: {Description: "bad request", SchemaRef: "err.json"},
				413: {Description: "too large", SchemaRef: "err.json"},
			},
		},
		{
			name: "WriteDecodeError: contract declares 400 but missing 413",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	httputil.WriteDecodeError(r.Context(), w, errcode.New(errcode.ErrValidationFailed, "x"))
}
`,
			responses: map[int]contracts.HTTPResponse{
				400: {Description: "bad request", SchemaRef: "err.json"},
			},
			wantErrors: []string{"handler returns 413 but contract does not declare it"},
		},
		{
			name: "ParsePageParamsOrWrite: handler calls it but contract missing 400",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	_, ok := httputil.ParsePageParamsOrWrite(w, r)
	_ = ok
}
`,
			responses: map[int]contracts.HTTPResponse{
				200: {},
			},
			wantErrors: []string{"handler returns 400 but contract does not declare it"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			const contractID = "http.test.helpers.v1"
			sliceRelDir := "cells/testcell/slices/testslice"

			sliceAbsDir := filepath.Join(root, sliceRelDir)
			require.NoError(t, os.MkdirAll(sliceAbsDir, 0o755))
			writeHandlerFileWithHTTPUtil(t, sliceAbsDir, contractID, tc.handlerSrc)

			project := makeProject(contractID, sliceRelDir)
			c := makeContract(contractID, "contracts/http/test/helpers/v1/contract.yaml", tc.responses)

			validator := NewValidator(project, root)
			results := validator.CheckHTTPResponseAlignment([]*metadata.ContractMeta{c}, root)

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
		})
	}
}
