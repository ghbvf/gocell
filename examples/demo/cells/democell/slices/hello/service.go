// Package hello implements the hello slice: a minimal pure-compute handler
// that returns a fixed greeting over GET /api/v1/hello. It is the smallest
// possible GoCell slice — no repository, no events, no transaction — and
// exists to show newcomers the Cell → Slice → Contract → handler shape.
package hello

// Service holds the hello slice business logic. It has no dependencies
// (pure compute), so NewService takes no arguments and cannot fail.
type Service struct{}

// NewService returns a new hello Service.
func NewService() *Service { return &Service{} }

// Greeting returns the fixed greeting message served by GET /api/v1/hello.
func (s *Service) Greeting() string { return "hello, gocell" }
