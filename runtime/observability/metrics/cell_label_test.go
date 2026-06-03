package metrics_test

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

func cellSet(ids ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return m
}

// TestResolveCellLabel covers the closed-set membership funnel: a ctx cell id is
// emitted only when present AND a member of the assembly set; everything else
// (absent, empty ctx value, non-member, empty/nil set) degrades to the sentinel.
func TestResolveCellLabel(t *testing.T) {
	withCell := func(id string) context.Context {
		return ctxkeys.WithCellID(context.Background(), id)
	}

	tests := []struct {
		name  string
		ctx   context.Context
		valid map[string]struct{}
		want  string
	}{
		{"member in set", withCell("accesscore"), cellSet("accesscore", "auditcore"), "accesscore"},
		{"non-member degrades to sentinel", withCell("evilcell"), cellSet("accesscore"), metrics.RuntimeCellSentinel},
		{"absent ctx degrades to sentinel", context.Background(), cellSet("accesscore"), metrics.RuntimeCellSentinel},
		{"empty ctx cell degrades to sentinel", withCell(""), cellSet("accesscore"), metrics.RuntimeCellSentinel},
		{"empty set degrades present cell", withCell("accesscore"), cellSet(), metrics.RuntimeCellSentinel},
		{"nil set degrades present cell", withCell("accesscore"), nil, metrics.RuntimeCellSentinel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := metrics.ResolveCellLabel(tt.ctx, tt.valid).String()
			if got != tt.want {
				t.Fatalf("ResolveCellLabel().String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCellLabelZeroValueIsSentinel locks the degenerate-safe contract: the
// unavoidable Go zero value renders as the framework sentinel, never an empty or
// forged cell label.
func TestCellLabelZeroValueIsSentinel(t *testing.T) {
	var zero metrics.CellLabel
	if got := zero.String(); got != metrics.RuntimeCellSentinel {
		t.Fatalf("zero CellLabel.String() = %q, want sentinel %q", got, metrics.RuntimeCellSentinel)
	}
}
