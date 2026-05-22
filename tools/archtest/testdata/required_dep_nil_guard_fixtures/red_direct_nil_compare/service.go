//go:build archtest_fixture

// Package reddirectnilcompare is a RED fixture for REQUIRED-DEP-NIL-GUARD-01 B2.
// A method body directly compares a gocell:"required" field to nil — this
// bypasses the generated funnel in a way A3 cannot catch (no IsNilInterface).
// B2 should flag this callsite.
package reddirectnilcompare

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

// DoWork runs the service operation.
// B2 violation: hand-written `s.repo == nil` guard on a required field in
// a method body bypasses the generated validateRequired() funnel.
func (s *Service) DoWork() error {
	if s.repo == nil { // B2: direct nil compare on gocell:"required" field
		return nil
	}
	s.repo.Get()
	return nil
}
