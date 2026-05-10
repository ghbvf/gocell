package typeseval_test

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

func TestIsGeneratedRelPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		rel  string
		want bool
	}{
		{"generated/contracts/event/x/v1/handler.go", true},
		{"generated/contracts/cell/y/v1/types.go", true},
		{"generated/", true},
		{"generated/x.go", true},
		// Top-level only: "generated" without trailing slash is not under
		// the generated/ tree — could be a hand-written file literally
		// named generated.go (unlikely but unambiguous).
		{"generated.go", false},
		// Hand-written paths must not match.
		{"kernel/outbox/result.go", false},
		{"runtime/foo/bar.go", false},
		{"cells/auditcore/internal/generated/sub.go", false},
		// Empty edge case.
		{"", false},
	}
	for _, c := range cases {
		c := c
		t.Run(c.rel, func(t *testing.T) {
			t.Parallel()
			got := typeseval.IsGeneratedRelPath(c.rel)
			if got != c.want {
				t.Errorf("IsGeneratedRelPath(%q) = %v, want %v", c.rel, got, c.want)
			}
		})
	}
}
