package metadata_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
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

// group is a tiny constructor to keep the table-driven cases readable.
func group(role string, endpoint string, cells ...string) metadata.TopologyGroup {
	return metadata.TopologyGroup{Role: role, Cells: cells, Endpoint: endpoint}
}

// TestValidateTopologyStructure covers all groups-partition validation rules.
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
			name: "single group covering all cells => nil",
			asm: assemblyWith(cells, metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
				group("monolith", "host:8080", "alpha", "beta", "gamma"),
			}}),
			wantErr: false,
		},
		{
			name: "two-group exhaustive partition with host:port endpoints => nil",
			asm: assemblyWith(cells, metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
				group("core", "core.svc:9000", "alpha"),
				group("edge", "edge.svc:9001", "beta", "gamma"),
			}}),
			wantErr: false,
		},
		{
			name: "two-group partition with https endpoints => nil",
			asm: assemblyWith(cells, metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
				group("core", "https://core.svc:9443", "alpha", "beta"),
				group("edge", "https://edge.svc:9443", "gamma"),
			}}),
			wantErr: false,
		},

		// ---- role validation ----
		{
			name: "empty role",
			asm: assemblyWith(cells, metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
				group("", "host:8080", "alpha", "beta", "gamma"),
			}}),
			wantErr: true,
			errSub:  "role must be non-empty",
		},
		{
			name: "duplicate role",
			asm: assemblyWith(cells, metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
				group("core", "core.svc:9000", "alpha"),
				group("core", "edge.svc:9001", "beta", "gamma"),
			}}),
			wantErr: true,
			errSub:  "duplicate group role",
		},

		// ---- group cells validation ----
		{
			name: "group with no cells",
			asm: assemblyWith(cells, metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
				group("core", "core.svc:9000"),
				group("edge", "edge.svc:9001", "alpha", "beta", "gamma"),
			}}),
			wantErr: true,
			errSub:  "at least one cell",
		},
		{
			name: "cell in more than one group (mutual exclusion)",
			asm: assemblyWith(cells, metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
				group("core", "core.svc:9000", "alpha", "beta"),
				group("edge", "edge.svc:9001", "alpha", "gamma"), // alpha twice
			}}),
			wantErr: true,
			errSub:  "more than one group",
		},
		{
			name: "cell repeated within a single group",
			asm: assemblyWith(cells, metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
				group("core", "core.svc:9000", "alpha", "alpha", "beta"),
				group("edge", "edge.svc:9001", "gamma"),
			}}),
			wantErr: true,
			errSub:  "more than one group",
		},
		{
			name: "group references cellID not in asm.Cells",
			asm: assemblyWith(cells, metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
				group("core", "core.svc:9000", "alpha", "beta"),
				group("edge", "edge.svc:9001", "gamma", "delta"), // delta unknown
			}}),
			wantErr: true,
			errSub:  "not declared in assembly cells",
		},

		// ---- exhaustiveness ----
		{
			name: "non-exhaustive: gamma not assigned to any group",
			asm: assemblyWith(cells, metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
				group("core", "core.svc:9000", "alpha"),
				group("edge", "edge.svc:9001", "beta"),
				// gamma missing
			}}),
			wantErr: true,
			errSub:  "non-exhaustive",
		},

		// ---- endpoint validation ----
		{
			name: "empty endpoint",
			asm: assemblyWith(cells, metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
				group("core", "", "alpha", "beta", "gamma"),
			}}),
			wantErr: true,
			errSub:  "empty endpoint",
		},
		{
			name: "whitespace-only endpoint",
			asm: assemblyWith(cells, metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
				group("core", "   ", "alpha", "beta", "gamma"),
			}}),
			wantErr: true,
			errSub:  "empty endpoint",
		},
		{
			name: "malformed endpoint",
			asm: assemblyWith(cells, metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
				group("core", "not-a-valid-addr", "alpha", "beta", "gamma"),
			}}),
			wantErr: true,
			errSub:  "invalid endpoint",
		},
		{
			name: "grpc:// scheme endpoint (non-http/https)",
			asm: assemblyWith(cells, metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
				group("core", "grpc://h:1", "alpha", "beta", "gamma"),
			}}),
			wantErr: true,
			errSub:  "invalid endpoint",
		},
		{
			// #1966 review P2.9: an endpoint carrying a path/query/fragment would be
			// silently truncated by the remote transport (only scheme+host kept), so
			// it is rejected at config-validation time (fail-closed).
			name: "endpoint with path",
			asm: assemblyWith(cells, metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
				group("core", "https://core.svc:9000/api", "alpha", "beta", "gamma"),
			}}),
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

// TestCellGroup covers cell→group lookup across the three states.
func TestCellGroup(t *testing.T) {
	cells := []string{"alpha", "beta", "gamma"}

	t.Run("empty topology => not found", func(t *testing.T) {
		asm := assemblyWith(cells, metadata.TopologyMeta{})
		_, ok := metadata.CellGroup(asm, "alpha")
		assert.False(t, ok, "no groups declared => cell is in no explicit group")
	})

	asm := assemblyWith(cells, metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
		group("core", "core.svc:9000", "alpha", "beta"),
		group("edge", "edge.svc:9001", "gamma"),
	}})

	t.Run("cell in core group", func(t *testing.T) {
		g, ok := metadata.CellGroup(asm, "beta")
		require.True(t, ok)
		assert.Equal(t, "core", g.Role)
		assert.Equal(t, "core.svc:9000", g.Endpoint)
	})
	t.Run("cell in edge group", func(t *testing.T) {
		g, ok := metadata.CellGroup(asm, "gamma")
		require.True(t, ok)
		assert.Equal(t, "edge", g.Role)
	})
	t.Run("unknown cell => not found", func(t *testing.T) {
		_, ok := metadata.CellGroup(asm, "delta")
		assert.False(t, ok)
	})
}

// TestSameGroup covers the co-location predicate used by governance TOPO-13.
func TestSameGroup(t *testing.T) {
	cells := []string{"alpha", "beta", "gamma"}

	t.Run("empty topology => all co-located", func(t *testing.T) {
		asm := assemblyWith(cells, metadata.TopologyMeta{})
		assert.True(t, metadata.SameGroup(asm, "alpha", "gamma"),
			"no groups => single-process default => co-located")
	})

	asm := assemblyWith(cells, metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
		group("core", "core.svc:9000", "alpha", "beta"),
		group("edge", "edge.svc:9001", "gamma"),
	}})

	t.Run("same group => true", func(t *testing.T) {
		assert.True(t, metadata.SameGroup(asm, "alpha", "beta"))
	})
	t.Run("different group => false", func(t *testing.T) {
		assert.False(t, metadata.SameGroup(asm, "alpha", "gamma"))
	})
	t.Run("unknown cell => false (fail-closed)", func(t *testing.T) {
		assert.False(t, metadata.SameGroup(asm, "alpha", "delta"))
	})
}

// TestTopologyMeta_ZeroValue confirms the zero value means "empty" / all-colocated.
func TestTopologyMeta_ZeroValue(t *testing.T) {
	var topo metadata.TopologyMeta
	assert.Nil(t, topo.Groups)

	asm := assemblyWith([]string{"alpha", "beta"}, topo)
	assert.True(t, metadata.SameGroup(asm, "alpha", "beta"),
		"zero-value topology treats all cells as co-located")
	_, ok := metadata.CellGroup(asm, "alpha")
	assert.False(t, ok, "zero-value topology assigns no explicit group")
}
