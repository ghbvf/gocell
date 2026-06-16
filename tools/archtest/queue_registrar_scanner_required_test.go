//go:build archtest

// INVARIANT: QUEUE-REGISTRAR-SCANNER-REQUIRED-01
//
// # QUEUE-REGISTRAR-SCANNER-REQUIRED-01 — QueueRegistrar param frozen to QueueWithScanner (Hard)
//
// ## Rule
//
// The `kernel/command.QueueRegistrar` interface MUST have exactly the method:
//
//	RegisterCommandQueue(QueueWithScanner)
//
// where `QueueWithScanner` is the composite interface embedding BOTH
// `command.Queue` AND `command.ActiveScanner`. The command bus needs a single
// handle that is simultaneously the enqueue/dequeue Queue AND the ScanActive
// scanner (the dequeue path, the Sweeper, and the internal ops view all call
// ScanActive). Narrowing the registrar parameter from the wide `Queue` to
// `QueueWithScanner` makes "register a queue that is not also an ActiveScanner"
// a COMPILE error at the call site, replacing the earlier cell-local runtime
// fail-fast (devicecell's `commandQueueTypeMismatch` flag + `requireCommandQueue`,
// #1694 F11 / #2009 F11) which only every implementer-could-forget runtime
// assertion (AI-robust Medium). This is the Medium→Hard upgrade (#2011).
//
// ## AI-robust rating: Hard (reflect interface method signature freeze)
//
// Upstream Hard: the narrowed parameter type makes a non-scanner queue
// unrepresentable at the RegisterCommandQueue call site (type-system gate, same
// family as USERREPO-METHOD-SET-FROZEN-01 narrow-a-param-to-forbid-misuse and
// MODULE-PROVIDE-NO-VALUE-HANDOFF-01 reflect signature freeze).
//
// Downstream Hard (this test): reflect.TypeOf pins (1) RegisterCommandQueue's
// In(0) to exactly `QueueWithScanner`, and (2) that `QueueWithScanner` itself
// still .Implements() both Queue and ActiveScanner. Re-widening the parameter
// back to `Queue`, or gutting `QueueWithScanner` down to a Queue-only alias
// while keeping the name, flips a reflect shape and fails immediately. There is
// no string anchor; the rule is form-locked.
//
// ## Blind spots and reverse self-checks
//
//  1. **Coordinated re-widening**: widening QueueRegistrar.RegisterCommandQueue
//     back to `Queue` alone would, on its own, break devicecell's
//     `var _ QueueRegistrar` compile-check (its method took QueueWithScanner).
//     A contributor who widens BOTH the interface and devicecell in lockstep
//     would compile but silently re-open the hole — TestQueueRegistrarScannerRequired01
//     catches exactly that by pinning In(0)==QueueWithScanner.
//  2. **Name-keep, gut the composite**: renaming a Queue-only interface to
//     `QueueWithScanner` would keep In(0)'s name but drop ScanActive. The
//     `.Implements(ActiveScanner)` assertion + the anti-vacuity reverse check
//     (a Queue-only interface must NOT satisfy QueueWithScanner) detect this.
//  3. **Wrong package path**: the reflect handles are taken from
//     `command.QueueRegistrar` / `command.QueueWithScanner` directly (canonical
//     import path), so a sibling type cannot be substituted vacuously.
//
// ## Symbol inventory (lives here, not in ai-robust.md per the charter)
//
//   - Frozen interface: `github.com/ghbvf/gocell/framework/kernel/command.QueueRegistrar`
//   - Frozen method: `RegisterCommandQueue`
//   - Required In[0]: `kernel/command.QueueWithScanner`
//   - Composite: `kernel/command.QueueWithScanner` embeds `command.Queue` + `command.ActiveScanner`
//
// See also: `framework/kernel/command/registrar.go` godoc §QUEUE-REGISTRAR-SCANNER-REQUIRED-01.
package archtest

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kcommand "github.com/ghbvf/gocell/framework/kernel/command"
)

const ruleQueueRegistrarScannerRequired01 = "QUEUE-REGISTRAR-SCANNER-REQUIRED-01"

// queueOnlyProbe has Queue's method set but NOT ActiveScanner's — it is the
// reflect-world successor of the deleted devicecell `queueWithoutActiveScanner`
// runtime fixture. It must NOT satisfy QueueWithScanner (anti-vacuity).
type queueOnlyProbe interface {
	kcommand.Queue
}

