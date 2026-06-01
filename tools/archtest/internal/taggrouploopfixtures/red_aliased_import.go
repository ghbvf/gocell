package taggrouploopfixtures

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest"
	kt "github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// red_aliased_import uses an aliased typeseval import (kt) to verify that
// ResolvePackageRef resolves the aliased qualified call back to the same
// *types.Func identity as the qualified import shape. The rule must catch
// this alongside the bare and qualified shapes (no façade gap).
func _(t *testing.T) {
	for _, tagGroup := range kt.KnownNonDefaultTags() {
		_ = archtest.Run(t, archtest.Typed(archtest.TypedOpts{Tags: tagGroup},
			[]string{"./..."}),

			func(p *archtest.Pass) []archtest.Diagnostic { return nil })
	}
}
