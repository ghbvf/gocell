// Package local_var_key_red is a testdata fixture for
// OPS-CONTRACT-STRING-FUNNEL-01 blind spot #2: a string(localVar) map key whose
// identifier resolves (via info.Uses) to a *types.Var — not a *types.Const —
// must be flagged. Uses a local ReadyProbeName mirror type.
package local_var_key_red

import "context"

// ReadyProbeName mirrors kernel/healthz.ReadyProbeName.
type ReadyProbeName string

// ProbeReady is a legitimate const; the violation is the runtime variable key.
const ProbeReady ReadyProbeName = "ok_ready"

// Res implements a Checkers()-shaped method.
type Res struct{}

// Checkers builds the map key from a local variable, which the funnel must
// reject because info.Uses resolves the ident to a *types.Var.
func (Res) Checkers() map[string]func(context.Context) error {
	dynamic := ProbeReady
	return map[string]func(context.Context) error{
		string(dynamic): func(context.Context) error { return nil },
	}
}
