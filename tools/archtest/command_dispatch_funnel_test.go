// command_dispatch_funnel_test.go — locks the COMMAND dispatch funnel as a
// double-lock: downstream (caller allowlist) + upstream (codegen sole-emitter).
//
//   - INVARIANT: COMMAND-DISPATCH-REGISTER-CALLER-01
//   - INVARIANT: COMMAND-GEN-FUNNEL-SOLE-EMITTER-01
//   - INVARIANT: COMMAND-ASYNC-DISPATCH-CALLER-01
//
// ADR ref: docs/architecture/202606040550-1044-adr-command-bus-dispatch-funnel.md
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// commandPkgPath is the canonical import path for the runtime/command package
// containing the *Registry type that both invariants guard.
const commandPkgPath = PlatformModulePath + "/runtime/command"

// commandGeneratedPrefix is the module-relative path prefix that is the SOLE
// sanctioned caller namespace for RegisterHandler and LookupHandler.
// Only packages under this prefix (plus the command package itself) may call
// those two methods.
const commandGeneratedPrefix = "generated/contracts/command/"

// commandRegistryTypeName is the Go type name of the guarded registry type.
const commandRegistryTypeName = "Registry"

// outboxPkgPath / relayTypeName / withCommandDispatchMethod identify the
// (*outbox.Relay).WithCommandDispatch sink that COMMAND-ASYNC-DISPATCH-CALLER-01
// locks to generated DispatchAsync values.
const (
	cmdRelayPkgPath           = PlatformModulePath + "/runtime/outbox"
	relayTypeName             = "Relay"
	withCommandDispatchMethod = "WithCommandDispatch"
	dispatchAsyncFuncName     = "DispatchAsync"
)

// isCommandSoleEmitterFuncName reports whether name is one of the generated
// command free-func names that, together with a *command.Registry parameter,
// mark the codegen sole-emitter trio: Register / Dispatch / DispatchAsync
// (#1667 added DispatchAsync). A hand-written look-alike declaring any of these
// alongside a Handler-style interface is a forbidden parallel funnel.
func isCommandSoleEmitterFuncName(name string) bool {
	return name == "Register" || name == "Dispatch" || name == dispatchAsyncFuncName
}

// ---------------------------------------------------------------------------
// INVARIANT: COMMAND-DISPATCH-REGISTER-CALLER-01
// ---------------------------------------------------------------------------

