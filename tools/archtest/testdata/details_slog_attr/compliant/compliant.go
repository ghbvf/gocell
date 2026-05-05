// Package compliant is a fixture for DETAILS-SLOG-ATTR-01 positive case:
// every errcode.WithDetails call passes typed slog.Attr values, never a
// map[string]any literal. Parsed by archtest; not intended to compile.
package compliant

import (
	"log/slog"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// CallWithSingleAttr is the canonical compliant pattern.
func CallWithSingleAttr() error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "x",
		errcode.WithDetails(slog.String("field", "name")))
}

// CallWithMultipleAttrs verifies multiple slog.Attr arguments are accepted.
func CallWithMultipleAttrs() error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "x",
		errcode.WithDetails(
			slog.String("field", "name"),
			slog.Int("len", 3),
			slog.Bool("required", true),
		))
}

// CallWithEmptyArgs is allowed: WithDetails() is the no-op form.
func CallWithEmptyArgs() error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "x",
		errcode.WithDetails())
}

// CallWithSpread verifies attrs... spread form does not trip the gate (the
// gate only flags map literal arguments, not other expression shapes).
func CallWithSpread(attrs []slog.Attr) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "x",
		errcode.WithDetails(attrs...))
}
