//go:build archtest_fixture

// Package redstalegen is a RED fixture for REQUIRED-DEP-NIL-GUARD-01 A1.
// Service has TWO required fields but service_required_gen.go only guards ONE —
// the generator output is stale. A1 should report a mismatch diagnostic.
package redstalegen

// Repo is the primary required dependency interface.
type Repo interface{ Get() }

// Cache is the secondary required dependency interface.
type Cache interface{ Set() }

// Service holds two required interface dependencies.
type Service struct {
	repo  Repo  `gocell:"required"`
	cache Cache `gocell:"required"`
}

// NewService constructs a Service.
func NewService(repo Repo, cache Cache) (*Service, error) {
	s := &Service{repo: repo, cache: cache}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}
