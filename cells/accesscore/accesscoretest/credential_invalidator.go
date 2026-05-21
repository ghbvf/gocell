package accesscoretest

import (
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// CredentialInvalidatorOption configures NewCredentialInvalidator.
type CredentialInvalidatorOption func(*invalidatorConfig)

type invalidatorConfig struct {
	users        ports.UserRepository
	sessions     session.Store
	refreshStore refresh.Store
}

// WithInvalidatorUsers injects a custom UserRepository into the Invalidator.
// When not provided, a fresh fixture UserRepo() is used — which is a separate
// store from any AccessFixture the caller may hold; prefer passing
// fixture.UserRepo() explicitly when sharing state with other services.
func WithInvalidatorUsers(r ports.UserRepository) CredentialInvalidatorOption {
	return func(c *invalidatorConfig) { c.users = r }
}

// WithInvalidatorSessions injects a custom session.Store into the Invalidator.
func WithInvalidatorSessions(s session.Store) CredentialInvalidatorOption {
	return func(c *invalidatorConfig) { c.sessions = s }
}

// WithInvalidatorRefresh injects a custom refresh.Store into the Invalidator.
func WithInvalidatorRefresh(r refresh.Store) CredentialInvalidatorOption {
	return func(c *invalidatorConfig) { c.refreshStore = r }
}

// NewCredentialInvalidator constructs a real *credentialinvalidate.Invalidator
// suitable for embedding in test-built services. Default deps are:
//
//   - users: fresh AccessFixture(clock.Real()).UserRepo() — NOTE: this is a
//     standalone store NOT paired with any other fixture; callers wiring
//     identitymanage should use BuildIdentityManageService or pass
//     WithInvalidatorUsers(fixture.UserRepo()) explicitly to share state.
//   - sessions: session.NewMemStore(sessiontest.Protocol(), clock.Real())
//   - refreshStore: testutil.RealRefreshStore(t)
func NewCredentialInvalidator(t *testing.T, opts ...CredentialInvalidatorOption) *credentialinvalidate.Invalidator {
	t.Helper()
	cfg := &invalidatorConfig{}
	for _, o := range opts {
		o(cfg)
	}

	if cfg.users == nil {
		cfg.users = NewAccessFixture(t, clock.Real()).UserRepo()
	}
	if cfg.sessions == nil {
		cfg.sessions = testutil.RealSessionRepo(t)
	}
	if cfg.refreshStore == nil {
		cfg.refreshStore = testutil.RealRefreshStore(t)
	}

	inv, err := credentialinvalidate.New(cfg.users, cfg.sessions, cfg.refreshStore)
	if err != nil {
		t.Fatalf("NewCredentialInvalidator: %v", err)
	}
	return inv
}
