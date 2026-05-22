//go:build archtest_fixture

// Package greenbasic is a GREEN fixture for REQUIRED-DEP-NIL-GUARD-01.
// Service has one required interface field, the generated file matches,
// and NewService calls validateRequired() after options. 0 A2/A3 violations.
package greenbasic

// Repo is the required dependency interface.
type Repo interface{ Get() }

// Service holds a required interface dependency.
type Service struct {
	repo Repo `gocell:"required"`
}

// Option is the functional option type for Service.
type Option func(*Service)

// NewService constructs a Service, applies options, then validates required deps.
func NewService(repo Repo, opts ...Option) (*Service, error) {
	s := &Service{repo: repo}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}
