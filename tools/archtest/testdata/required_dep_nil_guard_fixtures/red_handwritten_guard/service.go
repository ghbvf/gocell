//go:build archtest_fixture

// Package redhandwrittenguard is a RED fixture for REQUIRED-DEP-NIL-GUARD-01 A3.
// service.go contains a hand-written call to validation.IsNilInterface — this
// bypasses the generated funnel. A3 should flag this callsite.
package redhandwrittenguard

import (
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// Repo is the required dependency interface.
type Repo interface{ Get() }

// Service holds a required interface dependency.
type Service struct {
	repo Repo `gocell:"required"`
}

// NewService constructs a Service with a hand-written nil guard — A3 violation.
func NewService(repo Repo) (*Service, error) {
	s := &Service{repo: repo}
	// A3 violation: hand-written IsNilInterface call in service.go
	if validation.IsNilInterface(s.repo) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"redhandwrittenguard.NewService: repo required")
	}
	return s, nil
}
