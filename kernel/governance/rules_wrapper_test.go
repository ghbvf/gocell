package governance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScanContractSpecLiterals(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "accesscore")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package accesscore

var spec = wrapper.ContractSpec{
	ID:        "http.auth.login.v1",
	Kind:      "http",
	Transport: "http",
	Method:    "POST",
	Path:      "/api/v1/access/sessions/login",
}
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes_test.go"), []byte(`package accesscore
var ignored = wrapper.ContractSpec{ID: "test.only", Kind: "http"}
`), 0o644))

	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	require.Len(t, literals, 1)
	assert.Equal(t, "http.auth.login.v1", literals[0].id)
	assert.Equal(t, "http", literals[0].kind)
	assert.Equal(t, "POST", literals[0].method)
	assert.Equal(t, "/api/v1/access/sessions/login", literals[0].path)
}

func TestValidateContractSpecLiteral(t *testing.T) {
	project := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.login.v1": {
				ID:   "http.auth.login.v1",
				Kind: "http",
				Endpoints: metadata.EndpointsMeta{
					HTTP: &metadata.HTTPTransportMeta{
						Method: "POST",
						Path:   "/api/v1/access/sessions/login",
					},
				},
			},
		},
	}
	v := NewValidator(project, t.TempDir())

	assert.Empty(t, v.validateContractSpecLiteral(contractSpecLiteral{
		file:   "cells/accesscore/routes.go",
		line:   10,
		id:     "http.auth.login.v1",
		kind:   "http",
		method: "POST",
		path:   "/api/v1/access/sessions/login",
	}))

	missing := v.validateContractSpecLiteral(contractSpecLiteral{
		file: "cells/accesscore/routes.go",
		line: 11,
		id:   "http.auth.missing.v1",
	})
	require.Len(t, missing, 1)
	assert.Equal(t, codeFMT18, missing[0].Code)

	mismatch := v.validateContractSpecLiteral(contractSpecLiteral{
		file:   "cells/accesscore/routes.go",
		line:   12,
		id:     "http.auth.login.v1",
		kind:   "event",
		method: "GET",
		path:   "/wrong",
	})
	require.Len(t, mismatch, 3)
}

func TestValidateFMT19WrapperPackageState(t *testing.T) {
	root := t.TempDir()
	wrapperDir := filepath.Join(root, "kernel", "wrapper")
	require.NoError(t, os.MkdirAll(wrapperDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wrapperDir, "state.go"), []byte(`package wrapper

var _ Tracer = NoopTracer{}
var zero NoopTracer = NoopTracer{}
var globalTracer Tracer = nil
var globalSpan *span = nil
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(wrapperDir, "state_test.go"), []byte(`package wrapper
var ignored Tracer = nil
`), 0o644))

	results := NewValidator(&metadata.ProjectMeta{}, root).validateFMT19(true)
	require.Len(t, results, 2)
	assert.Equal(t, codeFMT19, results[0].Code)
	assert.Contains(t, results[0].Message, "globalTracer")
	assert.Contains(t, results[1].Message, "globalSpan")
}
