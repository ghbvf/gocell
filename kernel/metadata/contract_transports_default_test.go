package metadata

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseContract_TransportsOmitVsExplicitEmpty locks the #1389 omit-vs-empty
// boundary (review C2/F1): the parser defaults `transports:` per kind ONLY when
// the key is OMITTED. An explicitly present-but-empty declaration (`transports:`
// null, or `transports: []`) is deliberately NOT defaulted — it stays empty so
// governance FMT-39's non-empty guard flags the malformed declaration instead of
// the per-kind default silently masking it. Mirrors the K#09 codegen-key funnel
// (TestParseContract_CodegenDefaultsTrue): the yaml-key probe distinguishes
// "omitted" from "explicitly empty".
func TestParseContract_TransportsOmitVsExplicitEmpty(t *testing.T) {
	t.Parallel()

	const base = `id: event.example.thing.v1
kind: event
ownerCell: examplecell
consistencyLevel: L2
lifecycle: active
endpoints:
  publisher: examplecell
`

	cases := []struct {
		name           string
		transportsLine string // inserted after `lifecycle: active`
		want           []string
		wantEmpty      bool
	}{
		{
			// Omitted key → defaulted per kind (event → amqp). This is the
			// byte-identical-compat path that keeps existing contracts unchanged.
			name:           "omitted_defaults_per_kind",
			transportsLine: "",
			want:           []string{"amqp"},
		},
		{
			// Explicit null (`transports:` with no value) → present key, empty
			// value → NOT defaulted, stays empty for FMT-39 to flag.
			name:           "explicit_null_stays_empty",
			transportsLine: "transports:\n",
			wantEmpty:      true,
		},
		{
			// Explicit empty list → NOT defaulted, stays empty for FMT-39.
			name:           "explicit_empty_list_stays_empty",
			transportsLine: "transports: []\n",
			wantEmpty:      true,
		},
		{
			// Explicit populated set → kept verbatim (declaration order).
			name:           "explicit_populated_kept",
			transportsLine: "transports: [amqp, mqtt]\n",
			want:           []string{"amqp", "mqtt"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := base + tc.transportsLine
			fs := fstest.MapFS{
				"contracts/event/example/thing/v1/contract.yaml": &fstest.MapFile{Data: []byte(doc)},
			}
			pm, err := NewParser("").ParseFS(fs)
			require.NoError(t, err)
			got, ok := pm.Contracts["event.example.thing.v1"]
			require.True(t, ok, "contract not parsed")
			if tc.wantEmpty {
				assert.Empty(t, got.Transports,
					"explicit-empty transports must NOT be defaulted (FMT-39 flags it), got %v", got.Transports)
				return
			}
			assert.Equal(t, tc.want, got.Transports)
		})
	}
}
