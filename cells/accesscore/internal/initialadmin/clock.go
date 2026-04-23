package initialadmin

import "time"

// Clock abstracts time retrieval to allow deterministic testing.
// Production code uses RealClock{}; tests may inject a fake implementation.
type Clock interface {
	Now() time.Time
}

// RealClock implements Clock using the system wall clock.
type RealClock struct{}

// Now returns the current wall-clock time.
func (RealClock) Now() time.Time { return time.Now() }