// TestCommandDispatchRegisterCaller01 asserts that every production callsite of
// (*runtime/command.Registry).RegisterHandler and
// (*runtime/command.Registry).LookupHandler is in the sanctioned caller
// namespace — packages under generated/contracts/command/** plus the
// runtime/command package itself (+ its own _test.go variants).
//
// # What this guards
//
// Batches A–D landed a command.Registry with RegisterHandler / LookupHandler
// methods and a REAL generated package
// (generated/contracts/command/devicecommand/enqueue/v1/command_gen.go)
// that calls both methods. The generated Register and Dispatch functions are
// the sole sanctioned callsites; a hand-written cell or example calling
// reg.RegisterHandler(...) directly bypasses the typed Handler interface and
// type-assert gate provided by the generated code, opening a route for
// type-unsafe or unintended handler wiring. This archtest pins both method
// callsites to the generated namespace so the funnel cannot be bypassed.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream: HARD by archtest caller-allowlist. The callee is resolved via
//     go/types (ResolveMethodCall), so import aliases, dot-imports, method-value
//     forms (`f := reg.RegisterHandler; f(...)`), and method-expression forms
//     (`(*command.Registry).RegisterHandler(reg, ...)`) are all resolved to the
//     same *types.Func symbol. Any callsite outside the allowlist fails CI. The
//     RED fixture (commandregistercallerfixture) exercises all four shapes so the
//     indirection coverage cannot silently regress to call-only detection.
//   - Upstream: MEDIUM, and this is a GO-LANGUAGE CEILING, not a deferred TODO.
//     Hard upstream would require RegisterHandler/LookupHandler to be unreachable
//     outside the sanctioned packages. Go package visibility cannot express
//     "only these packages may call an exported method on *Registry"; the ideal
//     Hard form would move the methods behind an internal/ wrap so only generated/
//     packages import the sealed internal type. That is a deliberate won't-do:
//     gh #1575 (won't-do tracker) — same permanent ceiling documented
//     for OUTBOX-RECONSTRUCTION-CALLER-01 / SPAN-SETATTR-HOLDER-SEAL (#851) /
//     HEALTHZ-HOLDER-SEAL (#893) / #1282. The downstream archtest is the
//     enforcement backstop; the ceiling is documented, not silently accepted.
//
// # Detection is REFERENCE-based, not call-based
//
// The scanner matches every SelectorExpr that go/types resolves to
// RegisterHandler or LookupHandler on *command.Registry — whether it is the
// callee of a call OR passed as a method/function value. This deliberately
// closes the "indirection through a method value" gap:
//   - `f := reg.RegisterHandler; f(id, h)` references the symbol at the
//     `reg.RegisterHandler` SelectorExpr and is therefore caught.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//  1. Dot-import bare-identifier form
//     (import . ".../runtime/command"; RegisterHandler(...)) references the
//     method as a promoted function, not a SelectorExpr, so it is not matched
//     by the SelectorExpr walker. Dot-importing runtime/command is absent from
//     the corpus and conspicuous; documented, not enforced.
//
//  2. Interface boxing: if a type alias or wrapper interface shadows
//     *command.Registry and exposes RegisterHandler/LookupHandler under a
//     different type identity, ResolveMethodCall resolves to the shadowing
//     type's method, which is NOT the guarded *command.Registry method.
//     Today no such wrapper exists; a future wrapper would be conspicuous in
//     code review. Documented.
//
//  3. //go:build-gated production files under a non-default build tag would be
//     missed by the default-tags Production scan. All callers today are default-
//     build; documented, not enforced.
//
// # Anti-vacuity / stale allowlist check
//
// Every non-empty allowlist entry must be observed hosting a live call; stale
// entries are surfaced as diagnostics. The generated command packages (which
// contain the real Register and Dispatch calls) are expected to appear. Phase 2
// scans the generated/contracts/command/** glob — NOT a single hardcoded package
// (#1580: the prior single-package form went vacuous the moment that one funnel
// was renamed; the glob auto-covers every present and future codegen command and
// only re-vacuates when ALL command funnels disappear). If codegen renames or
// removes every generated call, this check fails and prompts a reviewer to verify
// the funnel is still wired — preventing a dead entry from becoming a silent
// bypass slot.
func TestCommandDispatchRegisterCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// observed[rel] = true for each file where a guarded method reference is found.
	observed := map[string]bool{}

	// Phase 1: scan all PRODUCTION (hand-written, non-generated) packages for
	// unauthorized callers. Production() excludes generated/ by design, so no
	// generated call is seen here — that is correct; the rule bans direct calls
	// from non-generated code.
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
				if !isCommandRegistryMethod(p.TypesInfo, sel) {
					return
				}
				observed[rel] = true
				if !isCommandRegistryCaller(rel) {
					pos := p.Fset.Position(sel.Pos())
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"COMMAND-DISPATCH-REGISTER-CALLER-01: %s references "+
								"(*command.Registry).%s directly. Only packages under "+
								"%s and the runtime/command package itself may call "+
								"RegisterHandler or LookupHandler. Route command "+
								"registration and dispatch through the generated "+
								"Register / Dispatch funcs in generated/contracts/command/**. "+
								"If this IS a new sanctioned infra site, justify it and "+
								"update the archtest.",
							rel, sel.Sel.Name, commandGeneratedPrefix,
						),
					})
				}
			})
		}
		return d
	})

	// Phase 2: anti-vacuity check — scan ALL generated command packages (outside
	// Production scope) to confirm the sanctioned caller actually calls the guarded
	// methods. Production() excludes generated/ by design, so we use Typed() with
	// the generated/contracts/command/** glob (#1580: was a single hardcoded
	// devicecommand/enqueue/v1 package; generalized so a second codegen command
	// auto-joins coverage and removing only one funnel never silently re-vacuates
	// this check). At least one generated package must contain a guarded call;
	// none means the scanner regressed or every generated funnel was
	// renamed/removed, leaving the downstream caller-allowlist vacuously safe.
	const generatedPkgGlob = "./generated/contracts/command/..."
	var generatedCallFound bool
	_ = Run(t, Typed(TypedOpts{}, []string{generatedPkgGlob}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		for _, file := range p.Files {
			EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
				if isCommandRegistryMethod(p.TypesInfo, sel) {
					generatedCallFound = true
				}
			})
		}
		return nil
	})
	if !generatedCallFound {
		diags = append(diags, Diagnostic{
			Message: "COMMAND-DISPATCH-REGISTER-CALLER-01 anti-vacuity: NO package under " +
				generatedPkgGlob + " contains a call to RegisterHandler or " +
				"LookupHandler. Either the scanner regressed (check isCommandRegistryMethod) " +
				"or every generated command funnel was renamed/removed. The funnel is " +
				"vacuous — at least one sanctioned generated caller must be verifiably present.",
		})
	}

	Report(t, "COMMAND-DISPATCH-REGISTER-CALLER-01", diags)
}

