package auditappenduser_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/corecells/auditcore/internal/appender"
	"github.com/ghbvf/gocell/corecells/auditcore/slices/auditappenduser"
)

// TestSpec asserts the slice's only knob is wired correctly. HandleEvent /
// actor / framework behavior is covered in corecells/auditcore/internal/appender
// — extending this test with framework checks would re-fork what the
// appender package was extracted to single-source.
func TestSpec(t *testing.T) {
	assert.Equal(t, "auditappenduser", auditappenduser.Spec.Name())
	assert.Equal(t, appender.ActorAcceptUserFallback, auditappenduser.Spec.Mode())
}
