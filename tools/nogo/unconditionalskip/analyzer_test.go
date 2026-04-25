package unconditionalskip_test

import (
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/ghbvf/gocell/tools/nogo/unconditionalskip"
)

// TestAnalyzer exercises the unconditionalskip analyzer against the testdata
// package using the analysistest framework. Each "// want" comment in the
// testdata file declares an expected diagnostic; analysistest verifies that
// the analyzer emits exactly those diagnostics and no others.
//
// Test cases (see testdata/src/a/a.go):
//
//	case 1: Test fn first stmt is t.Skip("...") → REPORT
//	case 2: Test fn first stmt is conditional skip → NO REPORT
//	case 3: Test fn first stmt is t.Skipf("...") → REPORT
//	case 4: Test fn first stmt is t.SkipNow() → REPORT
//	case 5: t.Run sub-function with unconditional Skip → REPORT
//	case 6: TestMain(m *testing.M) → NO REPORT
//	case 7: helper fn with t.Skip but not Test prefix → NO REPORT
//	case 8: Skip not first statement → NO REPORT
func TestAnalyzer(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), unconditionalskip.Analyzer, "a")
}