// isCommandRegistryCaller reports whether a module-relative file path is in
// the sanctioned caller namespace for (*command.Registry).RegisterHandler and
// (*command.Registry).LookupHandler:
//
//   - Files under generated/contracts/command/** (the codegen output namespace).
//   - Files under runtime/command/ (the package that defines the Registry type;
//     its own implementation AND tests — runtime/command/*_test.go — are allowed
//     because that prefix matches both .go and _test.go).
//
// NOTE: there is deliberately NO blanket "_test.go anywhere" exemption. A test
// file under cells/ or examples/ that calls the raw RegisterHandler/LookupHandler
// directly (rather than the generated Register/Dispatch) is flagged — tests are
// expected to drive the funnel through the generated functions, same as
// production code. (The reverse RED fixtures live under tools/archtest/internal/**
// as build-tagged .go files loaded by a separate Fixture scan, not here.)
func isCommandRegistryCaller(rel string) bool {
	if strings.HasPrefix(rel, commandGeneratedPrefix) {
		return true
	}
	if strings.HasPrefix(rel, "runtime/command/") {
		return true
	}
	return false
}

// isCommandRegistryMethod reports whether sel is a reference (call or
// method-value) to (*command.Registry).RegisterHandler or
// (*command.Registry).LookupHandler, resolved alias-proof via go/types.
//
// We use ResolveMethodCall which resolves via types.Info.Selections — this
// handles pointer/value/promoted/alias receivers and method-value forms.
// Only methods on the exact *types.Named "Registry" in package
// "github.com/ghbvf/gocell/runtime/command" are matched; a same-named method
// on an unrelated type is not a false positive.
func isCommandRegistryMethod(info *types.Info, sel *ast.SelectorExpr) bool {
	if sel.Sel == nil {
		return false
	}
	name := sel.Sel.Name
	if name != "RegisterHandler" && name != "LookupHandler" {
		return false
	}
	fn, ok := ResolveMethodCall(info, sel)
	if !ok || fn == nil || fn.Pkg() == nil {
		return false
	}
	if fn.Pkg().Path() != commandPkgPath {
		return false
	}
	// Confirm the receiver base type is command.Registry, not some other type
	// in the package that happens to have the same method name.
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	recv := sig.Recv().Type()
	if ptr, ok := recv.(*types.Pointer); ok {
		recv = ptr.Elem()
	}
	named, ok := recv.(*types.Named)
	if !ok || named.Obj() == nil {
		return false
	}
	return named.Obj().Name() == commandRegistryTypeName
}

// ---------------------------------------------------------------------------
// INVARIANT: COMMAND-DISPATCH-REGISTER-CALLER-01 — RED fixture self-check
// ---------------------------------------------------------------------------

// TestCommandDispatchRegisterCaller01_RedFixture verifies that the scanner fires
// against the deliberate violations in commandregistercallerfixture, where a
// foreign (non-generated) package reaches RegisterHandler and LookupHandler
// through four syntactic shapes: direct call ×2, method value, and method
// expression.
//
// The fixture must produce ≥ 4 diagnostics — one per shape. Asserting ≥ 4
// (rather than ≥ 2) is what LOCKS the indirection coverage the rule godoc
// claims: a regression to call-only detection would drop the method-value and
// method-expression references and fail this self-check.
//
// Non-vacuity proof: if the scanner were trivially pass-open (reporting 0
// diagnostics for everything), this test would fail with found==0, catching
// the regression immediately.
func TestCommandDispatchRegisterCaller01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/commandregistercallerfixture"}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
					if isCommandRegistryMethod(p.TypesInfo, sel) &&
						!isCommandRegistryCaller(rel) {
						found++
					}
				})
			}
			return nil
		})

	assert.GreaterOrEqual(t, found, 4,
		"COMMAND-DISPATCH-REGISTER-CALLER-01 RED fixture self-check FAILED: "+
			"expected ≥ 4 violations from commandregistercallerfixture "+
			"(direct RegisterHandler + direct LookupHandler + method-value "+
			"RegisterHandler + method-expression LookupHandler); got %d. "+
			"If found<4 the scanner regressed to call-only detection (method-value "+
			"or method-expression form no longer matched); if found==0 it is "+
			"fail-open (isCommandRegistryMethod or isCommandRegistryCaller "+
			"regressed). Check that the fixture package loads under archtest_fixture "+
			"tag and that ResolveMethodCall resolves *command.Registry methods.",
		found)
}

// ---------------------------------------------------------------------------
// INVARIANT: COMMAND-GEN-FUNNEL-SOLE-EMITTER-01
// ---------------------------------------------------------------------------

