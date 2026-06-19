package enroll

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// TestSkeleton is the verify.unit.enroll.skeleton target (gocell verify slice
// resolves unit.enroll.skeleton to -run Skeleton in this package). PR-0 has no
// enrollment logic, so the executable check is a drift guard: the slice's Go
// identity consts must stay byte-identical to slice.yaml on the fields governance
// reads. It fails CI if a future edit updates one but not the other, and is the
// seed package MDM-PR1+ grows its enrollment unit tests into. (Removed once codegen
// derives slice metadata, symmetric with cell.go's TestEnrollCell_MetadataMatchesYAML.)
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
