// Package hello implements the hello slice: a minimal pure-compute handler
// that returns a fixed greeting over GET /api/v1/hello. It is the smallest
// possible GoCell slice — no repository, no events, no transaction — and
// exists to show newcomers the Cell → Slice → Contract → handler shape.
package hello

// Service holds the hello slice business logic. It has no dependencies
// (pure compute), so the generated validateRequired() is empty.
type Service struct{}

// NewService returns a new hello Service. It follows the standard slice
// constructor shape (returns an error, runs validateRequired) so the demo
// models the canonical REQUIRED-DEP-NIL-GUARD-01 pattern even though the hello
// Service has no required dependencies — validateRequired (generated, empty)
// always returns nil today.
func NewService() (*Service, error) {
	s := &Service{}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// Greeting returns the fixed greeting message served by GET /api/v1/hello.
func (s *Service) Greeting() string { return "hello, gocell" }