// TestCommandGenFunnelSoleEmitter01 asserts that the typed Handler interface
// paired with Register + Dispatch free funcs referencing *command.Registry is
// DECLARED only inside generated/contracts/command/**. No hand-written file in
// ANY production package (non-generated, non-_test.go) may declare a look-alike
// trio.
//
// # What this guards
//
// The generated command_gen.go shape — Handler interface + Register func +
// Dispatch func, all referencing command.Registry — is the codegen funnel's
// output. If a hand-written cell declares an equivalent trio, it:
//
//  1. Creates a parallel registration path that bypasses the archtest
//     COMMAND-DISPATCH-REGISTER-CALLER-01 caller-allowlist (which only permits
//     generated/ packages to call RegisterHandler/LookupHandler).
//  2. Makes the command dispatch contract ambiguous — two independent "Handler"
//     interfaces for the same command concept drift over time.
//
// This invariant targets DECLARATIONS (interface types + free funcs), not calls.
// A cell legitimately CALLING the generated Register(reg, h) is fine and is
// covered by the normal production build.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Upstream: HARD — codegen funnel + golden. The generated shape is
//     byte-locked by the contractgen golden test
//     (testdata/golden/synth_command_command_gen_go.golden) and emitted
//     exclusively by command.tmpl. No other template or hand-coded file
//     creates this pattern because contractgen is the single emitter. A
//     future codegen change that alters the shape updates the golden, which
//     is a reviewed change. This is the "codegen funnel + golden" Hard range
//     from ai-robust.md §Hard 范本目录.
//   - Downstream: MEDIUM — this AST/types declaration scan. It guards the
//     hand-written path that the type-system alone cannot forbid (Go cannot
//     prevent any package from declaring an interface or free func with any
//     signature). The Medium rating is appropriate; the Hard upstream (codegen
//     golden) is the primary gate.
//
// # Scanned declaration shapes
//
// For ALL production (non-generated, non-_test.go) files, the rule checks for
// co-occurrence of:
//
//  1. An exported interface type with at least one method whose name starts
//     with "Handle" and whose result list includes (*SomeType, error) — the
//     generated Handler shape.
//  2. A free func named "Register", "Dispatch", or "DispatchAsync" (the #1667
//     async sibling) whose parameter list includes *command.Registry.
//
// Co-occurrence within the same package is the violation. Individual shapes
// (a standalone Register func, or a standalone Handler interface) are not
// violations on their own — only the trio mirroring the generated contract
// shape is forbidden.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//  1. A look-alike trio spread across multiple files in the same package: the
//     per-package aggregation (collecting all declaration shapes in the package,
//     then checking co-occurrence) handles this correctly.
//
//  2. A Register func that accepts command.Registry by value (not pointer):
//     The generated shape always uses *command.Registry; the scanner checks for
//     *types.Pointer wrapping the Registry Named type. A value-receiver Register
//     func would not be detected. This is an acceptable miss — the generated
//     shape always uses pointer, so a value-receiver is a semantic mismatch.
//
//  3. Dot-import: if command is dot-imported, the *command.Registry parameter
//     appears as *Registry in source but the types.Info still carries the
//     canonical *types.Named identity. The scanner uses types.Info parameter
//     type resolution, which is alias-proof and dot-import-proof. Not a blind spot.
//
//  4. Build-tag-gated files: as with COMMAND-DISPATCH-REGISTER-CALLER-01, a
//     production file under a non-default tag is not scanned. Documented.
func TestCommandGenFunnelSoleEmitter01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}

		// Scan EVERY production (non-generated) package, not just the
		// cell-authoring layers. The ADR (D2) and the codegen golden lock the typed
		// Handler + Register/Dispatch trio to generated/contracts/command/** ONLY; a
		// hand-written look-alike trio in ANY production package (runtime/, kernel/,
		// pkg/, adapters/, cmd/, tools/, …) — not only cells/examples/cellmodules —
		// creates a parallel registration path that bypasses the
		// COMMAND-DISPATCH-REGISTER-CALLER-01 caller-allowlist. generated/ packages
		// are excluded by Production() scope; runtime/command (the Registry's
		// defining package) declares METHODS on *Registry, not the free-func trio,
		// so it never trips. The co-occurrence gate is the *command.Registry
		// free-func param, which only generated code carries — making a false
		// positive in an unrelated infra package structurally impossible.
		pkgPath := p.Pkg.Path()

		// Collect declaration shapes per package:
		// - hasHandlerInterface: any exported interface type with a Handle*-prefix
		//   method returning (*T, error).
		// - hasRegistryFreeFunc: any free func named Register or Dispatch with a
		//   *command.Registry parameter.
		var hasHandlerInterface bool
		var handlerInterfacePos []string
		var hasRegistryFreeFunc bool
		var registryFreeFuncPos []string

		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue // test files are always allowed
			}
			if p.IsGenerated(file) {
				continue // generated/ excluded by Production scope, but guard anyway
			}

			for _, decl := range file.Decls {
				switch d := decl.(type) {
				case *ast.GenDecl:
					for _, spec := range d.Specs {
						ts, ok := spec.(*ast.TypeSpec)
						if !ok || ts.Name == nil || !ts.Name.IsExported() {
							continue
						}
						iface, ok := ts.Type.(*ast.InterfaceType)
						if !ok {
							continue
						}
						if isHandlerLikeInterface(iface) {
							hasHandlerInterface = true
							pos := p.Fset.Position(ts.Pos())
							handlerInterfacePos = append(handlerInterfacePos,
								fmt.Sprintf("%s:%d (type %s)", rel, pos.Line, ts.Name.Name))
						}
					}
				case *ast.FuncDecl:
					if d.Name == nil || d.Recv != nil {
						continue // skip methods; only free funcs
					}
					name := d.Name.Name
					if !isCommandSoleEmitterFuncName(name) {
						continue
					}
					if funcHasCommandRegistryParam(p.TypesInfo, d) {
						hasRegistryFreeFunc = true
						pos := p.Fset.Position(d.Pos())
						registryFreeFuncPos = append(registryFreeFuncPos,
							fmt.Sprintf("%s:%d (func %s)", rel, pos.Line, name))
					}
				}
			}
		}

		if hasHandlerInterface && hasRegistryFreeFunc {
			return []Diagnostic{{
				Rel: pkgPath,
				Message: fmt.Sprintf(
					"COMMAND-GEN-FUNNEL-SOLE-EMITTER-01: package %s declares "+
						"a look-alike command dispatch trio (Handler-style interface + "+
						"Register/Dispatch free func referencing *command.Registry). "+
						"This shape must ONLY be emitted by contractgen (command.tmpl) "+
						"into generated/contracts/command/**; hand-written declarations "+
						"in cells/ or examples/ bypass the codegen funnel and create "+
						"a parallel, unguarded registration path. "+
						"Handler interface at: %v. Register/Dispatch func at: %v. "+
						"Remove the hand-written trio and use the generated package instead.",
					pkgPath, handlerInterfacePos, registryFreeFuncPos,
				),
			}}
		}
		return nil
	})

	Report(t, "COMMAND-GEN-FUNNEL-SOLE-EMITTER-01", diags)
}

