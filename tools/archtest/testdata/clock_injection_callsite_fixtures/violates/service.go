// Package violates is a fixture for archtest CLOCK-INJECTION-TEST-CALLSITE-01
// negative case: a test file calls NewService with options but omits WithClock.
package violates

// Clock is a minimal time-source interface (mirrors kernel/clock.Clock shape).
type Clock interface {
	Now() interface{}
}

// Option configures Service.
type Option func(*Service)

// WithClock sets the clock on Service. clk must not be nil.
func WithClock(clk Clock) Option {
	return func(s *Service) { s.clk = clk }
}

// WithName sets an optional name for testing purposes.
func WithName(name string) Option {
	return func(s *Service) { s.name = name }
}

// Service is a prod-style service that requires a Clock.
type Service struct {
	clk  Clock
	name string
}

// NewService constructs a Service. A Clock must be provided via WithClock.
func NewService(opts ...Option) *Service {
	s := &Service{}
	for _, o := range opts {
		o(s)
	}
	if s.clk == nil {
		panic("violates.NewService: clock required — use WithClock(...)")
	}
	return s
}
