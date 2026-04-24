package initialadmin

import (
	"testing"
	"time"
)

func TestRealClock_Now(t *testing.T) {
	t.Parallel()

	before := time.Now()
	c := realClock{}
	got := c.Now()
	after := time.Now()

	if got.Before(before) || got.After(after) {
		t.Errorf("realClock.Now() = %v, want between %v and %v", got, before, after)
	}
}
