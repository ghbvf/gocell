package initialadmin

import "time"

// Clock abstracts time retrieval to allow deterministic testing.
// Production code uses realClock{}; tests may inject a fake implementation.
type Clock interface {
	Now() time.Time
}

// realClock implements Clock using the system wall clock.
type realClock struct{}

// Now returns the current wall-clock time.
func (realClock) Now() time.Time { return time.Now() }
