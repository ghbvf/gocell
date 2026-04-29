// Package func_local_const_violates verifies that a function-local const
// initializer is NOT a compliant position: 1 violation expected.
package func_local_const_violates

import "time"

func f() {
	const localTimeout = 5 * time.Second
	time.Sleep(localTimeout)
}
