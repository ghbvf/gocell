package metadata_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// assemblyWith is a helper to build an *AssemblyMeta quickly for topology tests.
func assemblyWith(cells []string, topo metadata.TopologyMeta) *metadata.AssemblyMeta {
	return &metadata.AssemblyMeta{
		ID:       "testassembly",
		Cells:    metadata.CellRefs(cells...),
		Owner:    metadata.OwnerMeta{Team: "platform", Role: "owner"},
		Topology: topo,
	}
}

// TestCellLocation_Methods verifies the CellLocation helper methods.
func TestCellLocation_Methods(t *testing.T) {
	t.Run("Missing", func(t *testing.T) {
		loc := metadata.CellLocation{Kind: metadata.CellLocationMissing}
		assert.True(t, loc.IsMissing())
		assert.False(t, loc.IsLocal())
		assert.False(t, loc.IsRemote())
		ep, ok := loc.RemoteEndpoint()
		assert.False(t, ok)
		assert.Empty(t, ep)
	})
	t.Run("Local", func(t *testing.T) {
		loc := metadata.CellLocation{Kind: metadata.CellLocationLocal}
		assert.False(t, loc.IsMissing())
		assert.True(t, loc.IsLocal())
		assert.False(t, loc.IsRemote())
		ep, ok := loc.RemoteEndpoint()
		assert.False(t, ok)
		assert.Empty(t, ep)
	})
	t.Run("Remote", func(t *testing.T) {
		loc := metadata.CellLocation{Kind: metadata.CellLocationRemote, Endpoint: "host:8080"}
		assert.False(t, loc.IsMissing())
		assert.False(t, loc.IsLocal())
		assert.True(t, loc.IsRemote())
		ep, ok := loc.RemoteEndpoint()
		assert.True(t, ok)
		assert.Equal(t, "host:8080", ep)
	})
}

// TestClassifyCell covers the three placement states and both topology modes.
func TestClassifyCell(t *testing.T) {
	cells := []string{"alpha", "beta", "gamma"}

	tests := []struct {
		name     string
		asm      *metadata.AssemblyMeta
		cellID   string
		wantKind metadata.CellLocationKind
		wantEP   string
	}{
		// ---- empty topology (all-colocated default) ----
		{
			name:     "empty topology / known cell => Local",
			asm:      assemblyWith(cells, metadata.TopologyMeta{}),
			cellID:   "alpha",
			wantKind: metadata.CellLocationLocal,
		},
		{
			name:     "empty topology / unknown cell => Missing",
			asm:      assemblyWith(cells, metadata.TopologyMeta{}),
			cellID:   "delta",
			wantKind: metadata.CellLocationMissing,
		},

		// ---- non-empty topology ----
		{
			name: "non-empty topology / colocated cell => Local",
			asm: assemblyWith(cells, metadata.TopologyMeta{
				Colocated: []string{"alpha", "beta"},
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: "gamma", Endpoint: "svc.internal:9000"},
				},
			}),
			cellID:   "beta",
			wantKind: metadata.CellLocationLocal,
		},
		{
			name: "non-empty topology / remote cell => Remote with endpoint",
			asm: assemblyWith(cells, metadata.TopologyMeta{
				Colocated: []string{"alpha", "beta"},
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: "gamma", Endpoint: "svc.internal:9000"},
				},
			}),
			cellID:   "gamma",
			wantKind: metadata.CellLocationRemote,
			wantEP:   "svc.internal:9000",
		},
		{
			name: "non-empty topology / unclassified cell => Missing",
			asm: assemblyWith(cells, metadata.TopologyMeta{
				Colocated: []string{"alpha"},
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: "beta", Endpoint: "svc:9000"},
				},
			}),
			cellID:   "gamma", // in asm.Cells but not in topology
			wantKind: metadata.CellLocationMissing,
		},
		{
			name: "non-empty topology / totally unknown cell => Missing",
			asm: assemblyWith(cells, metadata.TopologyMeta{
				Colocated: []string{"alpha", "beta", "gamma"},
			}),
			cellID:   "delta",
			wantKind: metadata.CellLocationMissing,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := metadata.ClassifyCell(tc.asm, tc.cellID)
			assert.Equal(t, tc.wantKind, got.Kind, "Kind mismatch")
			if tc.wantKind == metadata.CellLocationRemote {
				ep, ok := got.RemoteEndpoint()
				require.True(t, ok)
				assert.Equal(t, tc.wantEP, ep)
			}
		})
	}
}

