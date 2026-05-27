package appender_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/auditcore/internal/appender"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func assertSpecPanicsWithErrcodeMessage(t *testing.T, wantMessage string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		require.NotNil(t, r, "expected panic")
		err, ok := r.(*errcode.Error)
		require.True(t, ok, "expected *errcode.Error, got %T (%v)", r, r)
		assert.Equal(t, wantMessage, err.Message)
		assert.Equal(t, errcode.KindInternal, err.Kind, "Assertion uses KindInternal")
		assert.Equal(t, errcode.CategoryInfra, err.Category, "Assertion uses CategoryInfra")
	}()
	fn()
}

// TestMustNewSpec_Whitelist locks the closed set of slice names that may
// construct an appender.Spec. Adding a new auditappend* slice requires
// extending the whitelist in spec.go — preventing accidental fan-out and
// making the inventory grep-able from a single source.
func TestMustNewSpec_Whitelist(t *testing.T) {
	cases := []string{
		"auditappenduser",
		"auditappendconfig",
		"auditappendsession",
		"auditappendrole",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			spec := appender.MustNewSpec(name)
			assert.Equal(t, name, spec.Name())
		})
	}
}

func TestMustNewSpec_RejectsUnknownName(t *testing.T) {
	const want = "appender.MustNewSpec: unknown slice name \"auditappendmystery\"" +
		"; whitelist: auditappenduser, auditappendconfig," +
		" auditappendsession, auditappendrole"
	assertSpecPanicsWithErrcodeMessage(t, want, func() {
		appender.MustNewSpec("auditappendmystery")
	})
}
