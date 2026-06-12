package assembly

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// slice builds a SliceMeta with the given cell + contractUsages for the collector tests.
func projSlice(cellID, sliceID string, cus ...metadata.ContractUsage) *metadata.SliceMeta {
	return &metadata.SliceMeta{ID: sliceID, BelongsToCell: cellID, ContractUsages: cus}
}

func subscribeCU(contract, projection, source string) metadata.ContractUsage {
	return metadata.ContractUsage{
		Contract: contract, Role: "subscribe", Handler: "H",
		Projection: projection, ProjectionSource: source,
	}
}

// TestCollectOutboxProjectionTopics is the derivation anti-vacuity + exclusion proof
// for PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01: the topic set is computed
// from slice.yaml contractUsages, includes ONLY explicit-outbox projections (sorted,
// deduped), and excludes saga-journal projections, future explicit sources,
// non-projection CUs, and out-of-assembly cells. An empty source on a projection CU
// fails closed (it is a parser-invariant violation, never defaulted to outbox).
func TestCollectOutboxProjectionTopics(t *testing.T) {
	cellA, cellB, cellOut := "acell", "bcell", "outcell"
	tests := []struct {
		name    string
		slices  map[string]*metadata.SliceMeta
		cells   []metadata.AssemblyCellRef
		want    []string
		wantErr bool
	}{
		{
			name:   "no projections yields empty set (corebundle today)",
			slices: map[string]*metadata.SliceMeta{cellA + "/s": projSlice(cellA, "s", subscribeCU("event.x.v1", "", ""))},
			cells:  []metadata.AssemblyCellRef{{ID: cellA}},
			want:   nil,
		},
		{
			name: "outbox projections sorted + deduped",
			slices: map[string]*metadata.SliceMeta{
				cellA + "/s1": projSlice(
					cellA, "s1",
					subscribeCU("event.beta.v1", "p_beta", "outbox"),
					subscribeCU("event.alpha.v1", "p_alpha", "outbox"),
				),
				cellB + "/s2": projSlice(
					cellB, "s2",
					subscribeCU("event.beta.v1", "p_beta_again", "outbox"), // dup contract
					subscribeCU("event.gamma.v1", "p_gamma", "outbox"),
				),
			},
			cells: []metadata.AssemblyCellRef{{ID: cellA}, {ID: cellB}},
			want:  []string{"event.alpha.v1", "event.beta.v1", "event.gamma.v1"},
		},
		{
			name: "saga-journal, non-projection, and unknown-source CUs are excluded",
			slices: map[string]*metadata.SliceMeta{
				cellA + "/s": projSlice(
					cellA, "s",
					subscribeCU("event.out.v1", "p_out", "outbox"),
					subscribeCU("saga.journal.v1", "p_saga", "saga-journal"), // excluded: saga source
					subscribeCU("event.plain.v1", "", ""),                    // excluded: not a projection
					// positive-allowlist anti-regression: a future explicit projectionSource
					// value must be excluded by construction, not silently journaled.
					subscribeCU("event.future.v1", "p_future", "kinesis"),
				),
			},
			cells: []metadata.AssemblyCellRef{{ID: cellA}},
			want:  []string{"event.out.v1"},
		},
		{
			name: "projections in cells outside the assembly are excluded",
			slices: map[string]*metadata.SliceMeta{
				cellA + "/s":   projSlice(cellA, "s", subscribeCU("event.in.v1", "p_in", "outbox")),
				cellOut + "/s": projSlice(cellOut, "s", subscribeCU("event.skip.v1", "p_skip", "outbox")),
			},
			cells: []metadata.AssemblyCellRef{{ID: cellA}}, // cellOut not in assembly
			want:  []string{"event.in.v1"},
		},
		{
			// fail-closed: a projection CU with an EMPTY projectionSource is a
			// parser-invariant violation (checkProjectionSourcePlacement requires an
			// explicit source whenever projection is set). Reaching the collector means
			// the parser was bypassed; it errors rather than fail-open-defaulting empty
			// to outbox and journaling it into the durable allowlist.
			name: "empty-source projection fails closed (parser bypass)",
			slices: map[string]*metadata.SliceMeta{
				cellA + "/s": projSlice(
					cellA, "s",
					subscribeCU("event.alpha.v1", "p_alpha", ""), // projection set, source empty
				),
			},
			cells:   []metadata.AssemblyCellRef{{ID: cellA}},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewGenerator(&metadata.ProjectMeta{Slices: tc.slices}, "github.com/ghbvf/gocell", "")
			got, err := g.collectOutboxProjectionTopics(tc.cells)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
