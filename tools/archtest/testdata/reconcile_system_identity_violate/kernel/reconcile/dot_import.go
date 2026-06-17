package reconcile

import (
	"context"

	. "github.com/ghbvf/gocell/framework/pkg/ctxkeys" //nolint:revive,staticcheck // RED fixture: dot-import bare identifier is the form under test
)

func bypassSystemIdentitySetterViaDotImport(ctx context.Context) context.Context {
	return WithSubjectID(ctx, "bypass")
}
