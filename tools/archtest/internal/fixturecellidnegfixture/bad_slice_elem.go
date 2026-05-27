//go:build archtest_fixture

package fixturecellidnegfixture

import "github.com/ghbvf/gocell/kernel/metadata"

// BadSliceElem uses bare literals as elements of JourneyMeta.Cells —
// must be flagged by A1 (slice element position).
var BadSliceElem = &metadata.JourneyMeta{
	ID:    "J-test",
	Cells: []string{"barelitejourney", "anotherbareliteral"},
}
