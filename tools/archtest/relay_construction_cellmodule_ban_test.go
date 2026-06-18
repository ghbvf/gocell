//go:build archtest

// INVARIANT: RELAY-CONSTRUCTION-CELLMODULE-BAN-01
//
// # RELAY-CONSTRUCTION-CELLMODULE-BAN-01
//
// The outbox relay constructor + registrar
//
//	framework/runtime/outbox.NewRelay
//	framework/runtime/bootstrap.WithRelay
//
// MUST NOT be called from any cellmodules/ production file. The outbox relay is
// per-POOL assembly infrastructure (one relay drains one pool's outbox table),
// NOT per-cell business wiring. It is constructed exclusively in the composition
// root's single provisioning site (cmd/corebundle/cap_wiring.go), where the pool
// is opened, and registered there via WithRelay keyed by the pool's
// InfraInstanceKey (#2341). Building a relay inside a cell module resurrects the
// per-cell-relay dual-path eliminated by #2341: in colocated mode N cells share
// one outbox table, so N cell-owned relays would double-drain it; in split mode a
// cell can only reach its own pool, so cell-owned relays cannot drain a sibling's.
//
// Before #2341 this exact dual-path existed: cellmodules/configcore/storage.go
// built the single assembly relay (a historical accident — it drained the shared
// table for all three cells). That call site is the anti-vacuity RED reference;
// after #2341 cellmodules/ holds ZERO relay constructions, so the dogfood is GREEN
// and the synthetic fixture (relayctorfixture, archtest_fixture-gated) supplies the
// non-vacuity RED proof.
//
// # AI-robust grade: Medium (caller funnel)
//
// scanRelayConstructionViolations resolves every CallExpr callee via
// archtest.ResolvePackageRef (owning package import path via go/types, not the
// source Ident), matching by (pkgPath, name) — so name shadowing / aliased / dot
// imports are all caught by package identity. The Hard ceiling: a relay needs a
// pool handle, and cell modules legitimately hold the pool (to build session /
// ledger / config stores via the not-banned NewSessionStore/NewOutboxStore
// family), so "no relay construction" cannot be made type-inexpressible — it is a
// documented Go ceiling, same family as CAPABILITY-PROVIDER-FUNNEL-01. The
// composition root (cmd/corebundle/cap_wiring.go) is naturally out of scope: this
// rule scans ./cellmodules/... only, so the sanctioned site needs no allowlist.
//
// # Blind spots (BS)
//
//   - BS-1 Function-value indirection: `var f = outboxruntime.NewRelay; f(...)`
//     resolves to a *types.Var (ok=false) and is not flagged — same accepted BS as
//     CAPABILITY-PROVIDER-FUNNEL-01 BS-2. cellmodules wiring uses direct calls.
//   - BS-2 Reflection construction: out of scope per ai-robust.md §3.
//   - BS-3 _test.go scope: Typed(Tests:false) loads production variants only; the
//     scanner additionally filters _test.go by rel suffix as defense-in-depth.
//   - BS-4 examples/ composition roots: the scanner covers ./cellmodules/... only and
//     intentionally excludes examples/. examples/ssobff/app.go and
//     examples/iotdevice/run.go are composition roots that legitimately call
//     outboxruntime.NewRelay and bootstrap.WithRelay — these are the correct
//     sanctioned sites for those examples (analogous to cmd/corebundle/cap_wiring.go).
//     Extending the scan to ./examples/... would false-positive on those root files.
//     Narrowing to examples/*/cells/** to catch only example cell implementations is
//     structurally fragile (example directory layouts are not enforced). This gap is
//     accepted: example cell directories currently hold zero relay constructions, and
//     the pattern is documented in code review guidance. Hard-ening path: if a
//     standard examples/cells/ layout is enforced, extend the scan with a path filter
//     that excludes root files (run.go / app.go). Filed as a known Medium→Hard gap.

package archtest

