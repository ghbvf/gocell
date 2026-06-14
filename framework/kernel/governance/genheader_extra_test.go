package governance

import "testing"

// TestIsInTestdata covers the path-segment check used by
// ListGeneratedInHEAD to skip files inside Go testdata/ subtrees.
//
// The function must match a literal path segment, not a substring, so that
// directory names like "testdataFixture" do not get spuriously skipped.
func TestIsInTestdata(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		{"testdata/fixture.go", true},         // first segment
		{"foo/testdata/fixture.go", true},     // middle segment
		{"foo/bar/testdata/fixture.go", true}, // deep middle segment
		{"foo/bar/testdata", true},            // tail segment (dir)
		{"foo/bar.go", false},                 // no testdata anywhere
		{"foo/testdataX/bar.go", false},       // substring, not segment
		{"foo/Xtestdata/bar.go", false},       // substring, not segment
		{"my-testdata/bar.go", false},         // hyphenated lookalike
		{"", false},                           // empty path
		{"testdata", true},                    // single-segment match
	}
	for _, tc := range cases {
		got := isInTestdata(tc.path)
		if got != tc.want {
			t.Errorf("isInTestdata(%q) = %t, want %t", tc.path, got, tc.want)
		}
	}
}
