//go:build archtest

// command_async_emit_caller_test.go — locks the UPSTREAM (producer-side)
// caller-allowlist of the async command emit funnel: any kernel/outbox.Emit or
// kernel/outbox.NewEntry callsite whose topic const-evaluates to a `command.*`
// namespace string must live inside runtime/command (i.e. flow through the
// sanctioned EmitAsync producer that stamps the idempotency identity slot).
//
//   - INVARIANT: COMMAND-ASYNC-EMIT-FUNNEL-01
//
// ADR ref: docs/architecture/202606040550-1044-adr-command-bus-dispatch-funnel.md
package archtest

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// commandTopicNamespacePrefix is the routing-topic namespace reserved for async
// commands. An Emit / NewEntry whose topic const-evaluates to this prefix is
// constructing a command entry and MUST go through runtime/command.EmitAsync (the
// sanctioned producer that stamps subject + commandID — the idempotency identity
// the relay reads via ClaimKeyFromEntry to dedup the dispatch, #1698).
const commandTopicNamespacePrefix = "command."

// commandEmitFunnelPkgPath is the SOLE sanctioned caller package for constructing
// a command-namespace entry: runtime/command (EmitAsync's body calls kout.NewEntry
// there).
const commandEmitFunnelPkgPath = PlatformFrameworkModulePath + "/runtime/command"

// TestCommandAsyncEmitFunnel01 asserts that every production kout.Emit / kout.NewEntry
// callsite whose topic argument const-evaluates to a `command.*` string is declared
// inside runtime/command. Any other package constructing a command-namespace entry
// directly bypasses EmitAsync — and therefore omits the idempotency identity slot
// (AggregateID=subject + CommandIDMetadataKey) that the relay's Claimer wrap needs to
// deduplicate the in-process dispatch (#1698). Such an entry would dead-letter at the
// relay (ClaimKeyFromEntry → ok=false → fail-closed), but catching it statically at
// the producer is the upstream half of the closure.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream: the relay fail-closes a command entry that lacks an idempotency
//     identity (command.ClaimKeyFromEntry → ok=false → kout.NewPermanentError →
//     MarkDead). That enforcement lives in runtime/outbox/relay.go::dispatchCommand,
//     NOT in this archtest — it is the runtime backstop, exercised by
//     runtime/outbox TestDispatchCommand_MissingIdentity_Permanent.
//   - Upstream: MEDIUM — this caller-allowlist archtest, and it is a GO-LANGUAGE
//     CEILING, not a deferred TODO. Producer (cells/examples) and relay
//     (runtime/outbox) are async-decoupled across the durable outbox, so "every
//     async command carries an idempotency identity" cannot be made compile-time
//     Hard end-to-end: Go cannot express "only runtime/command may construct a
//     command-namespace entry". The same permanent ceiling documented for
//     OUTBOX-RECONSTRUCTION-CALLER-01 / SPAN-SETATTR-HOLDER-SEAL (#851) /
//     HEALTHZ-HOLDER-SEAL (#893) / #1282. No fake Hard-upgrade issue is opened.
//
// # Detection is type-aware (not string scanning)
//
//   - Callee identity: ResolvePackageRef resolves call.Fun to kernel/outbox.Emit /
//     kernel/outbox.NewEntry, alias- and dot-import-proof. The generic Emit[T] is
//     unwrapped (IndexExpr / IndexListExpr) before resolution, mirroring
//     CheckEmitDeclCover.
//   - Topic value: EvaluateConstString const-folds the topic arg (BasicLit / Ident
//     / SelectorExpr / BinaryExpr concat) via go/types. Only a const string with
//     the `command.` prefix trips the rule.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//  1. Non-const topic (a runtime-assembled `"command." + x` where x is non-const):
//     EvaluateConstString returns ok=false and the callsite is skipped. This is the
//     #1282/#1508-family data-flow ceiling — a non-const command topic cannot be
//     statically attributed. Documented, not enforced; the relay fail-closes such an
//     entry anyway if it lacks identity.
//  2. generated/contracts/command/** packages construct command entries via the
//     generated DispatchAsync trust-boundary, NOT via Emit/NewEntry, and are excluded
//     by Production() scope regardless. Documented.
//  3. Build-tag-gated production files under a non-default tag are not scanned by the
//     default-tags Production scan (same posture as COMMAND-ASYNC-DISPATCH-CALLER-01).
//     Documented.
func TestCommandAsyncEmitFunnel01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var observedCommandEmit bool
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		inFunnel := p.Pkg.Path() == commandEmitFunnelPkgPath
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				topic, isCommand := commandTopicOfEmitOrNewEntry(p, call)
				if !isCommand {
					return
				}
				observedCommandEmit = true
				if inFunnel {
					return // runtime/command is the sanctioned producer
				}
				pos := p.Fset.Position(call.Pos())
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"COMMAND-ASYNC-EMIT-FUNNEL-01: %s constructs a command-namespace "+
							"entry (topic %q) via kout.Emit/NewEntry directly. Async command "+
							"entries MUST be emitted through runtime/command.EmitAsync, which "+
							"stamps the idempotency identity (subject + commandID) the relay's "+
							"Claimer wrap reads to dedup the dispatch (#1698). A bare Emit/NewEntry "+
							"omits that slot and the relay dead-letters the entry.",
						rel, topic),
				})
			})
		}
		return d
	})

	// Anti-vacuity: runtime/command.EmitAsync must contain a verifiable
	// command-namespace NewEntry construction, else the funnel guards nothing.
	// EmitAsync passes a NON-const dispatchID param to NewEntry, so the const-topic
	// production scan above cannot observe it — assert the funnel's existence
	// structurally instead (EmitAsync declared in runtime/command calling kout.NewEntry).
	if !observedCommandEmitFunnelPresent(t) {
		diags = append(diags, Diagnostic{
			Message: "COMMAND-ASYNC-EMIT-FUNNEL-01 anti-vacuity: runtime/command.EmitAsync " +
				"was not found calling kout.NewEntry. Either EmitAsync was removed/renamed or " +
				"the scanner regressed — the producer funnel guards nothing without it.",
		})
	}
	_ = observedCommandEmit // const-topic command emits are not expected in production today (vacuous-green)

	Report(t, "COMMAND-ASYNC-EMIT-FUNNEL-01", diags)
}

