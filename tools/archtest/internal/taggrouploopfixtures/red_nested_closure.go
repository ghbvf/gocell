package taggrouploopfixtures

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest"
)

// red_nested_closure puts a typed-scope Run (Run(t, Typed(...), ...)) inside an
// immediately-invoked closure that the loop calls. The Run CallExpr still lives
// lexically within RangeStmt.Body — the rule walks the body subtree, so closure
// nesting must not hide the call.
func _(t *testing.T) {
	for _, tagGroup := range archtest.KnownNonDefaultTags() {
		func() {
			_ = archtest.Run(t, archtest.Typed(archtest.TypedOpts{Tags: tagGroup},
				[]string{"./..."}),

				func(p *archtest.Pass) []archtest.Diagnostic { return nil })
		}()
	}
}
