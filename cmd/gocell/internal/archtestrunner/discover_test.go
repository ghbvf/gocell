package archtestrunner

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPartition_ExactlyOnce verifies the partition algorithm mirrors the shell's
// awk 'NR % n == s' semantics: for a sorted test list and Total=K, the union
// over all shards equals the full set and no test appears in two shards.
func TestPartition_ExactlyOnce(t *testing.T) {
	tests := []string{"TestA", "TestB", "TestC", "TestD", "TestE", "TestF"}

	for k := 1; k <= len(tests); k++ {
		k := k
		t.Run("total="+itoa(k), func(t *testing.T) {
			seen := map[string]int{}
			var all []string
			for idx := 0; idx < k; idx++ {
				got := partition(tests, Shard{Index: idx, Total: k})
				for _, name := range got {
					seen[name]++
					all = append(all, name)
				}
			}
			// Every test must appear exactly once across all shards.
			for _, name := range tests {
				assert.Equal(t, 1, seen[name], "test %q should appear in exactly one shard when Total=%d", name, k)
			}
			assert.Len(t, all, len(tests), "union of all shards must equal the full set")
		})
	}
}

// TestPartition_1BasedNRMirror asserts the exact 1-based NR modulo split:
//
//	tests=[A,B,C,D], Total=2
//	  NR=1 (A): 1%2=1 → shard 1
//	  NR=2 (B): 2%2=0 → shard 0
//	  NR=3 (C): 3%2=1 → shard 1
//	  NR=4 (D): 4%2=0 → shard 0
//	  shard0 = {B, D}; shard1 = {A, C}
func TestPartition_1BasedNRMirror(t *testing.T) {
	tests := []string{"TestA", "TestB", "TestC", "TestD"}

	shard0 := partition(tests, Shard{Index: 0, Total: 2})
	shard1 := partition(tests, Shard{Index: 1, Total: 2})

	assert.Equal(t, []string{"TestB", "TestD"}, shard0, "shard0 should contain NR=2,4 entries")
	assert.Equal(t, []string{"TestA", "TestC"}, shard1, "shard1 should contain NR=1,3 entries")
}

// TestPartition_K1_ReturnsAll verifies that Total=1 returns all tests (single shard).
func TestPartition_K1_ReturnsAll(t *testing.T) {
	tests := []string{"TestA", "TestB", "TestC"}
	got := partition(tests, Shard{Index: 0, Total: 1})
	assert.Equal(t, tests, got)
}

// TestPartition_EmptyInput tolerates an empty test list without panic.
func TestPartition_EmptyInput(t *testing.T) {
	got := partition(nil, Shard{Index: 0, Total: 3})
	assert.Empty(t, got)

	got2 := partition([]string{}, Shard{Index: 1, Total: 3})
	assert.Empty(t, got2)
}

// TestPartition_Total0_ReturnsAll verifies that Total=0 returns all tests (no sharding).
func TestPartition_Total0_ReturnsAll(t *testing.T) {
	tests := []string{"TestA", "TestB", "TestC"}
	got := partition(tests, Shard{Total: 0})
	assert.Equal(t, tests, got)
}

// TestPartition_Stability verifies that identical inputs always produce identical output.
func TestPartition_Stability(t *testing.T) {
	tests := []string{"TestAlpha", "TestBeta", "TestGamma", "TestDelta", "TestEpsilon"}
	shard := Shard{Index: 1, Total: 3}

	first := partition(tests, shard)
	second := partition(tests, shard)
	assert.Equal(t, first, second, "partition must be deterministic")
}

// TestPartition_EmptyShardTolerated verifies a shard that gets no tests returns empty slice, not nil.
func TestPartition_EmptyShardTolerated(t *testing.T) {
	// With 2 tests and Total=5:
	//   TestA is NR=1, 1%5=1 → shard 1
	//   TestB is NR=2, 2%5=2 → shard 2
	// So shards 0, 3, 4 will be empty.
	tests := []string{"TestA", "TestB"}
	for _, idx := range []int{0, 3, 4} {
		got := partition(tests, Shard{Index: idx, Total: 5})
		assert.NotNil(t, got, "empty shard result must not be nil (idx=%d)", idx)
		assert.Empty(t, got, "shard %d should be empty for 2 tests with Total=5", idx)
	}
}

// TestValidateShard exercises the validation logic.
func TestValidateShard(t *testing.T) {
	cases := []struct {
		shard   Shard
		wantErr bool
	}{
		{Shard{Total: 0}, false},           // no sharding
		{Shard{Index: 0, Total: 1}, false}, // single shard
		{Shard{Index: 0, Total: 3}, false}, // first of 3
		{Shard{Index: 2, Total: 3}, false}, // last of 3
		{Shard{Index: 3, Total: 3}, true},  // index out of range
		{Shard{Index: -1, Total: 3}, true}, // negative index
		{Shard{Index: 0, Total: -1}, true}, // negative total
	}
	for _, tc := range cases {
		err := validateShard(tc.shard)
		if tc.wantErr {
			require.Error(t, err, "shard %+v should be invalid", tc.shard)
		} else {
			require.NoError(t, err, "shard %+v should be valid", tc.shard)
		}
	}
}

// itoa converts an int to its decimal string representation.
func itoa(n int) string {
	return strconv.Itoa(n)
}
