package errcode

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMapCodeToStatus_Known checks the lookup path for a small representative
// sample across status families.
func TestMapCodeToStatus_Known(t *testing.T) {
	cases := []struct {
		code Code
		want int
	}{
		{ErrSessionNotFound, http.StatusNotFound},
		{ErrValidationFailed, http.StatusBadRequest},
		{ErrAuthUnauthorized, http.StatusUnauthorized},
		{ErrAuthForbidden, http.StatusForbidden},
		{ErrSessionConflict, http.StatusConflict},
		{ErrNotImplemented, http.StatusNotImplemented},
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			assert.Equal(t, tc.want, MapCodeToStatus(tc.code))
		})
	}
}

// TestMapCodeToStatus_UnknownDefaults500 ensures unmapped codes degrade to 500
// rather than panicking, while emitting a warn (verified indirectly: no panic).
func TestMapCodeToStatus_UnknownDefaults500(t *testing.T) {
	assert.Equal(t, http.StatusInternalServerError,
		MapCodeToStatus(Code("ERR_NOT_REGISTERED_IN_TABLE")))
}

// TestIsClientError covers the 4xx-range membership predicate.
func TestIsClientError(t *testing.T) {
	cases := []struct {
		code Code
		want bool
	}{
		{ErrValidationFailed, true},       // 400
		{ErrAuthUnauthorized, true},       // 401
		{ErrAuthForbidden, true},          // 403
		{ErrSessionNotFound, true},        // 404
		{ErrSessionConflict, true},        // 409
		{ErrNotImplemented, false},        // 501 — server-side
		{Code("ERR_UNREGISTERED"), false}, // unknown — false (not in table)
		{ErrAuthVerifierConfig, false},    // 500 — server-side
		{ErrNonceStoreFull, false},        // 503 — server-side
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			assert.Equal(t, tc.want, IsClientError(tc.code))
		})
	}
}

func TestPublicCode_KeepsTransportLevel5xxSemantics(t *testing.T) {
	cases := []struct {
		name string
		code Code
		want Code
	}{
		{
			name: "500 internal infra code collapses to internal",
			code: ErrConfigDecryptFailed,
			want: ErrInternal,
		},
		{
			name: "503 transient infra code collapses to service unavailable",
			code: ErrKeyProviderTransient,
			want: ErrServiceUnavailable,
		},
		{
			name: "504 server timeout keeps public machine semantics",
			code: ErrServerTimeout,
			want: ErrServerTimeout,
		},
		{
			name: "4xx public codes pass through",
			code: ErrAuthUnauthorized,
			want: ErrAuthUnauthorized,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, PublicCode(tc.code))
		})
	}
}

func TestPublicCodeForStatus(t *testing.T) {
	cases := []struct {
		status int
		want   Code
	}{
		{http.StatusInternalServerError, ErrInternal},
		{http.StatusServiceUnavailable, ErrServiceUnavailable},
		{http.StatusGatewayTimeout, ErrServerTimeout},
		{http.StatusNotImplemented, ErrInternal},
	}

	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			assert.Equal(t, tc.want, PublicCodeForStatus(tc.status))
		})
	}
}

// TestCodeToStatus_Exhaustive parses pkg/errcode/errcode.go with go/ast,
// extracts every Code constant, and verifies it has an entry in codeToStatus.
// This fails loudly when a new errcode.Code is added without registering an
// HTTP status mapping, forcing the developer to make a conscious choice.
func TestCodeToStatus_Exhaustive(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	errcodeFile := filepath.Join(filepath.Dir(thisFile), "errcode.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, errcodeFile, nil, 0)
	require.NoError(t, err, "failed to parse errcode.go")

	// Collect string values of all `const ... Code = "..."` declarations.
	var codes []string
	for _, decl := range f.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}
		for _, spec := range genDecl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || vs.Type == nil {
				continue
			}
			ident, ok := vs.Type.(*ast.Ident)
			if !ok || ident.Name != "Code" {
				continue
			}
			for _, val := range vs.Values {
				lit, ok := val.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				codes = append(codes, s)
			}
		}
	}

	require.NotEmpty(t, codes, "should find Code constants in errcode.go")

	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			_, registered := codeToStatus[Code(code)]
			assert.True(t, registered,
				"errcode.Code %q has no entry in codeToStatus — add it to the map in status.go", code)
		})
	}
}