// isHandlerLikeInterface reports whether iface has at least one exported method
// whose name starts with "Handle" and whose result list has the shape
// (*SomeType, error) — the generated Handler interface pattern.
func isHandlerLikeInterface(iface *ast.InterfaceType) bool {
	if iface.Methods == nil {
		return false
	}
	for _, field := range iface.Methods.List {
		if len(field.Names) == 0 {
			continue
		}
		name := field.Names[0].Name
		if !strings.HasPrefix(name, "Handle") {
			continue
		}
		ft, ok := field.Type.(*ast.FuncType)
		if !ok {
			continue
		}
		if isPointerPlusErrorResults(ft.Results) {
			return true
		}
	}
	return false
}

// isPointerPlusErrorResults reports whether the result list contains exactly
// two results where the first is a pointer type and the second is the builtin
// error interface. This matches the generated (*Response, error) pattern.
func isPointerPlusErrorResults(fl *ast.FieldList) bool {
	if fl == nil || len(fl.List) != 2 {
		return false
	}
	// First result: must be a pointer type (*SomeResponse)
	firstType := fl.List[0].Type
	if _, ok := firstType.(*ast.StarExpr); !ok {
		return false
	}
	// Second result: must be the builtin error identifier
	secondType := fl.List[1].Type
	ident, ok := secondType.(*ast.Ident)
	return ok && ident.Name == "error"
}

// funcHasCommandRegistryParam reports whether the free func d has a parameter
// of type *command.Registry, resolved via go/types (alias-proof).
func funcHasCommandRegistryParam(info *types.Info, d *ast.FuncDecl) bool {
	if d.Type == nil || d.Type.Params == nil {
		return false
	}
	for _, field := range d.Type.Params.List {
		if isCommandRegistryPtrType(info, field.Type) {
			return true
		}
	}
	return false
}

