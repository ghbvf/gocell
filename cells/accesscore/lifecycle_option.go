package accesscore

import "github.com/ghbvf/gocell/cells/accesscore/internal/initialadmin"

// LifecycleOption is a re-export of initialadmin.LifecycleOption so composition
// roots (cmd/corebundle, examples/) can pass typed bootstrap options to
// WithInitialAdminBootstrap without importing an internal package.
//
// ref: uber-go/fx — expensive production dependencies exposed as options
// so tests can swap in fast stubs without diverging from the wire graph.
type LifecycleOption = initialadmin.LifecycleOption

// WithBootstrapPasswordHasher is a re-export of initialadmin.WithPasswordHasher.
// Composition roots use this to inject a low-cost bcrypt hasher in tests.
func WithBootstrapPasswordHasher(h PasswordHasher) LifecycleOption {
	return initialadmin.WithPasswordHasher(h)
}
