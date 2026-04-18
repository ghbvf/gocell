package initialadmin_test

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/access-core/internal/initialadmin"
)

func TestRealClock_Now(t *testing.T) {
	t.Parallel()

	before := time.Now()
	c := initialadmin.RealClock{}
	got := c.Now()
	after := time.Now()

	if got.Before(before) || got.After(after) {
		t.Errorf("RealClock.Now() = %v, want between %v and %v", got, before, after)
	}
}
