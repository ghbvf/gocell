package bootstrap

import (
	"fmt"
	"sync/atomic"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
)

// authProvider is the optional cell-level interface that exposes an
// IntentTokenVerifier for runtime authentication. PolicyJWTFromAssembly
// scans the assembly for cells implementing this interface during phase4.
type authProvider interface {
	TokenVerifier() auth.IntentTokenVerifier
}

// PolicyJWTFromAssembly returns a cell.Policy that resolves its verifier
// from an authProvider cell in the assembly during phase4 (Policy.Validate).
// Use it when the verifier is owned by a cell (typical for accesscore-style
// designs where the auth cell publishes its own verifier).
//
//	bootstrap.WithListener(cell.PrimaryListener, ":8080", bootstrap.PolicyJWTFromAssembly(asm))
//
// Fail-fast:
//   - a nil assembly panics with "bootstrap: PolicyJWTFromAssembly assembly
//     must not be nil".
//   - the assembly passed here MUST be the same instance passed to
//     WithAssembly. Bootstrap.phase0 compares identities and returns an error
//     if they differ — the composition root single-assembly invariant cannot
//     be silently bypassed by handing PolicyJWTFromAssembly a sibling
//     assembly. Round-3 finding #10 hardening.
//   - if zero or multiple authProvider cells exist at phase4 the validation
//     step returns an error and Bootstrap.Run exits before the listener binds.
func PolicyJWTFromAssembly(asm *assembly.CoreAssembly) cell.Policy {
	if asm == nil {
		panic("bootstrap: PolicyJWTFromAssembly assembly must not be nil")
	}
	holder := &atomic.Pointer[auth.IntentTokenVerifier]{}
	marker := &jwtFromAssemblyMarker{
		asm: asm,
		getter: func() auth.IntentTokenVerifier {
			vp := holder.Load()
			if vp == nil {
				return nil
			}
			return *vp
		},
	}
	return cell.Policy{
		Name:      "jwt",
		Extension: marker,
		Validate: func() error {
			v, err := discoverAuthVerifierFromAssembly(asm)
			if err != nil {
				return err
			}
			holder.Store(&v)
			return nil
		},
	}
}

// jwtFromAssemblyMarker carries the assembly reference + lazy verifier
// getter so Bootstrap.phase0 can verify the assembly identity matches
// WithAssembly (round-3 finding #10) and phase5 can extract the verifier
// after Validate has resolved it. Implements jwtVerifierGetter for the
// existing single-source-of-truth extraction path.
type jwtFromAssemblyMarker struct {
	asm    *assembly.CoreAssembly
	getter func() auth.IntentTokenVerifier
}

func (m *jwtFromAssemblyMarker) verifier() auth.IntentTokenVerifier { return m.getter() }

// discoverAuthVerifierFromAssembly walks the assembly's cells in deterministic
// order and returns the unique IntentTokenVerifier exposed by an authProvider
// cell. Zero providers, multiple providers, and nil verifiers all return an
// error so the failure surfaces during Bootstrap.Run rather than the first
// request.
func discoverAuthVerifierFromAssembly(asm *assembly.CoreAssembly) (auth.IntentTokenVerifier, error) {
	var (
		found   auth.IntentTokenVerifier
		foundID string
	)
	for _, id := range asm.CellIDs() {
		ap, ok := asm.Cell(id).(authProvider)
		if !ok {
			continue
		}
		v := ap.TokenVerifier()
		if v == nil {
			return nil, fmt.Errorf(
				"bootstrap: cell %q implements authProvider but TokenVerifier() returned nil",
				id)
		}
		if found != nil {
			return nil, fmt.Errorf(
				"bootstrap: multiple authProvider cells discovered: %q and %q; "+
					"keep only one or supply the verifier explicitly via PolicyJWT(verifier)",
				foundID, id)
		}
		found = v
		foundID = id
	}
	if found == nil {
		return nil, fmt.Errorf(
			"bootstrap: PolicyJWTFromAssembly found no authProvider cell in the assembly; " +
				"register a cell whose TokenVerifier() returns a non-nil IntentTokenVerifier, " +
				"or wire the verifier explicitly via PolicyJWT(verifier)")
	}
	return found, nil
}
