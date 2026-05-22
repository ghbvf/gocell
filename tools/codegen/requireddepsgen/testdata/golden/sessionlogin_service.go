//go:build archtest_fixture

// Package sessionlogin is a minimal fixture for requireddepsgen golden tests.
package sessionlogin

import (
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/outbox"
	session "github.com/ghbvf/gocell/runtime/auth/session"
)

// Service holds the dependencies for the sessionlogin slice.
type Service struct {
	userRepo     ports.UserRepository     `gocell:"required"`
	sessionStore session.Store            `gocell:"required"`
	txRunner     persistence.CellTxManager `gocell:"required"`
	emitter      outbox.Emitter           `gocell:"required"`
}
