//go:build !unix && !windows

package initialadmin

import (
	"context"
	"errors"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
)

// BootstrapDeps holds the injected repository and utility dependencies.
// Stub definition for unsupported platforms.
type BootstrapDeps struct {
	UserRepo ports.UserRepository
	RoleRepo ports.RoleRepository
	Logger   *slog.Logger
	Clock    clock.Clock
}

// errUnsupportedPlatform is returned on platforms that are neither unix nor windows.
var errUnsupportedPlatform = errors.New("initialadmin: platform not supported")

// envDrivenBootstrapper is a stub on unsupported platforms.
type envDrivenBootstrapper struct{}

// newEnvDrivenBootstrapper always returns errUnsupportedPlatform on unsupported platforms.
func newEnvDrivenBootstrapper(_ BootstrapDeps, _ PasswordHasher) (*envDrivenBootstrapper, error) {
	return nil, errUnsupportedPlatform
}

// ensureAdminFromCreds always returns errUnsupportedPlatform on unsupported
// platforms. Signature mirrors the unix/windows version: (created bool, err error).
func (b *envDrivenBootstrapper) ensureAdminFromCreds(_ context.Context, _ BootstrapCredentials) (bool, error) {
	return false, errUnsupportedPlatform
}
