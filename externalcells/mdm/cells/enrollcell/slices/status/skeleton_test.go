package status

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// TestSkeleton is the verify.unit.status.service target (gocell verify slice
// resolves unit.status.service to -run Skeleton in this package). It is a drift
// guard: the slice's Go identity consts must stay byte-identical to slice.yaml on
// the fields governance reads. Fails CI if a future edit updates one but not the
// other. Seed package that status service unit tests grow into.
func TestSkeleton(t *testing.T) {
	raw, err := os.ReadFile("slice.yaml")
	if err != nil {
		t.Fatalf("read slice.yaml: %v", err)
	}
	var fromYAML metadata.SliceMeta
	if err := yaml.Unmarshal(raw, &fromYAML); err != nil {
		t.Fatalf("unmarshal slice.yaml: %v", err)
	}

	if fromYAML.ID != SliceID {
		t.Errorf("slice.yaml id (%q) drifted from SliceID const (%q) — update doc.go", fromYAML.ID, SliceID)
	}
	if fromYAML.BelongsToCell != BelongsToCell {
		t.Errorf("slice.yaml belongsToCell (%q) drifted from BelongsToCell const (%q) — update doc.go", fromYAML.BelongsToCell, BelongsToCell)
	}
}
