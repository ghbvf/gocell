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
	cases := []struct {
		name string
		mode appender.ActorMode
	}{
		{"auditappenduser", appender.ActorAcceptUserFallback},
		{"auditappendconfig", appender.ActorAcceptUserFallback},
		{"auditappendsession", appender.ActorAcceptUserFallback},
		{"auditappendrole", appender.ActorRequireExplicit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := appender.MustNewSpec(tc.name, tc.mode)
			assert.Equal(t, tc.name, spec.Name())
			assert.Equal(t, tc.mode, spec.Mode())
		})
	}
}

func TestMustNewSpec_RejectsUnknownName(t *testing.T) {
	const want = "appender.MustNewSpec: unknown slice name \"auditappendmystery\"" +
		"; whitelist: auditappenduser, auditappendconfig," +
		" auditappendsession, auditappendrole"
	assertSpecPanicsWithErrcodeMessage(t, want, func() {
		appender.MustNewSpec("auditappendmystery", appender.ActorAcceptUserFallback)
	})
}

// TestActorMode_ZeroValueRejected guards the sealed ActorMode contract.
// A zero-value ActorMode (default-constructed by external code) must not pass
// MustNewSpec — only the package-level ActorAcceptUserFallback /
// ActorRequireExplicit instances are valid.
func TestActorMode_ZeroValueRejected(t *testing.T) {
	assertSpecPanicsWithErrcodeMessage(t,
		"appender.MustNewSpec: invalid ActorMode (zero value); use appender.ActorAcceptUserFallback or appender.ActorRequireExplicit",
		func() {
			appender.MustNewSpec("auditappenduser", appender.ActorMode{})
		})
}
