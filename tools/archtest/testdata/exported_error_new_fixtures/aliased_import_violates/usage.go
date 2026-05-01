// Package aliased_import_violates verifies that aliased imports of the
// stdlib `errors` package are still caught (typed-info resolution rather
// than ident-name matching): 1 violation expected.
package aliased_import_violates

import errs "errors"

var ErrAliased = errs.New("aliased")
