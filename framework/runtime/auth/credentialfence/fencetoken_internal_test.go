package credentialfence

import (
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// TestMustHave_TypedNilPanics constructs a typed-nil FenceToken
// (var x *fenceToken = nil; var tok FenceToken = x) and verifies
// MustHave panics through the panic-taxonomy funnel with an
// *errcode.Error payload carrying KindInternal. This is the
// internal complement to the external test, which can only
// exercise bare-nil because the fenceToken impl is unexported.
func TestMustHave_TypedNilPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("MustHave did not panic on typed-nil FenceToken")
		}
		err, ok := r.(*errcode.Error)
		if !ok {
			t.Fatalf("MustHave panic payload must be *errcode.Error; got %T", r)
		}
		if err.Kind != errcode.KindInternal {
			t.Errorf("MustHave panic Kind must be KindInternal; got %v", err.Kind)
		}
	}()
	var typed *fenceToken
	var tok FenceToken = typed // typed-nil interface value
	MustHave(tok, "credentialfence.internal_test.callsite")
}
