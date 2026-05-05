//go:build unix || windows

package initialadmin

import (
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
)

// BootstrapDeps holds the injected repository and utility dependencies.
type BootstrapDeps struct {
	UserRepo ports.UserRepository
	RoleRepo ports.RoleRepository
	Logger   *slog.Logger
	Clock    clock.Clock
}
