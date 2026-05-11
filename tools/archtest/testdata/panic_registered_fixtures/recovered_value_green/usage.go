// Package recovered_value_green verifies that re-panicking a recovered value
// wrapped with panicregister.Approved is accepted: 0 violations expected.
package recovered_value_green

import "github.com/ghbvf/gocell/pkg/panicregister"

func foo() {
	defer func() {
		if r := recover(); r != nil {
			panic(panicregister.Approved("rethrow", r))
		}
	}()
	_ = 1
}
