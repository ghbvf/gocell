// Package bare_string_red verifies that a bare panic("literal") is caught:
// 1 violation expected.
package bare_string_red

func foo() {
	panic("bare")
}
