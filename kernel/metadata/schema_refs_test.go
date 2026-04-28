package metadata

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContractDirFromMeta(t *testing.T) {
	assert.Empty(t, ContractDirFromMeta(nil))
	assert.Equal(t, "examples/demo/contracts/http/auth/login/v1", ContractDirFromMeta(&ContractMeta{
		ID:  "http.auth.login.v1",
		Dir: "examples/demo/contracts/http/auth/login/v1",
	}))
	assert.Equal(t, "contracts/http/auth/login/v1", ContractDirFromMeta(&ContractMeta{
		ID: "http.auth.login.v1",
	}))
	assert.Equal(t, "contracts/http/auth/login/v1", ContractDirFromID("http.auth.login.v1"))
}

func TestContractSchemaRefsDeterministicOrder(t *testing.T) {
	c := &ContractMeta{
		ID: "http.auth.login.v1",
		SchemaRefs: SchemaRefsMeta{
			Request:  "request.schema.json",
			Response: "response.schema.json",
			Payload:  "payload.schema.json",
			Headers:  "headers.schema.json",
			Extra: map[string]string{
				"zeta":  "zeta.schema.json",
				"audit": "audit.schema.json",
			},
		},
		Endpoints: EndpointsMeta{
			HTTP: &HTTPTransportMeta{
				Responses: map[int]HTTPResponseMeta{
					500: {SchemaRef: "server-error.schema.json"},
					400: {SchemaRef: "bad-request.schema.json"},
				},
			},
		},
	}

	refs := ContractSchemaRefs(c)
	require.Len(t, refs, 8)
	assert.Equal(t, []ContractSchemaRef{
		{Field: "schemaRefs.request", Ref: "request.schema.json", Scope: SchemaRefScopeContractDir},
		{Field: "schemaRefs.response", Ref: "response.schema.json", Scope: SchemaRefScopeContractDir},
		{Field: "schemaRefs.payload", Ref: "payload.schema.json", Scope: SchemaRefScopeContractDir},
		{Field: "schemaRefs.headers", Ref: "headers.schema.json", Scope: SchemaRefScopeContractDir},
		{Field: "schemaRefs.audit", Ref: "audit.schema.json", Scope: SchemaRefScopeContractDir},
		{Field: "schemaRefs.zeta", Ref: "zeta.schema.json", Scope: SchemaRefScopeContractDir},
		{Field: "endpoints.http.responses[400].schemaRef", Ref: "bad-request.schema.json", Scope: SchemaRefScopeProjectRoot},
		{Field: "endpoints.http.responses[500].schemaRef", Ref: "server-error.schema.json", Scope: SchemaRefScopeProjectRoot},
	}, refs)

	assert.Nil(t, ContractSchemaRefs(nil))
}

func TestResolveContractSchemaRefEmptyRefDoesNotRequireRoot(t *testing.T) {
	resolved, err := ResolveContractSchemaRef("", nil, ContractSchemaRef{Field: "schemaRefs.request"})
	require.NoError(t, err)
	assert.Equal(t, "schemaRefs.request", resolved.Field)
	assert.Empty(t, resolved.AbsPath)
}

func TestResolveContractSchemaRefRejectsInvalidInputs(t *testing.T) {
	c := &ContractMeta{ID: "http.auth.login.v1", Dir: "contracts/http/auth/login/v1"}
	abs := filepath.Join(t.TempDir(), "schema.json")

	tests := []struct {
		name     string
		root     string
		contract *ContractMeta
		ref      ContractSchemaRef
		want     string
	}{
		{
			name:     "project root required",
			contract: c,
			ref:      ContractSchemaRef{Field: "schemaRefs.request", Ref: "request.schema.json"},
			want:     "project root is required",
		},
		{
			name:     "absolute ref rejected",
			root:     t.TempDir(),
			contract: c,
			ref:      ContractSchemaRef{Field: "schemaRefs.request", Ref: abs},
			want:     "absolute paths are not allowed",
		},
		{
			name: "missing contract dir",
			root: t.TempDir(),
			ref:  ContractSchemaRef{Field: "schemaRefs.request", Ref: "request.schema.json"},
			want: "contract directory is unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveContractSchemaRef(tt.root, tt.contract, tt.ref)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			var schemaErr *SchemaRefError
			require.ErrorAs(t, err, &schemaErr)
			assert.Equal(t, tt.ref.Field, schemaErr.Field)
			assert.Equal(t, tt.ref.Ref, schemaErr.Ref)
		})
	}
}