// isCommandRegistryPtrType reports whether expr is the type *command.Registry,
// resolved via go/types to the canonical Named type in runtime/command.
// Covers *ast.StarExpr + *ast.SelectorExpr (qualified form) and dot-import
// (bare *ast.Ident) by relying on types.Info.Types for the underlying type.
func isCommandRegistryPtrType(info *types.Info, expr ast.Expr) bool {
	if info == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok {
		return false
	}
	ptr, ok := tv.Type.(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := ptr.Elem().(*types.Named)
	if !ok || named.Obj() == nil {
		return false
	}
	return named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == commandPkgPath &&
		named.Obj().Name() == commandRegistryTypeName
}

// ---------------------------------------------------------------------------
// INVARIANT: COMMAND-GEN-FUNNEL-SOLE-EMITTER-01 — RED fixture self-check
// ---------------------------------------------------------------------------

// TestCommandGenFunnelSoleEmitter01_RedFixture verifies that the declaration
// scanner fires against the deliberate look-alike trio in
// commandgenfunnelfixture, where a hand-written package declares Handler
// interface + Register func + Dispatch func referencing *command.Registry.
//
// The fixture must produce ≥ 1 diagnostic (the package-level co-occurrence).
//
// Non-vacuity proof: if the scanner were trivially pass-open (reporting 0
// diagnostics for everything), this test would fail with found==0.
func TestCommandGenFunnelSoleEmitter01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/commandgenfunnelfixture"}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}

			// Use the same detection logic as the production rule,
			// but applied to the fixture package (which simulates cells/ context).
			var hasHandlerInterface bool
			var hasRegistryFreeFunc bool

			for _, file := range p.Files {
				rel := p.Rel(file)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				for _, decl := range file.Decls {
					switch d := decl.(type) {
					case *ast.GenDecl:
						for _, spec := range d.Specs {
							ts, ok := spec.(*ast.TypeSpec)
							if !ok || ts.Name == nil || !ts.Name.IsExported() {
								continue
							}
							iface, ok := ts.Type.(*ast.InterfaceType)
							if !ok {
								continue
							}
							if isHandlerLikeInterface(iface) {
								hasHandlerInterface = true
							}
						}
					case *ast.FuncDecl:
						if d.Name == nil || d.Recv != nil {
							continue
						}
						name := d.Name.Name
						if name != "Register" && name != "Dispatch" {
							continue
						}
						if funcHasCommandRegistryParam(p.TypesInfo, d) {
							hasRegistryFreeFunc = true
						}
					}
				}
			}

			if hasHandlerInterface && hasRegistryFreeFunc {
				found++
			}
			return nil
		})

	assert.GreaterOrEqual(t, found, 1,
		"COMMAND-GEN-FUNNEL-SOLE-EMITTER-01 RED fixture self-check FAILED: "+
			"expected ≥ 1 package-level violation from commandgenfunnelfixture "+
			"(Handler interface + Register/Dispatch func with *command.Registry param); "+
			"got %d. If found==0 the declaration scanner is fail-open. "+
			"Check isHandlerLikeInterface / funcHasCommandRegistryParam / "+
			"isCommandRegistryPtrType resolve the fixture types correctly "+
			"under the archtest_fixture build tag.",
		found)
}

// ---------------------------------------------------------------------------
// INVARIANT: COMMAND-ASYNC-DISPATCH-CALLER-01
// ---------------------------------------------------------------------------

