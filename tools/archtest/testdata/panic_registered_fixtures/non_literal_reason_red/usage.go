// Package non_literal_reason_red verifies that panic(panicregister.Approved(
// fmt.Sprintf("x"), nil)) is caught because reason is not a string literal:
// 1 violation expected.
package non_literal_reason_red

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/panicregister"
)

func foo() {
	panic(panicregister.Approved(fmt.Sprintf("x"), nil))
}