// commandTopicOfEmitOrNewEntry reports the const topic of a kout.Emit or
// kout.NewEntry call when it const-evaluates to a `command.*` string. ok=false for
// any other callee, a non-const topic, or a non-command topic.
//
// Emit(ctx, clk, emitter, topic, payload) → topic at Args[3].
// NewEntry(clk, ctx, eventType, payload, ...opts) → eventType at Args[2].
func commandTopicOfEmitOrNewEntry(p *Pass, call *ast.CallExpr) (topic string, ok bool) {
	fun := call.Fun
	if idx, isIdx := fun.(*ast.IndexExpr); isIdx {
		fun = idx.X
	} else if idxl, isIdxl := fun.(*ast.IndexListExpr); isIdxl {
		fun = idxl.X
	}
	pkgPath, name, resolved := ResolvePackageRef(p.TypesInfo, fun)
	if !resolved || pkgPath != outboxImportPath {
		return "", false
	}
	var topicExpr ast.Expr
	switch {
	case name == "Emit" && len(call.Args) >= 4:
		topicExpr = call.Args[3]
	case name == "NewEntry" && len(call.Args) >= 3:
		topicExpr = call.Args[2]
	default:
		return "", false
	}
	val, isConst := EvaluateConstString(p.TypesInfo, topicExpr)
	if !isConst || !strings.HasPrefix(val, commandTopicNamespacePrefix) {
		return "", false
	}
	return val, true
}

// observedCommandEmitFunnelPresent verifies runtime/command declares an EmitAsync
// func whose body calls kout.NewEntry — the structural anti-vacuity anchor (the
// const-topic production scan cannot see EmitAsync's non-const dispatchID arg).
func observedCommandEmitFunnelPresent(t *testing.T) bool {
	t.Helper()
	var found bool
	_ = Run(t, Typed(TypedOpts{}, []string{"./framework/runtime/command/..."}), func(p *Pass) []Diagnostic {
		if !p.Typed() || p.Pkg.Path() != commandEmitFunnelPkgPath {
			return nil
		}
		for _, file := range p.Files {
			EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
				if fd.Recv != nil || fd.Name == nil || fd.Name.Name != "EmitAsync" {
					return
				}
				EachInSubtree[ast.CallExpr](fd, func(call *ast.CallExpr) {
					fun := call.Fun
					if idx, isIdx := fun.(*ast.IndexExpr); isIdx {
						fun = idx.X
					}
					pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, fun)
					if ok && pkgPath == outboxImportPath && name == "NewEntry" {
						found = true
					}
				})
			})
		}
		return nil
	})
	return found
}

// TestCommandAsyncEmitFunnel01_RedFixture verifies the scanner fires against a
// hand-written package that constructs a command-namespace entry via a bare
// kout.Emit / kout.NewEntry (bypassing EmitAsync).
//
// The fixture must produce ≥ 2 diagnostics (one Emit + one NewEntry). total==0 means
// the scanner is fail-open.
func TestCommandAsyncEmitFunnel01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/commandasyncemitfixture"}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			// The fixture package path is not runtime/command, so any command-topic
			// Emit/NewEntry there is a violation.
			for _, file := range p.Files {
				EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
					if _, isCommand := commandTopicOfEmitOrNewEntry(p, call); isCommand {
						found++
					}
				})
			}
			return nil
		})

	assert.GreaterOrEqual(t, found, 2,
		"COMMAND-ASYNC-EMIT-FUNNEL-01 RED fixture self-check FAILED: expected ≥ 2 "+
			"violations from commandasyncemitfixture (bare kout.Emit + bare kout.NewEntry "+
			"with a command.* topic); got %d. found==0 means the scanner is fail-open — "+
			"check ResolvePackageRef / EvaluateConstString resolve under the archtest_fixture tag.",
		found)
}
