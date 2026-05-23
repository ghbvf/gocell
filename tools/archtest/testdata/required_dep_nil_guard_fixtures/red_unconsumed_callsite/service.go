//go:build archtest_fixture

// Package redunconsumedcallsite is a RED fixture for REQUIRED-DEP-NIL-GUARD-01
// A2. NewService calls validateRequired() but discards its error result, so a
// nil required dependency would not abort construction — A2 should flag this.
package redunconsumedcallsite

// Repo is the required dependency interface.
type Repo interface{ Get() }

// Service holds a required interface dependency.
type Service struct {
	repo Repo `gocell:"required"`
}

// Option is the functional option type for Service.
type Option func(*Service)

// NewService constructs a Service and calls validateRequired() after the
// options loop, but discards the returned error — construction is not aborted
// when a required dep is nil.
func NewService(repo Repo, opts ...Option) (*Service, error) {
	s := &Service{repo: repo}
	for _, o := range opts {
		o(s)
	}
	_ = s.validateRequired() // BUG: error discarded, construction not aborted
	return s, nil
}