// queueWithScannerProbe carries both method sets and is the positive control.
type queueWithScannerProbe interface {
	kcommand.Queue
	kcommand.ActiveScanner
}

// TestQueueRegistrarScannerRequired01 reflects over command.QueueRegistrar and
// asserts RegisterCommandQueue takes exactly one argument of type
// command.QueueWithScanner, and that QueueWithScanner transitively requires
// both Queue and ActiveScanner. Any re-widening or gutting flips a reflect
// shape and fails.
func TestQueueRegistrarScannerRequired01(t *testing.T) {
	t.Parallel()

	regIface := reflect.TypeOf((*kcommand.QueueRegistrar)(nil)).Elem()
	require.Equal(t, reflect.Interface, regIface.Kind(),
		"%s: command.QueueRegistrar must be an interface; got %s",
		ruleQueueRegistrarScannerRequired01, regIface.Kind())

	m, ok := regIface.MethodByName("RegisterCommandQueue")
	require.True(t, ok,
		"%s: command.QueueRegistrar must have a RegisterCommandQueue method",
		ruleQueueRegistrarScannerRequired01)

	mt := m.Type
	// Interface-method reflect type carries no receiver, so In(0) is the first
	// real parameter and there are no return values.
	require.Equal(t, 1, mt.NumIn(),
		"%s: RegisterCommandQueue must take exactly 1 parameter; got %d",
		ruleQueueRegistrarScannerRequired01, mt.NumIn())
	require.Equal(t, 0, mt.NumOut(),
		"%s: RegisterCommandQueue must return nothing; got %d return values",
		ruleQueueRegistrarScannerRequired01, mt.NumOut())

	qws := reflect.TypeOf((*kcommand.QueueWithScanner)(nil)).Elem()
	assert.Equal(t, qws, mt.In(0),
		"%s: RegisterCommandQueue In[0] must be command.QueueWithScanner; got %s. "+
			"Re-widening to the bare command.Queue re-opens the non-scanner wiring hole "+
			"that #2011 closed at compile time.",
		ruleQueueRegistrarScannerRequired01, mt.In(0))

	// QueueWithScanner must itself be an interface that embeds BOTH Queue and
	// ActiveScanner — guards against gutting it to a Queue-only alias.
	require.Equal(t, reflect.Interface, qws.Kind(),
		"%s: command.QueueWithScanner must be an interface; got %s",
		ruleQueueRegistrarScannerRequired01, qws.Kind())
	queueIface := reflect.TypeOf((*kcommand.Queue)(nil)).Elem()
	scannerIface := reflect.TypeOf((*kcommand.ActiveScanner)(nil)).Elem()
	assert.True(t, qws.Implements(queueIface),
		"%s: command.QueueWithScanner must embed command.Queue",
		ruleQueueRegistrarScannerRequired01)
	assert.True(t, qws.Implements(scannerIface),
		"%s: command.QueueWithScanner must embed command.ActiveScanner (ScanActive/GetCommand)",
		ruleQueueRegistrarScannerRequired01)
}

// TestQueueRegistrarScannerRequired01_AntiVacuity proves the freeze is
// non-vacuous: a Queue-only interface must NOT satisfy QueueWithScanner (so the
// In(0) type genuinely demands the scanner methods), while a both-embedding
// interface must. If QueueWithScanner were ever gutted to require only Queue's
// methods, queueOnlyProbe would start satisfying it and this test fails.
func TestQueueRegistrarScannerRequired01_AntiVacuity(t *testing.T) {
	t.Parallel()

	qws := reflect.TypeOf((*kcommand.QueueWithScanner)(nil)).Elem()

	queueOnly := reflect.TypeOf((*queueOnlyProbe)(nil)).Elem()
	assert.False(t, queueOnly.Implements(qws),
		"%s anti-vacuity: a Queue-only interface must NOT satisfy QueueWithScanner; "+
			"if it does, the composite no longer requires ActiveScanner",
		ruleQueueRegistrarScannerRequired01)

	both := reflect.TypeOf((*queueWithScannerProbe)(nil)).Elem()
	assert.True(t, both.Implements(qws),
		"%s positive control: a Queue+ActiveScanner interface must satisfy QueueWithScanner",
		ruleQueueRegistrarScannerRequired01)
}
