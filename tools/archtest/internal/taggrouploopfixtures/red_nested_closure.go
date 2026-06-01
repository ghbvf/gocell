package taggrouploopfixtures

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest"
)

// red_nested_closure puts archtest.RunTyped inside an immediately-invoked
// closure that the loop calls. The RunTyped CallExpr still lives lexically
// within RangeStmt.Body — the rule walks the body subtree, so closure
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
