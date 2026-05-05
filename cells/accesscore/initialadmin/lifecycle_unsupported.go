//go:build !unix && !windows

package initialadmin

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
)

// Lifecycle is a stub on unsupported platforms. WithInitialAdminBootstrap can
// be applied (so composition-root code compiles uniformly across platforms),
// but cell.Init invokes PlatformSupported() before any method on this stub is
// exercised, returning ErrCellPlatformUnsupported.
type Lifecycle struct{}

// LifecycleOption is a no-op on unsupported platforms.
type LifecycleOption func(*Lifecycle)

// The exported With* options accept and discard their arguments so callers can
// use the same option chain on any GOOS without conditional wiring.
func WithUsername(string) LifecycleOption               { return func(*Lifecycle) {} }
func WithPasswordHasher(PasswordHasher) LifecycleOption { return func(*Lifecycle) {} }
func WithClock(clock.Clock) LifecycleOption             { return func(*Lifecycle) {} }

// BootstrapCredentials is the unsupported-platform stub of the same type in lifecycle.go.
type BootstrapCredentials struct {
	Username []byte
	Password []byte
}

// NewLifecycle returns a stub Lifecycle. cell.Init's PlatformSupported() check
// surfaces the unsupported-platform failure before any method runs.
func NewLifecycle(_ BootstrapCredentials, _ ...LifecycleOption) *Lifecycle { return &Lifecycle{} }

// Bind is a stub; never reached in practice on unsupported platforms because
// cell.Init returns ErrCellPlatformUnsupported earlier.
func (l *Lifecycle) Bind(_ BootstrapDeps, _ *slog.Logger) {}

// Hook returns a contributor whose OnStart fails fast with the same
// ErrCellPlatformUnsupported errcode, so any caller bypassing the cell.Init
// platform check still observes a consistent error code.
func (l *Lifecycle) Hook() cell.LifecycleHook {
	return cell.LifecycleHook{
		Name:    "accesscore.initial-admin-bootstrap",
		OnStart: func(_ context.Context) error { return errUnsupportedPlatform },
		OnStop:  func(_ context.Context) error { return nil },
	}
}
