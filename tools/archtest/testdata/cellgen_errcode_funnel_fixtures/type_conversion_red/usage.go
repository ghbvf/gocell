// Package type_conversion_red verifies F10: a type conversion wrapping
// a blacklisted constructor function value. The pre-Ident-scan rule
// only resolved CallExpr.Fun = {SelectorExpr, Ident, ParenExpr}, so the
// outer CallExpr's Fun = `(func(string) error)(errors.New)` (a nested
// CallExpr, type conversion) was skipped. Ident-scan catches the inner
// Ident `New` regardless of nesting. 1 violation expected on the line
// containing errors.New.
package type_conversion_red

import "errors"

func use() error {
	return (func(string) error)(errors.New)("via type conversion")
}