// isWithCommandDispatchCall reports whether call is a call to
// (*outbox.Relay).WithCommandDispatch, resolved alias-proof via go/types
// (ResolveMethodCall handles pointer/value/promoted/alias receivers and
// method-value forms; only the exact *types.Named "Relay" in runtime/outbox
// matches).
func isWithCommandDispatchCall(info *types.Info, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != withCommandDispatchMethod {
		return false
	}
	fn, ok := ResolveMethodCall(info, sel)
	if !ok || fn == nil || fn.Pkg() == nil || fn.Pkg().Path() != cmdRelayPkgPath {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	recv := sig.Recv().Type()
	if ptr, ok := recv.(*types.Pointer); ok {
		recv = ptr.Elem()
	}
	named, ok := recv.(*types.Named)
	if !ok || named.Obj() == nil {
		return false
	}
	return named.Obj().Name() == relayTypeName
}

// isGeneratedDispatchAsync reports whether v is a DIRECT reference (Ident or
// SelectorExpr) to a func named DispatchAsync declared under
// generated/contracts/command/**. Func literals, wrapper calls, and forwarded
// func vars deliberately do NOT resolve to a generated *types.Func here and are
// therefore reported as violations — the funnel requires the relay's
// command-dispatch values to be the generated symbols themselves. reg is carried
// as a parameter (command.AsyncDispatchFunc takes *Registry) precisely so no
// composition-root closure is needed and the map value stays a direct symbol.
func isGeneratedDispatchAsync(info *types.Info, v ast.Expr) bool {
	var ident *ast.Ident
	switch e := v.(type) {
	case *ast.SelectorExpr:
		ident = e.Sel
	case *ast.Ident:
		ident = e
	default:
		return false
	}
	fn, ok := info.ObjectOf(ident).(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Name() != dispatchAsyncFuncName {
		return false
	}
	rel := strings.TrimPrefix(fn.Pkg().Path(), PlatformModulePath+"/")
	return strings.HasPrefix(rel, commandGeneratedPrefix)
}

// asyncDispatchMapViolations returns a short description of each value in the
// WithCommandDispatch dispatch-map argument that is NOT a direct generated
// DispatchAsync reference. A non-literal map argument is itself one violation
// (its values cannot be statically verified — forward the map as a literal).
func asyncDispatchMapViolations(info *types.Info, arg ast.Expr) []string {
	lit, ok := arg.(*ast.CompositeLit)
	if !ok {
		return []string{"non-literal dispatch map (cannot verify values are generated DispatchAsync)"}
	}
	var bad []string
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if !isGeneratedDispatchAsync(info, kv.Value) {
			bad = append(bad, asyncDispatchValueText(kv.Value))
		}
	}
	return bad
}

// asyncDispatchValueText renders a best-effort label for a dispatch-map value
// for diagnostics (no fset needed).
func asyncDispatchValueText(v ast.Expr) string {
	switch e := v.(type) {
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok && e.Sel != nil {
			return id.Name + "." + e.Sel.Name
		}
		if e.Sel != nil {
			return e.Sel.Name
		}
		return "selector"
	case *ast.Ident:
		return e.Name
	case *ast.FuncLit:
		return "func literal"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// TestCommandAsyncDispatchCaller01 asserts that every value bound into
// (*outbox.Relay).WithCommandDispatch's dispatch map is a direct generated
// DispatchAsync symbol from generated/contracts/command/**. This is the
// DOWNSTREAM half of the async dispatch funnel double-lock: the relay can only
// dispatch a command in-process through the generated DispatchAsync (which
// decodes the payload, LookupHandler-s, type-asserts the typed Handler, and
// invokes it). A composition root that bound a hand-rolled func or a wrapper
// closure would bypass the typed Handler + registry single-handler guarantee.
// The UPSTREAM half is COMMAND-GEN-FUNNEL-SOLE-EMITTER-01 (DispatchAsync is
// codegen sole-emitted + golden byte-locked, so it cannot be hand-written).
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream: HARD — go/types value-resolution binds each map value's
//     *types.Func identity (pkg path + name), alias-proof and form-unique
//     (Ident / SelectorExpr only; func literals and wrappers are violations).
//     Residual: a forwarded func var (`var f command.AsyncDispatchFunc = …;
//     map{id: f}`) resolves to a var, not the generated func — reported as a
//     violation, so the only escape is laundering through an intermediate that
//     still does not name DispatchAsync, the #1508-family data-flow ceiling.
//   - Upstream: MEDIUM — Go has no friend-package; it cannot make "only the
//     relay may receive an AsyncDispatchFunc, and only a generated one"
//     compile-time. Mirrors COMMAND-DISPATCH-REGISTER-CALLER-01 and the
//     #851/#893/#1282/#1575 permanent ceilings. Hard-upgrade tracked with the
//     sync sibling at gh #1575.
//
// # Scope: production, non-test
//
// Only production, non-_test.go files are scanned. The funnel's target is
// production composition-root wiring (cmd/ / cellmodules/ / examples/ run.go).
// Test harnesses legitimately build relays with FAKE AsyncDispatchFunc values to
// unit-test the dispatch-vs-publish branch in isolation without importing a
// concrete generated device command into a runtime unit test — forcing them
// through a generated symbol would couple every relay test to a specific command
// package. A test binding a non-generated func does not ship, so it is not a
// production bypass. (Blind spot: a _test.go that wired a real production relay
// is not caught here; that is acceptable — the same test-exempt posture as the
// sole-emitter declaration scan.)
//
// # Vacuity
//
// There are ZERO production WithCommandDispatch callsites today — the real async
// command producer wiring (devicecell enqueue → outbox, iotdevice durable-mode
// relay WithCommandDispatch) is deferred to a follow-up (gh backlog, Blocked-by
// #1667), exactly like GRPC-METHOD-IN-CONTRACT-01 ships vacuous-green before its
// first consumer. Phase 1 is therefore vacuous-green; the RED fixture below
// proves the rule bites, and Phase 2 anti-vacuity (a generated DispatchAsync
// must exist) prevents the funnel from guarding nothing.
func TestCommandAsyncDispatchCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			if strings.HasSuffix(rel, "_test.go") {
				continue // production composition-root wiring only; see godoc
			}
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				if !isWithCommandDispatchCall(p.TypesInfo, call) || len(call.Args) < 2 {
					return
				}
				for _, bad := range asyncDispatchMapViolations(p.TypesInfo, call.Args[1]) {
					pos := p.Fset.Position(call.Pos())
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"COMMAND-ASYNC-DISPATCH-CALLER-01: Relay.WithCommandDispatch is bound "+
								"a non-generated dispatch value (%s). Relay command-dispatch values "+
								"MUST be a generated DispatchAsync symbol from "+
								"generated/contracts/command/** (e.g. enqueue.DispatchAsync) so every "+
								"async command crosses the typed Handler + registry funnel. Do not "+
								"wrap it in a closure or hand-roll a func — bind the generated "+
								"DispatchAsync directly.",
							bad,
						),
					})
				}
			})
		}
		return d
	})

	// Phase 2: anti-vacuity — at least one generated DispatchAsync must exist,
	// else the funnel guards nothing (codegen removed/renamed it). Production()
	// excludes generated/, so scan the generated command glob via Typed().
	const generatedPkgGlob = "./generated/contracts/command/..."
	var generatedDispatchAsyncFound bool
	_ = Run(t, Typed(TypedOpts{}, []string{generatedPkgGlob}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		for _, file := range p.Files {
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Recv != nil || fd.Name == nil {
					continue
				}
				if fd.Name.Name == dispatchAsyncFuncName && funcHasCommandRegistryParam(p.TypesInfo, fd) {
					generatedDispatchAsyncFound = true
				}
			}
		}
		return nil
	})
	if !generatedDispatchAsyncFound {
		diags = append(diags, Diagnostic{
			Message: "COMMAND-ASYNC-DISPATCH-CALLER-01 anti-vacuity: NO generated DispatchAsync " +
				"func found under " + generatedPkgGlob + ". Either codegen no longer emits " +
				"DispatchAsync (command.tmpl regressed) or the scanner broke — the relay " +
				"command-dispatch funnel guards nothing without a generated DispatchAsync to bind.",
		})
	}

	Report(t, "COMMAND-ASYNC-DISPATCH-CALLER-01", diags)
}

