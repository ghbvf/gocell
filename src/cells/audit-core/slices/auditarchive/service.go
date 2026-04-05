// Package auditarchive implements the audit-archive slice: stub implementation
// for Phase 2.
package auditarchive

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
)

const (
	ErrNotImplemented errcode.Code = "ERR_NOT_IMPLEMENTED"
)

// Service is a stub implementation for the audit-archive slice.
type Service struct{}

// NewService creates an audit-archive Service.
func NewService() *Service {
	return &Service{}
}

// Archive is not available in Phase 2.
func (s *Service) Archive(_ context.Context) error {
	return errcode.New(ErrNotImplemented, "audit archive not available in Phase 2")
}
