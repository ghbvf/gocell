package auditappendconfig_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/corecells/auditcore/internal/appender"
	"github.com/ghbvf/gocell/corecells/auditcore/slices/auditappendconfig"
)

// TestSpec asserts the slice's only knob is wired correctly. HandleEvent /
// actor / framework behavior is covered in corecells/auditcore/internal/appender.
func TestSpec(t *testing.T) {
	assert.Equal(t, "auditappendconfig", auditappendconfig.Spec.Name())
	assert.Equal(t, appender.ActorAcceptUserFallback, auditappendconfig.Spec.Mode())
}