// TestValidateTopologyStructure covers all validation rules.
func TestValidateTopologyStructure(t *testing.T) {
	cells := []string{"alpha", "beta", "gamma"}

	tests := []struct {
		name    string
		asm     *metadata.AssemblyMeta
		wantErr bool
		errSub  string // substring expected in error message if wantErr
	}{
		// ---- happy paths ----
		{
			name:    "empty topology => nil (all-colocated default)",
			asm:     assemblyWith(cells, metadata.TopologyMeta{}),
			wantErr: false,
		},
		{
			name: "exhaustive topology with valid endpoints => nil",
			asm: assemblyWith(cells, metadata.TopologyMeta{
				Colocated: []string{"alpha"},
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: "beta", Endpoint: "host:8080"},
					{CellID: "gamma", Endpoint: "https://remote.svc/"},
				},
			}),
			wantErr: false,
		},
		{
			name: "exhaustive topology with https URL endpoint => nil",
			asm: assemblyWith(cells, metadata.TopologyMeta{
				Colocated: []string{"alpha"},
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: "beta", Endpoint: "https://svc.internal:9000/api"},
					{CellID: "gamma", Endpoint: "host:8080"},
				},
			}),
			wantErr: false,
		},
		{
			name: "remote entry with grpc:// scheme (non-http/https) => error",
			asm: assemblyWith(cells, metadata.TopologyMeta{
				Colocated: []string{"alpha", "beta"},
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: "gamma", Endpoint: "grpc://h:1"},
				},
			}),
			wantErr: true,
			errSub:  "invalid endpoint",
		},
		{
			name: "all colocated exhaustive => nil",
			asm: assemblyWith(cells, metadata.TopologyMeta{
				Colocated: []string{"alpha", "beta", "gamma"},
			}),
			wantErr: false,
		},
		{
			name: "all remote exhaustive => nil",
			asm: assemblyWith(cells, metadata.TopologyMeta{
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: "alpha", Endpoint: "a:1"},
					{CellID: "beta", Endpoint: "b:2"},
					{CellID: "gamma", Endpoint: "c:3"},
				},
			}),
			wantErr: false,
		},

		// ---- mutual exclusion ----
		{
			name: "mutual exclusion: cell in both colocated and remote",
			asm: assemblyWith(cells, metadata.TopologyMeta{
				Colocated: []string{"alpha", "beta"},
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: "alpha", Endpoint: "host:9000"},
					{CellID: "gamma", Endpoint: "host:9001"},
				},
			}),
			wantErr: true,
			errSub:  "mutual exclusion",
		},

		// ---- duplicate cellID within colocated ----
		{
			name: "duplicate cellID in colocated",
			asm: assemblyWith(cells, metadata.TopologyMeta{
				Colocated: []string{"alpha", "alpha", "beta"},
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: "gamma", Endpoint: "host:9000"},
				},
			}),
			wantErr: true,
			errSub:  "duplicate colocated",
		},

		// ---- duplicate cellID within remote ----
		{
			name: "duplicate cellID in remote",
			asm: assemblyWith(cells, metadata.TopologyMeta{
				Colocated: []string{"alpha"},
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: "beta", Endpoint: "host:9000"},
					{CellID: "beta", Endpoint: "host:9001"},
					{CellID: "gamma", Endpoint: "host:9002"},
				},
			}),
			wantErr: true,
			errSub:  "duplicate remote",
		},

		// ---- unknown cellID not in asm.Cells ----
		{
			name: "colocated references cellID not in asm.Cells",
			asm: assemblyWith(cells, metadata.TopologyMeta{
				Colocated: []string{"alpha", "beta", "delta"}, // "delta" not in cells
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: "gamma", Endpoint: "host:9000"},
				},
			}),
			wantErr: true,
			errSub:  "unknown colocated",
		},
		{
			name: "remote references cellID not in asm.Cells",
			asm: assemblyWith(cells, metadata.TopologyMeta{
				Colocated: []string{"alpha", "beta", "gamma"},
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: "delta", Endpoint: "host:9000"}, // "delta" not in cells
				},
			}),
			wantErr: true,
			errSub:  "unknown remote",
		},

		// ---- non-exhaustive: cell in asm.Cells but not classified ----
		{
			name: "non-exhaustive: gamma not classified",
			asm: assemblyWith(cells, metadata.TopologyMeta{
				Colocated: []string{"alpha"},
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: "beta", Endpoint: "host:9000"},
				},
				// gamma missing
			}),
			wantErr: true,
			errSub:  "non-exhaustive topology",
		},

		// ---- empty endpoint ----
		{
			name: "remote entry with empty endpoint",
			asm: assemblyWith(cells, metadata.TopologyMeta{
				Colocated: []string{"alpha", "beta"},
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: "gamma", Endpoint: ""},
				},
			}),
			wantErr: true,
			errSub:  "empty endpoint",
		},

		// ---- whitespace-only endpoint ----
		{
			name: "remote entry with whitespace endpoint",
			asm: assemblyWith(cells, metadata.TopologyMeta{
				Colocated: []string{"alpha", "beta"},
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: "gamma", Endpoint: "   "},
				},
			}),
			wantErr: true,
			errSub:  "empty endpoint",
		},

		// ---- malformed endpoint (not host:port, not valid URL with host) ----
		{
			name: "remote entry with malformed endpoint",
			asm: assemblyWith(cells, metadata.TopologyMeta{
				Colocated: []string{"alpha", "beta"},
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: "gamma", Endpoint: "not-a-valid-addr"},
				},
			}),
			wantErr: true,
			errSub:  "invalid endpoint",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := metadata.ValidateTopologyStructure(tc.asm)
			if tc.wantErr {
				require.Error(t, err, "expected error but got nil")
				if tc.errSub != "" {
					assert.Contains(t, err.Error(), tc.errSub,
						"error message should contain %q", tc.errSub)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestClassifyCell_RemoteWithEmptyEndpoint documents that ClassifyCell does no
// endpoint validation (F9): a remote entry with empty endpoint returns
// CellLocationRemote with an empty endpoint string, and RemoteEndpoint()
// returns ("", true). ValidateTopologyStructure owns endpoint validation.
func TestClassifyCell_RemoteWithEmptyEndpoint(t *testing.T) {
	cells := []string{"alpha", "beta"}
	asm := assemblyWith(cells, metadata.TopologyMeta{
		Colocated: []string{"alpha"},
		Remote: []metadata.TopologyRemoteEntry{
			{CellID: "beta", Endpoint: ""},
		},
	})
	loc := metadata.ClassifyCell(asm, "beta")
	assert.Equal(t, metadata.CellLocationRemote, loc.Kind,
		"ClassifyCell must return CellLocationRemote even when endpoint is empty")
	assert.True(t, loc.IsRemote())
	ep, ok := loc.RemoteEndpoint()
	assert.True(t, ok, "RemoteEndpoint must return (_, true) for CellLocationRemote")
	assert.Equal(t, "", ep, "empty endpoint is preserved as-is; validation is ValidateTopologyStructure's concern")
}

// TestTopologyMeta_ZeroValue confirms the zero value means "empty" / all-colocated.
func TestTopologyMeta_ZeroValue(t *testing.T) {
	var topo metadata.TopologyMeta
	assert.Nil(t, topo.Colocated)
	assert.Nil(t, topo.Remote)

	asm := assemblyWith([]string{"alpha"}, topo)
	loc := metadata.ClassifyCell(asm, "alpha")
	assert.Equal(t, metadata.CellLocationLocal, loc.Kind,
		"zero-value topology should treat known cells as Local")
}
