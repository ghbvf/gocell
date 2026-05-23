//go:build archtest_fixture

// Package redmissingcallsite is a RED fixture for REQUIRED-DEP-NIL-GUARD-01 A2.
// NewService does NOT call validateRequired() — A2 should flag this.
package redmissingcallsite

// Repo is the required dependency interface.
type Repo interface{ Get() }

// Service holds a required interface dependency.
type Service struct {
	repo Repo `gocell:"required"`
}

// NewService constructs a Service but forgets to call validateRequired.
func NewService(repo Repo) (*Service, error) {
	s := &Service{repo: repo}
	// missing: s.validateRequired()
	return s, nil
}
