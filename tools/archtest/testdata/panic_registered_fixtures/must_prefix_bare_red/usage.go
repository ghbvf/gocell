// Package must_prefix_bare_red verifies that Must*-prefixed functions no
// longer receive an exemption — a bare panic inside MustFoo is caught:
// 1 violation expected.
package must_prefix_bare_red

func MustFoo() {
	panic("bare")
}
