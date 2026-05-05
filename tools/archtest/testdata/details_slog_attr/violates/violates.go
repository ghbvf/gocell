// Package violates is a fixture for DETAILS-SLOG-ATTR-01 negative cases.
// Each function exercises a distinct violation pattern: the legacy
// map[string]any literal form, plus the wire-unsafe slog.Any / Group /
// LogValue constructors whose Attr.Value would carry handler-dependent
// payloads. Parsed by archtest; not intended to compile.
package violates

import (
	"log/slog"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// CallWithMapLiteral is the canonical violation pattern: passing a map
// literal where typed slog.Attr arguments are required.
func CallWithMapLiteral() error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "x",
		errcode.WithDetails(map[string]any{"reason": "legacy"}))
}

// CallWithSlogAny attaches an arbitrary Go value via slog.Any — KindAny
// payload is not wire-stable per stdlib log/slog docs.
func CallWithSlogAny(v any) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "x",
		errcode.WithDetails(slog.Any("payload", v)))
}

// CallWithSlogGroup nests sub-attrs whose serialization shape is handler-
// dependent — KindGroup is not allowed in the wire schema.
func CallWithSlogGroup() error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "x",
		errcode.WithDetails(slog.Group("g", slog.String("inner", "v"))))
}
