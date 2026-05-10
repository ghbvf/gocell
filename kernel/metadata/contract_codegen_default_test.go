package metadata

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseContract_CodegenDefaultsTrue is a RED test for K#09 funnel:
// when contract.yaml omits the `codegen:` key, parser must default
// ContractMeta.Codegen to true. Existing contracts that explicitly set
// `codegen: true` continue to parse as true; explicit `codegen: false`
// is the only opt-out.
//
// INVARIANT: SCAFFOLD-CONTRACT-CODEGEN-DEFAULT-TRUE
func TestParseContract_CodegenDefaultsTrue(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		yaml      string
		wantValue bool
	}{
		{
			name: "absent_field_defaults_true",
			yaml: `id: http.example.echo.v1
kind: http
ownerCell: examplecell
consistencyLevel: L1
lifecycle: active
endpoints:
  server: examplecell
  clients: []
schemaRefs:
  request: request.schema.json
  response: response.schema.json
`,
			wantValue: true,
		},
		{
			name: "explicit_true_stays_true",
			yaml: `id: http.example.echo.v1
kind: http
ownerCell: examplecell
consistencyLevel: L1
lifecycle: active
codegen: true
endpoints:
  server: examplecell
  clients: []
`,
			wantValue: true,
		},
		{
			name: "explicit_false_stays_false",
			yaml: `id: http.example.echo.v1
kind: http
ownerCell: examplecell
consistencyLevel: L1
lifecycle: active
codegen: false
endpoints:
  server: examplecell
  clients: []
`,
			wantValue: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fs := fstest.MapFS{
				"contracts/http/example/echo/v1/contract.yaml": &fstest.MapFile{Data: []byte(tc.yaml)},
			}
			pm, err := NewParser("").ParseFS(fs)
			require.NoError(t, err)
			got, ok := pm.Contracts["http.example.echo.v1"]
			require.True(t, ok, "contract not parsed")
			assert.Equal(t, tc.wantValue, got.Codegen,
				"Codegen default true: expected %v for %s", tc.wantValue, tc.name)
		})
	}
}
