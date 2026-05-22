package accesscoretest

import (
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
)

// CredentialInvalidatorOption configures NewCredentialInvalidator.
//
// The only public option is WithInvalidatorFixture. Round-2 review of PR
// #845 (worktree 650) collapsed the previous WithInvalidatorUsers /
// WithInvalidatorSessions / WithInvalidatorRefresh trio because they let
// callers wire independent stores into the invalidator while a real
// sessionlogin.Service held its own state — the exact mis-pairing failure
// mode PR #595 was supposed to prevent. The fixture is now the sole source
// of the (UserRepository, session.Store, refresh.Store) triple; any other
// wiring path is unexpressible in the type system.
type CredentialInvalidatorOption func(*invalidatorConfig)

type invalidatorConfig struct {
	fixture *AccessFixture
}

// WithInvalidatorFixture is the required option for NewCredentialInvalidator.
// Passing nil is a no-op (the option function is idempotent); the final nil
// check happens inside NewCredentialInvalidator and fails the test.
//
// Usage:
//
//	fix := accesscoretest.NewAccessFixture(t, clock.Real())
//	inv := accesscoretest.NewCredentialInvalidator(t,
//	    accesscoretest.WithInvalidatorFixture(fix),
//	)
func WithInvalidatorFixture(f *AccessFixture) CredentialInvalidatorOption {
	return func(c *invalidatorConfig) {
		if f != nil {
			c.fixture = f
		}
	}
}

// NewCredentialInvalidator constructs a real *credentialinvalidate.Invalidator
// wired to the user/session/refresh stores held by the supplied
// AccessFixture. The fixture is required: omitting WithInvalidatorFixture
// fails the test immediately so the invalidator can never silently run
// against an isolated store.
func NewCredentialInvalidator(t *testing.T, opts ...CredentialInvalidatorOption) *credentialinvalidate.Invalidator {
	t.Helper()
	cfg := &invalidatorConfig{}
	for _, o := range opts {
		o(cfg)
	}
	if cfg.fixture == nil {
		t.Fatalf("NewCredentialInvalidator: WithInvalidatorFixture is required; " +
			"pass an AccessFixture so user/session/refresh state stays paired")
	}

	inv, err := credentialinvalidate.New(
		cfg.fixture.bundle.UserRepository(),
		cfg.fixture.sessionStore,
		cfg.fixture.refreshStore,
	)
	if err != nil {
		t.Fatalf("NewCredentialInvalidator: %v", err)
	}
	return inv
}
