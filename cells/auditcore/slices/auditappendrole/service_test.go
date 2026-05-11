package auditappendrole_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/cells/auditcore/internal/appender"
	"github.com/ghbvf/gocell/cells/auditcore/slices/auditappendrole"
)

// TestSpec asserts the slice's only knob is wired correctly. The role slice
// uniquely uses ActorRequireExplicit (B2-C-05 fail-closed: payload.userId in
// role events identifies the target, not the actor).
func TestSpec(t *testing.T) {
	assert.Equal(t, "auditappendrole", auditappendrole.Spec.Name())
	assert.Equal(t, appender.ActorRequireExplicit, auditappendrole.Spec.Mode())
}
