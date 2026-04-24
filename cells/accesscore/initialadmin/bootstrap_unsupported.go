//go:build !unix && !windows

package initialadmin

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/runtime/worker"
)

// errUnsupportedPlatform is returned by bootstrapper.ensureAdmin on platforms
// where writeCredentialFile is unavailable.
var errUnsupportedPlatform = errors.New("initialadmin: bootstrap not supported on this platform")

// BootstrapDeps holds the injected repository and utility dependencies.
type BootstrapDeps struct {
	UserRepo ports.UserRepository
	RoleRepo ports.RoleRepository
	Logger   *slog.Logger
	Clock    Clock
}

// bootstrapConfig controls the bootstrap behaviour.
type bootstrapConfig struct {
	Username       string
	CredentialPath string
	TTL            time.Duration
	PasswordSource io.Reader
	Scheduler      Scheduler
}

// bootstrapper is a stub on unsupported platforms.
type bootstrapper struct{}

// newBootstrapper returns errUnsupportedPlatform on unsupported platforms.
func newBootstrapper(_ BootstrapDeps, _ bootstrapConfig) (*bootstrapper, error) {
	return nil, errUnsupportedPlatform
}

// ensureAdmin always returns errUnsupportedPlatform on unsupported platforms.
func (b *bootstrapper) ensureAdmin(_ context.Context) (worker.Worker, error) {
	return nil, errUnsupportedPlatform
}
