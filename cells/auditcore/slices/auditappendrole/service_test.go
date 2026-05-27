package auditappendrole_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/cells/auditcore/slices/auditappendrole"
)

// TestSpec asserts the slice's only knob is wired correctly. Actor identity
// comes exclusively from entry.Principal.ActorID (injected by producers via
// InjectPrincipalFromContext); HandleEvent / framework behavior is covered in
// cells/auditcore/internal/appender.
func TestSpec(t *testing.T) {
	assert.Equal(t, "auditappendrole", auditappendrole.Spec.Name())
}
