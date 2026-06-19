package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
)

// TestConfiguredListeners verifies the read-only test-introspection accessor reports
// exactly the listeners registered via WithListener — populated at option-apply time
// (before Run) — sorted by canonical name. Mirrors ManagedResourceRegistrationOrder;
// it lets composition-root tests assert listener IDENTITY (#1755 F4: corebundle's
// operator-credential block registers cell.AdminListener), not just an option count.
func TestConfiguredListeners(t *testing.T) {
	t.Parallel()

	b := New(clock.Real(),
		WithListener(cell.HealthListener, "127.0.0.1:0", []kauth.ListenerAuth{kauth.AuthNone{}}),
		WithListener(cell.AdminListener, "127.0.0.1:0", []kauth.ListenerAuth{kauth.AuthNone{}}),
	)
	assert.Equal(t, []cell.ListenerRef{cell.AdminListener, cell.HealthListener}, b.ConfiguredListeners(),
		"ConfiguredListeners must report registered listeners sorted by canonical name (admin < health)")

	// No listeners registered → empty (non-nil) slice.
	assert.Empty(t, New(clock.Real()).ConfiguredListeners())
}
