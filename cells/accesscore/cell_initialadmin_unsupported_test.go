//go:build !unix && !windows

package accesscore

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/ghbvf/gocell/kernel/clock"
)

// unsupportedDeps mirrors the unix testDeps helper without the unix build tag.
func unsupportedDeps() cell.Dependencies {
	return cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
		Clock:          clock.Real(),
	}
}

// TestAccessCoreInit_InitialAdminUnsupportedPlatform_FailFast verifies that
// when WithInitialAdminBootstrap is configured on a build without unix or
// windows file-permission primitives, cell.Init fails fast in phase2 with
// ErrCellPlatformUnsupported — instead of deferring the failure to the
// LifecycleHook OnStart in phase3b.
func TestAccessCoreInit_InitialAdminUnsupportedPlatform_FailFast(t *testing.T) {
	ac := NewAccessCore(
		WithUserRepository(mem.NewUserRepository()),
		WithSessionRepository(mem.NewSessionRepository(clock.Real())),
		WithRoleRepository(mem.NewRoleRepository()),
		WithOutboxDeps(noopPublisher{}, nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithMetricsProvider(metrics.NopProvider{}),
		WithInitialAdminBootstrap(),
	)

	err := ac.Init(context.Background(), unsupportedDeps())
	require.Error(t, err,
		"Init must fail fast when WithInitialAdminBootstrap is active on an unsupported platform")

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "error must be *errcode.Error; got %T", err)
	assert.Equal(t, errcode.ErrCellPlatformUnsupported, ec.Code,
		"failure must carry ErrCellPlatformUnsupported so operators can route it separately from ErrCellInvalidConfig")
}

// TestAccessCoreInit_InitialAdminNotConfigured_NoCheck verifies that
// AccessCore Init does not invoke the platform check when
// WithInitialAdminBootstrap is absent — non-bootstrap deployments must run
// fine on any platform.
func TestAccessCoreInit_InitialAdminNotConfigured_NoCheck(t *testing.T) {
	ac := NewAccessCore(
		WithUserRepository(mem.NewUserRepository()),
		WithSessionRepository(mem.NewSessionRepository(clock.Real())),
		WithRoleRepository(mem.NewRoleRepository()),
		WithOutboxDeps(noopPublisher{}, nil),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithRefreshStore(newTestRefreshStore()),
		WithMetricsProvider(metrics.NopProvider{}),
	)

	require.NoError(t, ac.Init(context.Background(), unsupportedDeps()),
		"Init without WithInitialAdminBootstrap must succeed on any platform")
	assert.Empty(t, ac.LifecycleHooks(),
		"LifecycleHooks must be empty when no bootstrap is configured")
}
