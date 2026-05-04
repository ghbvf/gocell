package governance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
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
func makeContract(id, contractFile string, responses map[int]metadata.HTTPResponseMeta) *metadata.ContractMeta {
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
		responses   map[int]metadata.HTTPResponseMeta
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
			responses: map[int]metadata.HTTPResponseMeta{
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
			responses: map[int]metadata.HTTPResponseMeta{
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
			responses: map[int]metadata.HTTPResponseMeta{
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
			responses: map[int]metadata.HTTPResponseMeta{
				400: {Description: "bad request", SchemaRef: "err.json"},
			},
			wantErrors: []string{"handler returns 500 but contract does not declare it"},
		},
		{
			name: "errcode indirect: KindNotFound maps to 404",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	_ = errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "not found")
}
`,
			responses: map[int]metadata.HTTPResponseMeta{
				401: {Description: "unauthorized", SchemaRef: "err.json"},
			},
			wantErrors: []string{"handler returns 404 but contract does not declare it"},
		},
		{
			name: "errcode kind wins over code name: notfound code with internal kind maps to 500",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	_ = errcode.New(errcode.KindInternal, errcode.ErrAuthUserNotFound, "not found")
}
`,
			responses: map[int]metadata.HTTPResponseMeta{
				404: {Description: "not found", SchemaRef: "err.json"},
			},
			wantErrors: []string{"handler returns 500 but contract does not declare it"},
		},
		{
			name: "errcode indirect happy: ErrValidationFailed→400 declared",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	_ = errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "bad input")
}
`,
			responses: map[int]metadata.HTTPResponseMeta{
				400: {Description: "bad request", SchemaRef: "err.json"},
			},
		},
		{
			name:       "skip: contract has no matching slice",
			handlerSrc: "", // irrelevant — noHandler suppresses file creation
			responses: map[int]metadata.HTTPResponseMeta{
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
			responses: map[int]metadata.HTTPResponseMeta{},
		},
		{
			// Finding 9: errcode.WithDetails wraps an inner errcode.New call;
			// the ast.Inspect recursive walk must find the inner New call's Code.
			name: "errcode.WithDetails wrapping inner New: inner code is found",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	_ = errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "x", errcode.WithDetails(nil))
}
`,
			responses: map[int]metadata.HTTPResponseMeta{
				404: {Description: "not found", SchemaRef: "err.json"},
			},
		},
		{
			// Finding 9: WithDetails inner code NOT declared → must produce finding.
			name: "errcode.WithDetails inner code missing from contract",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	_ = errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "x", errcode.WithDetails(nil))
}
`,
			responses: map[int]metadata.HTTPResponseMeta{
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
			responses: map[int]metadata.HTTPResponseMeta{
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

			validator := NewValidator(project, root, clock.Real())
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

// writeGeneratedHandlerFile writes a minimal handler_gen.go under
// <root>/generated/contracts/<segments...>/ that matches the gocell codegen
// format. The caller provides the status code(s) emitted by the handle method.
//
// The file content mirrors the real generated handler structure:
//   - standard DO NOT EDIT header (detected by isGoCellGeneratedFile)
//   - package-level var contractSpec with the given contractID
//   - func (h *Handler) handle(…) containing the provided statusBody
func writeGeneratedHandlerFile(t *testing.T, root, contractID, statusBody string) string {
	t.Helper()
	segments := strings.Split(contractID, ".")
	parts := append([]string{root, "generated", "contracts"}, segments...)
	dir := filepath.Join(parts...)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "handler_gen.go")
	content := `// Code generated by gocell generate contract. DO NOT EDIT.
// source: examples/test/contracts/` + contractID + `/contract.yaml

package testpkg

import (
	"net/http"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

var contractSpec = wrapper.ContractSpec{ID: "` + contractID + `"}

type Handler struct{}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handle(w, r)
}

