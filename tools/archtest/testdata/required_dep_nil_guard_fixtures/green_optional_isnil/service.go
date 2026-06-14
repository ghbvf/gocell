//go:build archtest_fixture

// Package greenoptionalisnil is a GREEN fixture for REQUIRED-DEP-NIL-GUARD-01
// A3. The Service has a required field (guarded by the generated method) AND an
// optional dependency wired via a builder-noop option that uses
// validation.IsNilInterface(param) for typed-nil safety. A3 must NOT flag the
// option's IsNilInterface call (it is not a required-field guard).
package greenoptionalisnil

import "github.com/ghbvf/gocell/framework/pkg/validation"

// Repo is the required dependency interface.
type Repo interface{ Get() }

// Metrics is an optional dependency interface.
type Metrics interface{ Inc() }

// Service holds a required dep and an optional dep.
type Service struct {
	repo    Repo `gocell:"required"`
	metrics Metrics
}

// Option is the functional option type for Service.
type Option func(*Service)

// WithMetrics injects an optional metrics recorder; typed-nil inputs are not
// stored (builder-noop). This IsNilInterface call is on an option param, not a
// required field, so A3 must allow it.
func WithMetrics(m Metrics) Option {
	return func(s *Service) {
		if !validation.IsNilInterface(m) {
			s.metrics = m
		}
	}
}

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
