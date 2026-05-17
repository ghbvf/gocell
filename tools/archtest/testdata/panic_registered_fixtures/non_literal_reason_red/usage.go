// Package non_literal_reason_red verifies that panic(panicregister.Approved(
// fmt.Sprintf("x"), nil)) is caught because reason is not a string literal:
// 1 violation expected (declared via spec.Violation()).
package non_literal_reason_red

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/panicregister"
	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func foo() {
	spec.Violation()
	panic(panicregister.Approved(fmt.Sprintf("x"), nil))
}
