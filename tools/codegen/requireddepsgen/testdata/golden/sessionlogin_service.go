//go:build archtest_fixture

// Package sessionlogin is a minimal fixture for requireddepsgen golden tests.
package sessionlogin

import (
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	session "github.com/ghbvf/gocell/framework/runtime/auth/session"
)

// Service holds the dependencies for the sessionlogin slice.
type Service struct {
	userRepo     ports.UserRepository      `gocell:"required"`
	sessionStore session.Store             `gocell:"required"`
	txRunner     persistence.CellTxManager `gocell:"required"`
	emitter      outbox.Emitter            `gocell:"required"`
}
