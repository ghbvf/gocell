package auditappendsession_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/cells/auditcore/internal/appender"
	"github.com/ghbvf/gocell/cells/auditcore/slices/auditappendsession"
)

// TestSpec asserts the slice's only knob is wired correctly. HandleEvent /
// actor / framework behavior is covered in cells/auditcore/internal/appender.
func TestSpec(t *testing.T) {
	assert.Equal(t, "auditappendsession", auditappendsession.Spec.Name())
	assert.Equal(t, appender.ActorAcceptUserFallback, auditappendsession.Spec.Mode())
}
