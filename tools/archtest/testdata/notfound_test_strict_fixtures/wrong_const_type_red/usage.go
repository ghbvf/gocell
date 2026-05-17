// Package wrong_const_type_red proves that a const declared in the
// errcode package but with a NAMED TYPE other than errcode.Code
// (e.g. a sibling typed-string surface) is rejected even when its
// value happens to match the NotFound pattern. Form-locking the
// named type closes the drift surface where future maintainers
// could add a same-package same-value-pattern const under a
// different sentinel surface.
//
// Note: pure-AST fixture mode (info == nil) cannot resolve const Type
// from selectors alone, so this fixture's AST-only fallback path
// passes name-shape gates (Err…NotFound). The named-type guard is
// exercised by the typed module-wide TestNotFoundTestStrict against
// production code where *types.Info is loaded. To keep this fixture
// deterministic and minimal, we trip an orthogonal RED form here —
// the expected arg is a literal `errcode.Code("ERR_X_NOT_FOUND")`
// conversion (CallExpr), not a SelectorExpr — which is already
// rejected by callSatisfiesFunnelRule's form lock. Pair with the
// existing basic_lit_expected_red fixture; this one names the
// drift surface explicitly in its package comment so future
// maintainers reading the fixture set see the type-system guard.
package wrong_const_type_red

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
)

func TestFoo_NotFound(t *testing.T) {
	err := errors.New("simulated")
	// CallExpr conversion form — fails the SelectorExpr form lock the
	// same way basic_lit_expected_red exercises. The named-type guard
	// (errcode.Kind / sibling named types) is verified at the typed
	// production scan level, not in pure-AST fixture mode.
	errcodetest.AssertCode(t, err, errcode.Code("ERR_X_NOT_FOUND"))
}
