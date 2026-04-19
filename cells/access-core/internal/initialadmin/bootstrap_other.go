//go:build !unix

package initialadmin

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/runtime/worker"
)

// ErrUnsupportedPlatform is returned by Bootstrapper.EnsureAdmin on non-unix
// platforms where WriteCredentialFile (unix-only, mode 0600) is unavailable.
var ErrUnsupportedPlatform = errors.New("initialadmin: bootstrap not supported on this platform")

// BootstrapDeps holds the injected repository and utility dependencies.
type BootstrapDeps struct {
	UserRepo ports.UserRepository
	RoleRepo ports.RoleRepository
	Logger   *slog.Logger
	Clock    Clock
}

// BootstrapConfig controls the bootstrap behaviour.
type BootstrapConfig struct {
	Username       string
	CredentialPath string
	TTL            time.Duration
	PasswordSource io.Reader
	Scheduler      Scheduler
}

// Bootstrapper is a stub on non-unix platforms.
type Bootstrapper struct{}

// NewBootstrapper returns ErrUnsupportedPlatform on non-unix platforms.
func NewBootstrapper(_ BootstrapDeps, _ BootstrapConfig) (*Bootstrapper, error) {
	return nil, ErrUnsupportedPlatform
}

// EnsureAdmin always returns ErrUnsupportedPlatform on non-unix platforms.
func (b *Bootstrapper) EnsureAdmin(_ context.Context) (worker.Worker, error) {
	return nil, ErrUnsupportedPlatform
}
