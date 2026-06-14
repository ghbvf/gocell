// Package errutil provides test-only helpers for inspecting joined error trees.
// All helpers in this package are intended for use in *_test.go files only.
package errutil

// FlattenJoined recursively unwraps an errors.Join tree and returns all leaf
// errors (errors that do not themselves implement Unwrap() []error). The order
// of leaves matches a depth-first left-to-right traversal of the join tree.
//
// Use this helper to assert that every error produced by a multi-step rollback
// or shutdown is reachable via errors.Is / errors.As without relying on the
// exact join structure.
//
// Example:
//
//	errs := errutil.FlattenJoined(err)
//	for _, e := range errs {
//	    var pe *phaseError
//	    if errors.As(e, &pe) { ... }
//	}
func FlattenJoined(err error) []error {
	if err == nil {
		return nil
	}
	type joinUnwrapper interface {
		Unwrap() []error
	}
	if j, ok := err.(joinUnwrapper); ok {
		var out []error
		for _, e := range j.Unwrap() {
			out = append(out, FlattenJoined(e)...)
		}
		return out
	}
	return []error{err}
}
