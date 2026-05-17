// Package aliased_import_violates verifies that aliased imports of the
// stdlib `errors` package are still caught (typed-info resolution rather
// than ident-name matching): 1 violation expected (declared via spec.Violation()).
package aliased_import_violates

import (
	errs "errors"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

var _ = func() any { spec.Violation(); return nil }()

var ErrAliased = errs.New("aliased")
