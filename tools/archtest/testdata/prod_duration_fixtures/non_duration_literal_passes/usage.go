// Package non_duration_literal_passes verifies that non-duration numeric
// literals (int, []int slice literals) are not flagged: no violation expected.
package non_duration_literal_passes

var count = 5
var ids = []int{1, 2, 3}
var rate = 0.5
