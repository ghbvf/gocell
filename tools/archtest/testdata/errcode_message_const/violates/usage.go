// Package violates is a fixture for MESSAGE-CONST-LITERAL-01 negative
// case: errcode.New is called with a fmt.Sprintf-formatted message,
// which the scanner must report as a violation. Parsed by archtest in
// pure-AST mode; not intended to compile.
package violates

import "fmt"

// CallWithSprintfMessage is the canonical violation pattern: runtime data
// is interpolated into the message argument instead of WithDetails or
// WithInternal.
func CallWithSprintfMessage(field string) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		fmt.Sprintf("violates: %s is required", field))
}
