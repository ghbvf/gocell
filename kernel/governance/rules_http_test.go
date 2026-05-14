package governance

// rules_http_test.go consolidates tests for the three HTTP-contract governance
// rules merged into rules_http.go: CH-04 (response alignment), CH-05 (path-param
// UUID), and CH-06 (typed response envelope).

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

// =============================================================================
// CH-04 — response alignment tests
// =============================================================================

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

var spec = contractspec.ContractSpec{ID: "` + contractID + `"}

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

var spec = contractspec.ContractSpec{ID: "` + contractID + `"}

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
	_ = errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "x", errcode.WithDetails())
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
	_ = errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "x", errcode.WithDetails())
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

var contractSpec = contractspec.ContractSpec{ID: "` + contractID + `"}

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

// makeContractWithAuth builds a ContractMeta for CH-04 tests that includes
// an Auth field on the HTTP transport metadata.
func makeContractWithAuth(
	id, contractFile string,
	responses map[int]metadata.HTTPResponseMeta,
	auth metadata.HTTPAuthMeta,
) *metadata.ContractMeta {
	c := makeContract(id, contractFile, responses)
	c.Endpoints.HTTP.Auth = auth
	return c
}

// TestCheckHTTPResponseAlignment_AuthResponses verifies that CH-04 treats
// status codes declared in auth.responses as covered, so middleware-injected
// codes (e.g. 401 from bootstrap auth, 429 from rate limiter) do not trigger
// a "handler returns N but contract does not declare it" finding even though
// the handler AST never emits those codes.
func TestCheckHTTPResponseAlignment_AuthResponses(t *testing.T) {
	tests := []struct {
		name          string
		handlerSrc    string
		responses     map[int]metadata.HTTPResponseMeta
		authResponses []int
		wantErrors    []string
	}{
		{
			// Core case: 401 and 429 live in auth.responses only.
			// Handler AST emits 200/400/500 but not 401/429.
			// Contract.responses declares 200/400/500.
			// CH-04 must NOT report 401/429 as missing.
			name: "auth.responses covers 401+429 not emitted by handler AST",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusBadRequest)
	w.WriteHeader(http.StatusInternalServerError)
}
`,
			responses: map[int]metadata.HTTPResponseMeta{
				400: {Description: "bad request", SchemaRef: "err.json"},
				500: {Description: "internal error", SchemaRef: "err.json"},
			},
			authResponses: []int{401, 429},
			wantErrors:    nil,
		},
		{
			// Regression: auth.responses does not suppress genuinely missing codes.
			// Handler emits 400 but contract.responses only declares 500.
			// auth.responses covers 401 — not 400. CH-04 must still report 400.
			name: "auth.responses does not suppress genuinely missing handler status",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusBadRequest)
}
`,
			responses: map[int]metadata.HTTPResponseMeta{
				500: {Description: "internal error", SchemaRef: "err.json"},
			},
			authResponses: []int{401, 429},
			wantErrors:    []string{"handler returns 400 but contract does not declare it"},
		},
		{
			// No auth.responses set (nil) — existing behavior unchanged.
			name: "no auth.responses: missing 400 still reported",
			handlerSrc: `
func h(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusBadRequest)
}
`,
			responses: map[int]metadata.HTTPResponseMeta{
				401: {Description: "unauthorized", SchemaRef: "err.json"},
			},
			authResponses: nil,
			wantErrors:    []string{"handler returns 400 but contract does not declare it"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			const contractID = "http.test.auth.v1"
			sliceRelDir := "cells/testcell/slices/testslice"

			sliceAbsDir := filepath.Join(root, sliceRelDir)
			require.NoError(t, os.MkdirAll(sliceAbsDir, 0o755))
			writeHandlerFile(t, sliceAbsDir, contractID, tc.handlerSrc)

			project := makeProject(contractID, sliceRelDir)
			c := makeContractWithAuth(
				contractID,
				"contracts/http/test/auth/v1/contract.yaml",
				tc.responses,
				metadata.HTTPAuthMeta{Responses: tc.authResponses},
			)

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

// =============================================================================
// CH-05 — path-param UUID tests
// =============================================================================

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

var spec = contractspec.ContractSpec{ID: "` + contractID + `"}

func setup(mux http.Handler) {
	auth.Mount(mux, auth.Route{Contract: spec, Handler: http.HandlerFunc(h)})
}

` + src
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// makeUUIDContract builds a ContractMeta with specified UUID path params.
func makeUUIDContract(id, contractFile string, uuidParams []string) *metadata.ContractMeta {
	pp := make(map[string]metadata.ParamSchema, len(uuidParams))
	for _, name := range uuidParams {
		pp[name] = metadata.ParamSchema{Type: "string", Format: "uuid"}
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
							PathParams: map[string]metadata.ParamSchema{
								"key": {Type: "string"}, // no format:uuid
							},
						},
					},
				}
			} else {
				c = makeUUIDContract(contractID, "contracts/http/test/uuid/v1/contract.yaml", tc.uuidParams)
			}

			validator := NewValidator(project, root, clock.Real())
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
var specA = contractspec.ContractSpec{ID: "` + contractA + `"}
var specB = contractspec.ContractSpec{ID: "` + contractB + `"}

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

	validator := NewValidator(project, root, clock.Real())
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

// =============================================================================
// CH-06 — typed response envelope tests
// =============================================================================

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
	assert.Equal(t, codeCH06, results[0].Code)
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
	assert.Equal(t, codeCH06, results[0].Code)
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
