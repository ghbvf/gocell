//go:build archtest_fixture

// Package redbareservicereturn is a RED fixture for REQUIRED-DEP-NIL-GUARD-01
// A2. The Service struct has a gocell:"required" field (so a real
// validateRequired is generated), but NewService returns a bare *Service with
// no error result — it cannot propagate the guard and silently skips it. A2
// should flag the missing error return.
package redbareservicereturn

// Repo is the required dependency interface.
type Repo interface{ Get() }

// Service holds a required interface dependency.
type Service struct {
	repo Repo `gocell:"required"`
}

// NewService returns a bare *Service: with no error result it cannot propagate
// the generated validateRequired guard, so a nil required dep slips through.
func NewService(repo Repo) *Service {
	return &Service{repo: repo}
}
