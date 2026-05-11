// Package non_funnel_err_red verifies that panic(err) without Approved wrap
// is caught: 1 violation expected.
package non_funnel_err_red

func foo(err error) {
	panic(err)
}
