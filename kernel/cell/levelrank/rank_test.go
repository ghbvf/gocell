package levelrank_test

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/cell/levelrank"
)

func TestRank(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want int
	}{
		{"L0", 0},
		{"L1", 1},
		{"L2", 2},
		{"L3", 3},
		{"L4", 4},
		{"", -1},
		{"L5", -1},
		{"l0", -1},
		{"L00", -1},
		{"random", -1},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := levelrank.Rank(tc.in); got != tc.want {
				t.Fatalf("Rank(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestAt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   int
		want string
	}{
		{0, "L0"},
		{1, "L1"},
		{2, "L2"},
		{3, "L3"},
		{4, "L4"},
		{-1, ""},
		{5, ""},
		{100, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run("", func(t *testing.T) {
			t.Parallel()
			if got := levelrank.At(tc.in); got != tc.want {
				t.Fatalf("At(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRankAtRoundTrip asserts the canonical alignment Rank(At(i)) == i for
// every valid index in the L0..L4 taxonomy.
func TestRankAtRoundTrip(t *testing.T) {
	t.Parallel()
	for i := 0; i < len(levelrank.Levels); i++ {
		s := levelrank.At(i)
		if s == "" {
			t.Fatalf("At(%d) returned empty", i)
		}
		if got := levelrank.Rank(s); got != i {
			t.Fatalf("Rank(At(%d)=%q) = %d, want %d", i, s, got, i)
		}
	}
}
