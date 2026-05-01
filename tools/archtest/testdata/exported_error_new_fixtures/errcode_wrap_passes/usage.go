// Package errcode_wrap_passes verifies that a top-level exported sentinel
// bound to a non-`errors.New` initializer (e.g., a hypothetical errcode
// constructor) is allowed: 0 violations expected. Uses a local helper to
// avoid pulling in pkg/errcode under the fixture module root.
package errcode_wrap_passes

type sentinelError struct{ msg string }

func (e *sentinelError) Error() string { return e.msg }

func newSentinel(msg string) error { return &sentinelError{msg: msg} }

var ErrFoo = newSentinel("foo")
