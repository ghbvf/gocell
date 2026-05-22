package accesscoretest

import (
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
)

// CredentialInvalidator is the public opaque handle returned by
// NewCredentialInvalidator. It wraps the unexported
// *cells/accesscore/internal/credentialinvalidate.Invalidator so external
// _test packages can hold and pass the value without importing the
// internal/ package — which Go's internal-package rule forbids.
//
// The struct intentionally has no exported fields and no exported methods:
// it is a sealed handle, not an API surface. The single legal touchpoint of
// the underlying internal type lives in this package (where the internal
// import is permitted), keeping the testutil → internal coupling
// unidirectional and unbypassable from outside cells/accesscore/.
//
// Hard funnel form (sibling of Hard 范本 §"single sanctioned holder"):
//   - upstream  Hard — Go's internal-package rule makes *credentialinvalidate.Invalidator
//     unnameable in any package not rooted at cells/accesscore/. External
//     callers therefore cannot bypass this wrapper by declaring the raw type.
//   - downstream Hard — unwrap field is unexported (.inv), so no external
//     code can read or rewrap it; the only consumers are NewCredentialInvalidator
//     (constructor) and BuildIdentityManageService (which threads the
//     fixture-paired invalidator into identitymanage.NewService).
type CredentialInvalidator struct {
	inv *credentialinvalidate.Invalidator
}

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

// NewCredentialInvalidator constructs an opaque *CredentialInvalidator wired
// to the user/session/refresh stores held by the supplied AccessFixture. The
// fixture is required: omitting WithInvalidatorFixture fails the test
// immediately so the invalidator can never silently run against an isolated
// store.
//
// The returned *CredentialInvalidator is the only handle external test
// packages may hold. Pass it to WithIdentityInvalidator when building
// services; the internal unwrap to *credentialinvalidate.Invalidator happens
// inside this package.
func NewCredentialInvalidator(t *testing.T, opts ...CredentialInvalidatorOption) *CredentialInvalidator {
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
	return &CredentialInvalidator{inv: inv}
}
