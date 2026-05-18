// Package type_assertion_red verifies F11: a type assertion target
// holding a blacklisted constructor wrapped in `any`. The pre-Ident-scan
// rule's resolveCellgenCallee handled only SelectorExpr / Ident /
// ParenExpr, so the outer CallExpr's Fun = TypeAssertExpr (`any(x).(T)`)
// was skipped. Ident-scan catches the inner Ident `New` regardless of
// containment in TypeAssertExpr. 1 violation expected on the line
// containing errors.New.
package type_assertion_red

import "errors"

func use() error {
	ctor := any(errors.New).(func(string) error)
	return ctor("via type assertion")
}
