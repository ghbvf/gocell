package bootstrap

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/assembly"
)

// PolicyJWTFromAssembly returns a bootstrap.Option that discovers an
// IntentTokenVerifier from the assembly's cells and wires it into the primary
// listener's JWT auth middleware (same as WithAuthDiscovery, but requires an
// explicit assembly reference so the dependency is visible at composition root).
//
// Discovery runs during phase4 (discoverAuthVerifier): the assembly is scanned
// for cells that implement the authProvider interface
// (TokenVerifier() IntentTokenVerifier). Exactly one provider must exist; zero
// or multiple providers cause Bootstrap.Run to return an error — fail-closed.
//
// Equivalent migration:
//
//	Old: WithAuthDiscovery()
//	New: PolicyJWTFromAssembly(asm)
//
// The assembly passed here must be the same instance as the one passed to
// WithAssembly. Passing a different instance is a programming error and will
// cause Bootstrap.Run to fail at phase0.
//
// ref: go-kratos/kratos transport/http/server.go — middleware wired at server level.
func PolicyJWTFromAssembly(asm *assembly.CoreAssembly) Option {
	if asm == nil {
		panic("bootstrap: PolicyJWTFromAssembly assembly must not be nil")
	}
	return func(b *Bootstrap) {
		// Verify the asm matches what was set via WithAssembly, if already set.
		// Mismatch is a programming error at composition root.
		if b.assembly != nil && b.assembly != asm {
			b.policyJWTFromAssemblyMismatch = fmt.Errorf(
				"bootstrap: PolicyJWTFromAssembly(asm) received a different assembly than WithAssembly(asm); " +
					"both must receive the same *assembly.CoreAssembly instance")
			return
		}
		b.authDiscovery = true
	}
}
