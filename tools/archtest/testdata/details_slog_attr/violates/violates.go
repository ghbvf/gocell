// Package violates is a fixture for DETAILS-SLOG-ATTR-01 negative case:
// errcode.WithDetails is called with the legacy map[string]any{...} literal
// form, which the scanner must report as a violation. Parsed by archtest;
// not intended to compile.
package violates

import (
	"github.com/ghbvf/gocell/pkg/errcode"
)

// CallWithMapLiteral is the canonical violation pattern: passing a map
// literal where typed slog.Attr arguments are required.
func CallWithMapLiteral() error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "x",
		errcode.WithDetails(map[string]any{"reason": "legacy"}))
}
