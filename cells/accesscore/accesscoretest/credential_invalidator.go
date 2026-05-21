package accesscoretest

import (
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
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

// WithInvalidatorUsers injects a UserRepository into the Invalidator. This
// option is required: NewCredentialInvalidator fails the test immediately if
// it is not provided.
//
// Usage:
//
//	inv := accesscoretest.NewCredentialInvalidator(t,
//	    accesscoretest.WithInvalidatorUsers(fixture.UserRepository()),
//	)
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
// suitable for embedding in test-built services.
//
// WithInvalidatorUsers is required: omitting it fails the test immediately to
// prevent accidental use of an isolated store that is not paired with any other
// fixture.
//
// Default deps:
//   - sessions: testutil.RealSessionRepo(t)
//   - refreshStore: testutil.RealRefreshStore(t)
func NewCredentialInvalidator(t *testing.T, opts ...CredentialInvalidatorOption) *credentialinvalidate.Invalidator {
	t.Helper()
	cfg := &invalidatorConfig{}
	for _, o := range opts {
		o(cfg)
	}

	if cfg.users == nil {
		t.Fatalf("NewCredentialInvalidator: WithInvalidatorUsers is required; " +
			"pass WithInvalidatorUsers(fixture.UserRepository()) to share state with other services")
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
