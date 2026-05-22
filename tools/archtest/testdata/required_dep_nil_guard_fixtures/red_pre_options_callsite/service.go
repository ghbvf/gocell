//go:build archtest_fixture

// Package redpreoptionscallsite is a RED fixture for REQUIRED-DEP-NIL-GUARD-01 A2.
// NewService calls validateRequired() BEFORE the options loop — A2 should flag this.
package redpreoptionscallsite

// Repo is the required dependency interface.
type Repo interface{ Get() }

// Service holds a required interface dependency.
type Service struct {
	repo Repo `gocell:"required"`
}

// Option is the functional option type for Service.
type Option func(*Service)

// NewService constructs a Service but calls validateRequired() before applying opts.
func NewService(repo Repo, opts ...Option) (*Service, error) {
	s := &Service{repo: repo}
	// BUG: validateRequired called before opts loop — required deps set by opts are missed.
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}