// TestCommandAsyncDispatchCaller01_RedFixture verifies the scanner fires against
// the deliberate violations in commandasyncdispatchfixture, where
// WithCommandDispatch is bound two non-generated values: a package-level local
// func reference AND an inline func literal. Asserting ≥ 2 locks both shapes —
// a regression that only flagged func literals (or only named funcs) would drop
// to 1 and fail. found==0 means the scanner is fail-open.
func TestCommandAsyncDispatchCaller01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var found int
	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/commandasyncdispatchfixture"}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			for _, file := range p.Files {
				EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
					if !isWithCommandDispatchCall(p.TypesInfo, call) || len(call.Args) < 2 {
						return
					}
					found += len(asyncDispatchMapViolations(p.TypesInfo, call.Args[1]))
				})
			}
			return nil
		})

	assert.GreaterOrEqual(t, found, 2,
		"COMMAND-ASYNC-DISPATCH-CALLER-01 RED fixture self-check FAILED: expected ≥ 2 "+
			"violations from commandasyncdispatchfixture (a local-func value + a func-literal "+
			"value bound into WithCommandDispatch); got %d. If found==0 the scanner is "+
			"fail-open — check isWithCommandDispatchCall / isGeneratedDispatchAsync / "+
			"asyncDispatchMapViolations resolve under the archtest_fixture build tag.",
		found)
}

// TestCommandSoleEmitterFuncName_IncludesDispatchAsync locks DispatchAsync into
// the sole-emitter free-func name set (#1667). Without this, a regression that
// dropped DispatchAsync from isCommandSoleEmitterFuncName would let a
// hand-written Handler + DispatchAsync look-alike (no Register/Dispatch) escape
// COMMAND-GEN-FUNNEL-SOLE-EMITTER-01 — the RED fixture still trips via
// Register/Dispatch, so a per-name assertion is the only direct guard.
func TestCommandSoleEmitterFuncName_IncludesDispatchAsync(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"Register", "Dispatch", "DispatchAsync"} {
		assert.True(t, isCommandSoleEmitterFuncName(name),
			"isCommandSoleEmitterFuncName(%q) must be true — it is a generated command "+
				"sole-emitter free-func name", name)
	}
	assert.False(t, isCommandSoleEmitterFuncName("Subscribe"),
		"isCommandSoleEmitterFuncName must not over-match unrelated names")
}