func TestResolveContractSchemaRefScopes(t *testing.T) {
	root := t.TempDir()
	c := &ContractMeta{ID: "http.auth.login.v1", Dir: "contracts/http/auth/login/v1"}
	contractSchema := filepath.Join(root, "contracts/http/auth/login/v1/request.schema.json")
	sharedSchema := filepath.Join(root, "contracts/shared/error.schema.json")
	require.NoError(t, ensureTestFile(contractSchema))
	require.NoError(t, ensureTestFile(sharedSchema))

	contractRef := ContractSchemaRef{
		Field: "schemaRefs.request",
		Ref:   "request.schema.json",
		Scope: SchemaRefScopeContractDir,
	}
	resolved, err := ResolveContractSchemaRef(root, c, contractRef)
	require.NoError(t, err)
	assert.Equal(t, filepath.Clean(contractSchema), resolved.AbsPath)
	assert.Equal(t, "contracts/http/auth/login/v1/request.schema.json", resolved.ProjectRel)
	assert.Equal(t, contractRef, resolved.ContractSchemaRef)

	sharedRef := ContractSchemaRef{
		Field: "endpoints.http.responses[400].schemaRef",
		Ref:   "../../../../shared/error.schema.json",
		Scope: SchemaRefScopeProjectRoot,
	}
	resolved, err = ResolveContractSchemaRef(root, c, sharedRef)
	require.NoError(t, err)
	assert.Equal(t, filepath.Clean(sharedSchema), resolved.AbsPath)
	assert.Equal(t, "contracts/shared/error.schema.json", resolved.ProjectRel)

	sharedRef.Scope = SchemaRefScopeContractDir
	_, err = ResolveContractSchemaRef(root, c, sharedRef)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path escapes")
}

func TestResolveContractSchemaRefRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "..", "outside.schema.json")
	c := &ContractMeta{ID: "http.auth.login.v1", Dir: "contracts/http/auth/login/v1"}

	_, err := ResolveContractSchemaRef(root, c, ContractSchemaRef{
		Field: "endpoints.http.responses[500].schemaRef",
		Ref:   "../../../../../../outside.schema.json",
		Scope: SchemaRefScopeProjectRoot,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path escapes")
	assert.False(t, isWithinRoot(root, outside))
}

func TestResolveContractSchemaRefRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	c := &ContractMeta{ID: "http.auth.login.v1", Dir: "contracts/http/auth/login/v1"}
	link := filepath.Join(root, "contracts/http/auth/login/v1/linked.schema.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "schema.json"), []byte(`{"type":"object"}`), 0o644))
	if err := os.Symlink(filepath.Join(outside, "schema.json"), link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}

	_, err := ResolveContractSchemaRef(root, c, ContractSchemaRef{
		Field: "schemaRefs.request",
		Ref:   "linked.schema.json",
		Scope: SchemaRefScopeContractDir,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path escapes")
}

func TestResolveContractSchemaRefsSkipsEmptyAndStopsOnError(t *testing.T) {
	root := t.TempDir()
	c := &ContractMeta{
		ID:  "http.auth.login.v1",
		Dir: "contracts/http/auth/login/v1",
		SchemaRefs: SchemaRefsMeta{
			Request: "request.schema.json",
			Payload: "",
			Extra: map[string]string{
				"audit": "audit.schema.json",
			},
		},
	}
	require.NoError(t, ensureTestFile(filepath.Join(root, "contracts/http/auth/login/v1/request.schema.json")))
	require.NoError(t, ensureTestFile(filepath.Join(root, "contracts/http/auth/login/v1/audit.schema.json")))

	refs, err := ResolveContractSchemaRefs(root, c)
	require.NoError(t, err)
	require.Len(t, refs, 2)
	assert.Equal(t, []string{"schemaRefs.request", "schemaRefs.audit"}, []string{refs[0].Field, refs[1].Field})

	c.SchemaRefs.Response = "../../escape.schema.json"
	_, err = ResolveContractSchemaRefs(root, c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schemaRefs.response")
}

func TestIsWithinRootHandlesMissingLeaf(t *testing.T) {
	root := t.TempDir()
	assert.True(t, isWithinRoot(root, filepath.Join(root, "missing", "schema.json")))
}

func ensureTestFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(`{"type":"object"}`), 0o644)
}
