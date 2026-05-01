// Package short_err_name_passes verifies that names that share the "Err"
// prefix but are NOT the exported-sentinel naming convention (Err followed
// by an uppercase ASCII letter) are not flagged: 0 violations expected.
//
// Examples that the gate must NOT flag:
//   - Errno: lowercase 4th rune ("n")
//   - Errors: lowercase 4th rune ("o")
package short_err_name_passes

import "errors"

var Errno = errors.New("posix-style errno")
var Errors = errors.New("plural noun, not the sentinel convention")
