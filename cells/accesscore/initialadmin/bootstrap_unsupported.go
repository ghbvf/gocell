//go:build !unix && !windows

package initialadmin

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// errUnsupportedPlatform is returned by bootstrapper.ensureAdmin on platforms
// where writeCredentialFile is unavailable. Carries ErrCellPlatformUnsupported
// so operators can distinguish "wrong build for this platform" from generic
// configuration errors.
var errUnsupportedPlatform = errcode.New(errcode.ErrCellPlatformUnsupported,
	"initialadmin: bootstrap not supported on this platform")

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
	// Hasher is present for struct-shape parity with the unix/windows build.
	// It is unused on unsupported platforms.
	Hasher PasswordHasher
}

// bootstrapper is a stub on unsupported platforms.
type bootstrapper struct{}

// newBootstrapper returns errUnsupportedPlatform on unsupported platforms.
func newBootstrapper(_ BootstrapDeps, _ bootstrapConfig) (*bootstrapper, error) {
	return nil, errUnsupportedPlatform
}

// ensureAdmin always returns errUnsupportedPlatform on unsupported platforms.
func (b *bootstrapper) ensureAdmin(_ context.Context) (ensureAdminResult, error) {
	return ensureAdminResult{}, errUnsupportedPlatform
}
