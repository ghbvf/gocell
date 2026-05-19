package taggrouploopfixtures

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest"
)

// red_var_bound_range is the BS-4 var-indirection bypass: KnownNonDefaultTags()
// is bound to a local var, then the for-range iterates the var (RangeStmt.X is
// an *ast.Ident, not a CallExpr) with archtest.RunTyped in the body. The rule
// must resolve the Ident's object back to the KnownNonDefaultTags binding and
// catch it — same cumulative-RSS pathology, one level of indirection.
func _(t *testing.T) {
	tagGroups := archtest.KnownNonDefaultTags()
	for _, tagGroup := range tagGroups {
		_ = archtest.RunTyped(t, archtest.TypedOpts{Tags: tagGroup},
			[]string{"./..."},
			func(p *archtest.Pass) []archtest.Diagnostic { return nil })
	}
}
