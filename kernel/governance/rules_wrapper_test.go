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

import "github.com/ghbvf/gocell/kernel/wrapper"

var spec = wrapper.ContractSpec{
	ID:        "http.auth.login.v1",
	Kind:      "http",
	Transport: "http",
	Method:    "POST",
	Path:      "/api/v1/access/sessions/login",
}
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes_test.go"), []byte(`package accesscore

import "github.com/ghbvf/gocell/kernel/wrapper"

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

// TestScanContractSpecLiterals_EventSpecCall verifies FMT-18 picks up the
// wrapper.EventSpec("id", "transport") helper-constructor form so ID literals
// passed via the helper participate in the YAML cross-check.
func TestScanContractSpecLiterals_EventSpecCall(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "accesscore")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package accesscore

import "github.com/ghbvf/gocell/kernel/wrapper"

var spec = wrapper.EventSpec("event.role.assigned.v1", "amqp")
`), 0o644))

	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	require.Len(t, literals, 1)
	assert.Equal(t, "event.role.assigned.v1", literals[0].id)
	assert.Equal(t, "event", literals[0].kind)
	assert.Equal(t, "event.role.assigned.v1", literals[0].topic)
}

// TestScanContractSpecLiterals_ResolvesStringConst verifies that
// wrapper.ContractSpec{...} literals whose field values reference
// package-level string constants are resolved at scan time — so both
// `Path: "/api/v1/..."` and `Path: pathUserByID` flow through the same
// validation, preventing the pre-F1 escape hatch where constant
// references silently bypassed the YAML cross-check.
func TestScanContractSpecLiterals_ResolvesStringConst(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "accesscore")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package accesscore

import "github.com/ghbvf/gocell/kernel/wrapper"

const (
	pathUserByID = "/api/v1/access/users/{id}"
	TopicUserCreated = "event.user.created.v1"
)

var (
	specGet = wrapper.ContractSpec{
		ID: "http.auth.user.get.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: pathUserByID,
	}
	specEvent = wrapper.ContractSpec{
		ID: TopicUserCreated, Kind: "event", Transport: "amqp",
		Topic: TopicUserCreated,
	}
	specCall = wrapper.EventSpec(TopicUserCreated, "amqp")
)
`), 0o644))

	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	require.Len(t, literals, 3)

	// Struct literal with path resolved via const.
	assert.Equal(t, "http.auth.user.get.v1", literals[0].id)
	assert.Equal(t, "/api/v1/access/users/{id}", literals[0].path)

	// Struct literal with both ID and Topic via const.
	assert.Equal(t, "event.user.created.v1", literals[1].id)
	assert.Equal(t, "event.user.created.v1", literals[1].topic)

	// EventSpec call with const identifier.
	assert.Equal(t, "event.user.created.v1", literals[2].id)
	assert.Equal(t, "event", literals[2].kind)
	assert.Equal(t, "event.user.created.v1", literals[2].topic)
}

// TestScanContractSpecLiterals_HonoursImportAlias verifies that FMT-18
// discovers wrapper.ContractSpec / wrapper.EventSpec even when the file
// imports kernel/wrapper under a non-default local name, e.g.
//
//	import kw "github.com/ghbvf/gocell/kernel/wrapper"
//
// Pre-F-2 the scanner hard-coded `pkg.Name == "wrapper"` and silently
// skipped alias forms, a governance escape hatch.
func TestScanContractSpecLiterals_HonoursImportAlias(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "accesscore")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package accesscore

import kw "github.com/ghbvf/gocell/kernel/wrapper"

var (
	specHTTP  = kw.ContractSpec{ID: "http.auth.login.v1", Kind: "http", Transport: "http", Method: "POST", Path: "/x"}
	specEvent = kw.EventSpec("event.aliased.v1", "amqp")
)
`), 0o644))

	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	require.Len(t, literals, 2)
	assert.Equal(t, "http.auth.login.v1", literals[0].id)
	assert.Equal(t, "event.aliased.v1", literals[1].id)
}

// TestScanContractSpecLiterals_SkipsFilesWithoutWrapperImport ensures the
// scanner short-circuits non-wrapper files so an accidentally-matching
// `somepkg.ContractSpec{...}` literal in an unrelated file never produces
// a false positive.
func TestScanContractSpecLiterals_SkipsFilesWithoutWrapperImport(t *testing.T) {
	root := t.TempDir()
	cellsDir := filepath.Join(root, "cells", "accesscore")
	require.NoError(t, os.MkdirAll(cellsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellsDir, "routes.go"), []byte(`package accesscore

import "example.com/other/wrapper"

var spec = wrapper.ContractSpec{ID: "imposter.v1", Kind: "http", Transport: "http", Method: "GET", Path: "/x"}
`), 0o644))

	literals, err := scanContractSpecLiterals(filepath.Join(root, "cells"))
	require.NoError(t, err)
	assert.Empty(t, literals, "non-kernel/wrapper import must not produce FMT-18 literals")
}

// TestValidateContractSpecLiteral_UnresolvedWarns verifies the F-3 fix:
// EventSpec/ContractSpec invocations whose ID cannot be resolved to a
// string literal produce a visible FMT-18 error instead of being silently
// dropped.
func TestValidateContractSpecLiteral_UnresolvedWarns(t *testing.T) {
	v := NewValidator(&metadata.ProjectMeta{}, t.TempDir())
	results := v.validateContractSpecLiteral(contractSpecLiteral{
		file:       "cells/mystery/mystery.go",
		line:       42,
		kind:       "event",
		unresolved: true,
	})
	require.Len(t, results, 1)
	assert.Equal(t, codeFMT18, results[0].Code)
	assert.Contains(t, results[0].Message, "could not be resolved")
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