func (h *Handler) handle(w http.ResponseWriter, r *http.Request) {
` + statusBody + `
}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// makeCodegenProject builds a ProjectMeta where the contract has Codegen=true.
// No slice is needed for codegen contracts: findHandlerFile uses
// project.Contracts[contractID].Codegen instead of slice ContractUsages.
func makeCodegenProject(contractID string) *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			contractID: {
				ID:      contractID,
				Kind:    "http",
				Codegen: true,
				File:    "contracts/http/test/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

// TestCheckHTTPResponseAlignment_GeneratedHandler verifies that CH-04 works
// correctly when the handler lives in generated/contracts/.../handler_gen.go.
func TestCheckHTTPResponseAlignment_GeneratedHandler(t *testing.T) {
	tests := []struct {
		name       string
		statusBody string
		responses  map[int]metadata.HTTPResponseMeta
		wantErrors []string
	}{
		{
			// Pos test: generated handler returns 400 not declared in contract.
			name: "mismatch: generated handler returns 400 contract missing it",
			statusBody: `	httputil.WriteJSON(w, http.StatusBadRequest, nil)
`,
			responses: map[int]metadata.HTTPResponseMeta{
				404: {Description: "not found", SchemaRef: "err.json"},
			},
			wantErrors: []string{"handler returns 400 but contract does not declare it"},
		},
		{
			// Neg test: generated handler returns 400 and contract declares 400 — no finding.
			name: "aligned: generated handler returns 400 contract declares it",
			statusBody: `	httputil.WriteJSON(w, http.StatusBadRequest, nil)
`,
			responses: map[int]metadata.HTTPResponseMeta{
				400: {Description: "bad request", SchemaRef: "err.json"},
			},
		},
		{
			// Generated handler using DecodeJSONStrict (400 + 413) — both declared.
			name: "aligned: generated handler DecodeJSONStrict 400+413 both declared",
			statusBody: `	var req struct{ Name string }
	if err := httputil.DecodeJSONStrict(r, &req, httputil.DefaultDecodeJSONLimit); err != nil {
		httputil.WriteError(r.Context(), w, err)
		return
	}
`,
			responses: map[int]metadata.HTTPResponseMeta{
				400: {Description: "bad request", SchemaRef: "err.json"},
				413: {Description: "too large", SchemaRef: "err.json"},
			},
		},
		{
			// Generated handler DecodeJSONStrict but contract missing 413.
			name: "mismatch: generated handler DecodeJSONStrict missing 413",
			statusBody: `	var req struct{ Name string }
	if err := httputil.DecodeJSONStrict(r, &req, httputil.DefaultDecodeJSONLimit); err != nil {
		httputil.WriteError(r.Context(), w, err)
		return
	}
`,
			responses: map[int]metadata.HTTPResponseMeta{
				400: {Description: "bad request", SchemaRef: "err.json"},
			},
			wantErrors: []string{"handler returns 413 but contract does not declare it"},
		},
		{
			name: "mismatch: generated handler WriteError with direct KindNotFound error missing 404",
			statusBody: `	httputil.WriteError(r.Context(), w, errcode.New(errcode.KindNotFound, errcode.ErrOrderNotFound, "order not found"))
`,
			responses: map[int]metadata.HTTPResponseMeta{
				400: {Description: "bad request", SchemaRef: "err.json"},
			},
			wantErrors: []string{"handler returns 404 but contract does not declare it"},
		},
		{
			// Neg test: generated handler with StatusCreated (201) is not ≥400, no finding.
			name: "aligned: generated handler only writes 201 success",
			statusBody: `	httputil.WriteJSON(w, 201, nil)
`,
			responses: map[int]metadata.HTTPResponseMeta{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			const contractID = "http.test.generated.v1"

			writeGeneratedHandlerFile(t, root, contractID, tc.statusBody)

			project := makeCodegenProject(contractID)
			c := makeContract(contractID, "contracts/http/test/generated/v1/contract.yaml", tc.responses)

			validator := NewValidator(project, root, clock.Real())
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

// TestCheckHTTPResponseAlignment_LegacyRegression verifies that the legacy
// (non-codegen) handler path still works correctly after the generated handler
// dispatch was added. Codegen=false contracts must still use slice/handler.go.
func TestCheckHTTPResponseAlignment_LegacyRegression(t *testing.T) {
	root := t.TempDir()
	const contractID = "http.test.legacy.regression.v1"
	const sliceRelDir = "cells/legacycell/slices/legacyslice"
	sliceAbsDir := filepath.Join(root, sliceRelDir)
	require.NoError(t, os.MkdirAll(sliceAbsDir, 0o755))

	handlerPath := writeHandlerFile(t, sliceAbsDir, contractID, `
func h(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
}
`)
	require.NotEmpty(t, handlerPath)

	project := makeProject(contractID, sliceRelDir)
	// Explicitly mark the contract as non-codegen in the project contracts map.
	project.Contracts[contractID] = &metadata.ContractMeta{
		ID:      contractID,
		Kind:    "http",
		Codegen: false,
		File:    "contracts/http/test/legacy/v1/contract.yaml",
	}

	c := makeContract(contractID, "contracts/http/test/legacy/v1/contract.yaml",
		map[int]metadata.HTTPResponseMeta{
			400: {Description: "bad request"},
		},
	)

	validator := NewValidator(project, root, clock.Real())
	results := validator.CheckHTTPResponseAlignment([]*metadata.ContractMeta{c}, root)

	var errs []ValidationResult
	for _, r := range results {
		if r.Severity == SeverityError {
			errs = append(errs, r)
		}
	}
	// Legacy handler returns 404 but contract only declares 400 → 1 finding.
	require.Len(t, errs, 1, "expected one finding for undeclared 404")
	assert.Contains(t, errs[0].Message, "handler returns 404 but contract does not declare it")
}

// TestCheckHTTPResponseAlignment_HelperWriteStatuses verifies Finding 10:
// CH-04 must detect 4xx/5xx status codes written internally by pkg/httputil
// helpers (ParseUUIDPathParam, DecodeJSONStrict, ParsePageParamsOrWrite).
func TestCheckHTTPResponseAlignment_HelperWriteStatuses(t *testing.T) {
	tests := []struct {
		name       string
		handlerSrc string
		responses  map[int]metadata.HTTPResponseMeta
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
			responses: map[int]metadata.HTTPResponseMeta{
				404: {Description: "not found", SchemaRef: "err.json"},
			},
			wantErrors: []string{"handler returns 400 but contract does not declare it"},
		},
		{
			name: "DecodeJSONStrict: handler declares both 400 and 413, no finding",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name string }
	if err := httputil.DecodeJSONStrict(r, &req, httputil.DefaultDecodeJSONLimit); err != nil {
		httputil.WriteError(r.Context(), w, err)
	}
}
`,
			responses: map[int]metadata.HTTPResponseMeta{
				400: {Description: "bad request", SchemaRef: "err.json"},
				413: {Description: "too large", SchemaRef: "err.json"},
			},
		},
		{
			name: "DecodeJSONStrict: contract declares 400 but missing 413",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name string }
	if err := httputil.DecodeJSONStrict(r, &req, httputil.DefaultDecodeJSONLimit); err != nil {
		httputil.WriteError(r.Context(), w, err)
	}
}
`,
			responses: map[int]metadata.HTTPResponseMeta{
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
			responses: map[int]metadata.HTTPResponseMeta{
				200: {},
			},
			wantErrors: []string{"handler returns 400 but contract does not declare it"},
		},
		{
			name: "ParsePageParams plus WriteError: handler returns parser error through canonical writer",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	_, err := httputil.ParsePageParams(r)
	if err != nil {
		httputil.WriteError(r.Context(), w, err)
		return
	}
}
`,
			responses: map[int]metadata.HTTPResponseMeta{
				200: {},
			},
			wantErrors: []string{"handler returns 400 but contract does not declare it"},
		},
		{
			name: "WritePublic: kind argument drives status",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	httputil.WritePublic(r.Context(), w, errcode.KindRateLimited, errcode.ErrRateLimited, "rate limited")
}
`,
			responses: map[int]metadata.HTTPResponseMeta{
				400: {Description: "bad request", SchemaRef: "err.json"},
			},
			wantErrors: []string{"handler returns 429 but contract does not declare it"},
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

			validator := NewValidator(project, root, clock.Real())
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