import (
	"go/ast"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	relayCtorBanRuleID = "RELAY-CONSTRUCTION-CELLMODULE-BAN-01"
	// Derived from PlatformFrameworkModulePath so a module rename / /v2 bump updates
	// one place (ARCHTEST-MODULE-PATH-FUNNEL-01). The relay constructor lives in the
	// framework module's runtime/outbox; the registrar in runtime/bootstrap.
	relayCtorOutboxPath    = PlatformFrameworkModulePath + "/runtime/outbox"
	relayCtorBootstrapPath = PlatformFrameworkModulePath + "/runtime/bootstrap"
)

// relayCtorBannedInCellmodules is the closed set of (import path → constructor
// names) banned in cellmodules/ production files.
var relayCtorBannedInCellmodules = map[string]map[string]struct{}{
	relayCtorOutboxPath:    {"NewRelay": {}},
	relayCtorBootstrapPath: {"WithRelay": {}},
}

// scanRelayConstructionViolations walks every CallExpr in pass.Files, resolves the
// callee to its (pkgPath, name) tuple via archtest.ResolvePackageRef, and flags hits
// whose owning package + name are in relayCtorBannedInCellmodules — unless the file
// is a _test.go file. There is no sanctioned-site allowlist: the rule is run only
// over ./cellmodules/..., and the sole sanctioned site (cmd/corebundle/cap_wiring.go)
// is outside that scan scope.
func scanRelayConstructionViolations(p *Pass) []Diagnostic {
	var out []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
			pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
			if !ok {
				return
			}
			banned, hasPkg := relayCtorBannedInCellmodules[pkgPath]
			if !hasPkg {
				return
			}
			if _, isBanned := banned[name]; !isBanned {
				return
			}
			line := p.Fset.Position(call.Pos()).Line
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: shortPkg(pkgPath) + "." + name + " is per-pool assembly infrastructure and may " +
					"only be constructed/registered in cmd/corebundle/cap_wiring.go (keyed by the pool's " +
					"InfraInstanceKey); cell modules must not own outbox relays (#2341)",
			})
		})
	}
	return out
}

// TestRelayConstructionCellmoduleBan_CellmodulesClean dogfoods
// RELAY-CONSTRUCTION-CELLMODULE-BAN-01 against GoCell itself: no cellmodules/
// production file may construct (NewRelay) or register (WithRelay) an outbox relay.
// After #2341 the relay is owned by the composition root (cap_wiring.go), so this
// must be GREEN.
func TestRelayConstructionCellmoduleBan_CellmodulesClean(t *testing.T) {
	diags := Run(t, Typed(
		TypedOpts{Tests: false},
		[]string{"./cellmodules/..."},
	), scanRelayConstructionViolations)
	Report(t, relayCtorBanRuleID, diags)
}

// TestRelayConstructionCellmoduleBan_RedFixtureDetected asserts the production
// scanner catches both banned call shapes in the synthetic fixture
// (relayctorfixture, archtest_fixture-gated). It runs the SAME
// scanRelayConstructionViolations the dogfood uses (single source) so the rule
// cannot pass vacuously once cellmodules/ holds zero real relay constructions.
//
// Coverage: 1 outbox.NewRelay + 1 bootstrap.WithRelay = 2.
func TestRelayConstructionCellmoduleBan_RedFixtureDetected(t *testing.T) {
	diags := Run(t, Fixture(
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/relayctorfixture/..."},
	), scanRelayConstructionViolations)

	for _, d := range diags {
		t.Logf("RED fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}
	// Equality (not ≥) so the fixture cannot drift silently — any change to the
	// fixture files must update this count.
	assert.Len(t, diags, 2,
		"fixture must yield exactly 2 RELAY-CONSTRUCTION-CELLMODULE-BAN-01 hits "+
			"(1 outbox.NewRelay + 1 bootstrap.WithRelay); if the fixture changes "+
			"intentionally, update the expected count")
}
