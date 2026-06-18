//go:build archtest

// INVARIANT: AMQP-URL-REDACTION-FUNNEL-01
//
// AMQP connection URLs carry embedded credentials in the DSN form
// amqp://user:pass@host/vhost. Any path that leaks a raw AMQP URL into a
// slog/fmt/errcode sink exposes those credentials — in structured logs, error
// messages, or wire responses. The sanitize* funnel in adapters/rabbitmq
// (sanitizeURL / sanitizeErrorURL / sanitizeDialError) redacts user:pass before
// any URL string reaches a sink.
//
// This invariant scans adapters/rabbitmq and cellmodules/eventtransport for
// AMQP-URL field selections (rabbitmq.Config.URL and eventtransport.brokerSpec.url)
// that flow into a log/slog, fmt, or errcode sink call WITHOUT being enclosed by
// a sanitizer call. The current production code is entirely clean (zero violations
// in FunnelOnly); DetectsWithoutFunnel proves the detector is non-vacuous by
// disabling the sanitizer-stopAt boundary and confirming at least one sink-arg
// traversal crosses a .URL/.url selector on the way down.
//
// AI-robust grading: Medium. The funnel is a typed-field callsite scan resolved
// with go/types (*types.Named identity for Config / brokerSpec), not a bare string
// anchor. It is not Hard because the URL field is a plain string: a local variable
// (url := c.config.URL; slog.Info("...", slog.String("u", url))) would launder
// the field access into a non-SelectorExpr reference that this scan cannot see.
// The callee-resolved + type-identity field check is the Go-reachable ceiling for
// this shape.
//
// Blind spots: local-variable laundering (url := cells[id]; slog.Info(..., url))
// and map-value reads (cells[id]) are not field selections and are therefore
// outside this scan's coverage. The map-value path is exercised by the behavior
// test in transport_test.go (TestDedupBrokerURL_CredentialNonLeak), closing the
// gap without a Hard-form sealed URL type. Hard-via-sealed-redacted-Stringer URL
// type across all adapter call sites would require a large-surface API change;
// cost is not justified given the current threat model and the behavior-test
// coverage.
//
// Scanner logic lives in this file (single-file rule, dogfood-only: scans
// adapters/rabbitmq + cellmodules/eventtransport, neither of which exists in an
// external Cell repo). Platform-symbol paths derived from [PlatformModulePath]
// and [PlatformFrameworkModulePath].

package archtest

import (
	"go/ast"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	// amqpRabbitmqPkgPath is the canonical import path of the rabbitmq adapter.
	amqpRabbitmqPkgPath = PlatformModulePath + "/adapters/rabbitmq"
	// amqpEventTransportPkgPath is the canonical import path of eventtransport.
	amqpEventTransportPkgPath = PlatformModulePath + "/cellmodules/eventtransport"

	// amqpRabbitmqConfigTypeName is the type whose URL field is tracked in rabbitmq.
	amqpRabbitmqConfigTypeName = "Config"
	// amqpRabbitmqURLFieldName is the exported URL field on rabbitmq.Config.
	amqpRabbitmqURLFieldName = "URL"

	// amqpBrokerSpecTypeName is the type whose url field is tracked in eventtransport.
	amqpBrokerSpecTypeName = "brokerSpec"
	// amqpBrokerSpecURLFieldName is the unexported url field on eventtransport.brokerSpec.
	amqpBrokerSpecURLFieldName = "url"
)

// amqpSanitizerNames is the set of sanitizer function names in adapters/rabbitmq.
// A call whose callee name (last segment) is in this set is treated as a sanitizer
// boundary — FindFirstInSubtreeStopAt will not descend into it.
var amqpSanitizerNames = map[string]struct{}{
	"sanitizeURL":       {},
	"sanitizeErrorURL":  {},
	"sanitizeDialError": {},
}

// amqpSinkPkgPaths is the set of package paths whose calls are considered sinks.
var amqpSinkPkgPaths = map[string]struct{}{
	"log/slog": {},
	"fmt":      {},
	PlatformFrameworkModulePath + "/pkg/errcode": {},
}

// isAMQPURLFieldSel reports whether sel is a rabbitmq.Config.URL or
// eventtransport.brokerSpec.url field selection, resolved via go/types.
func isAMQPURLFieldSel(p *Pass, sel *ast.SelectorExpr) bool {
	return isRabbitmqConfigURL(p, sel) || isBrokerSpecURL(p, sel)
}

// isRabbitmqConfigURL reports whether sel is a SelectorExpr whose selector is
// "URL" and whose receiver type is rabbitmq.Config (or *rabbitmq.Config).
func isRabbitmqConfigURL(p *Pass, sel *ast.SelectorExpr) bool {
	if sel.Sel.Name != amqpRabbitmqURLFieldName {
		return false
	}
	return amqpNamedTypeIs(p, sel.X, amqpRabbitmqPkgPath, amqpRabbitmqConfigTypeName)
}

// isBrokerSpecURL reports whether sel is a SelectorExpr whose selector is "url"
// and whose receiver type is eventtransport.brokerSpec (or *brokerSpec).
func isBrokerSpecURL(p *Pass, sel *ast.SelectorExpr) bool {
	if sel.Sel.Name != amqpBrokerSpecURLFieldName {
		return false
	}
	return amqpNamedTypeIs(p, sel.X, amqpEventTransportPkgPath, amqpBrokerSpecTypeName)
}

// amqpNamedTypeIs reports whether expr's go/types type (dereferenced one level
// for pointer receivers, unaliased) is a *types.Named whose package path is
// pkgPath and whose name is typeName.
func amqpNamedTypeIs(p *Pass, expr ast.Expr, pkgPath, typeName string) bool {
	if p.TypesInfo == nil {
		return false
	}
	t := p.TypesInfo.TypeOf(expr)
	if t == nil {
		return false
	}
	// Dereference one level for pointer receivers (e.g. *rabbitmq.Config).
	if ptr, ok := t.Underlying().(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == pkgPath && obj.Name() == typeName
}

// amqpIsSanitizerCall reports whether call is a sanitizer boundary. Matches
// calls whose callee name (final segment) is in amqpSanitizerNames.
func amqpIsSanitizerCall(call *ast.CallExpr) bool {
	name := amqpCallName(call)
	_, ok := amqpSanitizerNames[name]
	return ok
}

// amqpCallName extracts the base function/method name from a call expression.
func amqpCallName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

// amqpIsSinkCall reports whether call is a sink call (slog / fmt / errcode
// package, any function).
func amqpIsSinkCall(p *Pass, call *ast.CallExpr) bool {
	if p.TypesInfo == nil {
		return false
	}
	pkgPath, _, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
	if !ok {
		return false
	}
	_, isSink := amqpSinkPkgPaths[pkgPath]
	return isSink
}

// collectAMQPURLLeakDiags collects diagnostics for AMQP URL field selections that
// flow into sink calls without sanitizer protection. It is the per-Pass body for
// the AMQP-URL-REDACTION-FUNNEL-01 scan.
//
// When treatSanitizerSafe=true, a sanitizer call acts as a stopAt boundary so
// field selections inside sanitizer arguments are not considered leaks (FunnelOnly
// mode). When treatSanitizerSafe=false, the stopAt is a no-op and every sink-arg
// traversal that reaches a .URL/.url selector reports a violation (DetectsWithoutFunnel
// mode — proves the detection path is non-vacuous).
func collectAMQPURLLeakDiags(p *Pass, diags *[]Diagnostic, treatSanitizerSafe bool) {
	if p.Pkg == nil || p.TypesInfo == nil {
		return
	}
	pkgPath := p.Pkg.Path()
	if pkgPath != amqpRabbitmqPkgPath && pkgPath != amqpEventTransportPkgPath {
		return
	}

	stopAt := func(n ast.Node) bool {
		if !treatSanitizerSafe {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		return ok && amqpIsSanitizerCall(call)
	}
	predicate := func(sel *ast.SelectorExpr) bool {
		return isAMQPURLFieldSel(p, sel)
	}

	for _, f := range p.Files {
		rel := p.Rel(f)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		collectAMQPURLLeakDiagsFile(p, f, rel, diags, stopAt, predicate)
	}
}

// collectAMQPURLLeakDiagsFile collects diagnostics for a single file.
// Extracted to keep collectAMQPURLLeakDiags within cognitive complexity ≤ 15.
func collectAMQPURLLeakDiagsFile(
	p *Pass,
	f ast.Node,
	rel string,
	diags *[]Diagnostic,
	stopAt func(ast.Node) bool,
	predicate func(*ast.SelectorExpr) bool,
) {
	EachInSubtree[ast.CallExpr](f, func(sinkCall *ast.CallExpr) {
		if !amqpIsSinkCall(p, sinkCall) {
			return
		}
		// Search each argument subtree for an AMQP URL field selection not
		// protected by a sanitizer boundary.
		for _, arg := range sinkCall.Args {
			// When treatSanitizerSafe=true: if the top-level arg is itself a
			// sanitizer call, it is safe by definition — skip entirely.
			// (FindFirstInSubtreeStopAt always enters the root before consulting
			// stopAt, so we must guard this case explicitly.)
			if stopAt != nil && stopAt(arg) {
				continue
			}
			sel, found := FindFirstInSubtreeStopAt[ast.SelectorExpr](arg, stopAt, predicate)
			if !found {
				continue
			}
			pos := p.Fset.Position(sel.Pos())
			*diags = append(*diags, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: "AMQP-URL-REDACTION-FUNNEL-01: AMQP URL field selection " +
					"flows into a slog/fmt/errcode sink without sanitizer protection — " +
					"AMQP URLs carry user:pass credentials; wrap with sanitizeURL / " +
					"sanitizeErrorURL / sanitizeDialError (defined in " +
					"adapters/rabbitmq/connection.go) before passing to any sink",
			})
		}
	})
}

// TestAMQPURLRedaction_FunnelOnly enforces AMQP-URL-REDACTION-FUNNEL-01:
// every AMQP URL field selection (rabbitmq.Config.URL, eventtransport.brokerSpec.url)
// that appears as an argument to a slog/fmt/errcode sink call must be enclosed
// by a sanitizer call (sanitizeURL / sanitizeErrorURL / sanitizeDialError).
// Expects zero violations because the current production code routes every
// URL-to-sink path through the sanitize funnel.
func TestAMQPURLRedaction_FunnelOnly(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic
	pkgs := []string{amqpRabbitmqPkgPath, amqpEventTransportPkgPath}
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, pkgs),
		func(p *Pass) []Diagnostic {
			collectAMQPURLLeakDiags(p, &diags, true)
			return nil
		})

	Report(t, "AMQP-URL-REDACTION-FUNNEL-01", diags)
}

// TestAMQPURLRedaction_DetectsWithoutFunnel proves that the scanner's detection
// path is non-vacuous: when sanitizer calls are NOT treated as safe boundaries
// (treatSanitizerSafe=false), the scanner must find at least one AMQP URL field
// selection inside a sink-call argument tree. If this test reports 0 detections,
// the go/types resolution path may be silently broken (wrong package paths, failed
// type resolution, or an API change in rabbitmq.Config / brokerSpec).
func TestAMQPURLRedaction_DetectsWithoutFunnel(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic
	pkgs := []string{amqpRabbitmqPkgPath, amqpEventTransportPkgPath}
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, pkgs),
		func(p *Pass) []Diagnostic {
			collectAMQPURLLeakDiags(p, &diags, false)
			return nil
		})

	assert.GreaterOrEqual(t, len(diags), 1,
		"AMQP-URL-REDACTION-FUNNEL-01: scanner found 0 AMQP URL field selections inside "+
			"sink-call args when sanitizer stopAt is disabled — "+
			"the go/types resolution path may be silently broken (check package paths, "+
			"type names rabbitmq.Config.URL / brokerSpec.url, or build tags)")
}

// TestAMQPURLRedaction_ScannerNonVacuous proves the field-selection recognizer
// fires on at least one AMQP URL selector anywhere in the scanned packages
// (not just inside sink args), guarding against a silent go/types failure that
// would make both FunnelOnly and DetectsWithoutFunnel vacuously pass.
func TestAMQPURLRedaction_ScannerNonVacuous(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var found int
	pkgs := []string{amqpRabbitmqPkgPath, amqpEventTransportPkgPath}
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, pkgs),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			pkgPath := p.Pkg.Path()
			if pkgPath != amqpRabbitmqPkgPath && pkgPath != amqpEventTransportPkgPath {
				return nil
			}
			for _, f := range p.Files {
				if strings.HasSuffix(p.Rel(f), "_test.go") {
					continue
				}
				EachInSubtree[ast.SelectorExpr](f, func(sel *ast.SelectorExpr) {
					if isAMQPURLFieldSel(p, sel) {
						found++
					}
				})
			}
			return nil
		})

	assert.GreaterOrEqual(t, found, 1,
		"AMQP-URL-REDACTION-FUNNEL-01: scanner found 0 AMQP URL field selections "+
			"anywhere in the scanned packages — "+
			"the go/types resolution path may be silently broken; "+
			"verify that rabbitmq.Config.URL and brokerSpec.url are correctly matched")
}

// TestAMQPURLRedaction_SanitizerBoundarySuppresses proves the sanitizer stopAt
// boundary itself is non-vacuous: it must suppress at least one real
// URL-into-sink violation in production code. Counting diagnostics with the
// boundary enabled (FunnelOnly mode) vs disabled (DetectsWithoutFunnel mode), the
// enabled count must be strictly lower — i.e. ≥1 inline sanitizer(.URL) call in a
// sink arg is actually being recognized as a boundary. Without this, a renamed or
// mistyped sanitizer in amqpSanitizerNames could leave the funnel detecting
// nothing while FunnelOnly still (vacuously) reports zero.
func TestAMQPURLRedaction_SanitizerBoundarySuppresses(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	count := func(treatSanitizerSafe bool) int {
		var diags []Diagnostic
		pkgs := []string{amqpRabbitmqPkgPath, amqpEventTransportPkgPath}
		_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, pkgs),
			func(p *Pass) []Diagnostic {
				collectAMQPURLLeakDiags(p, &diags, treatSanitizerSafe)
				return nil
			})
		return len(diags)
	}

	suppressed := count(false) - count(true)
	assert.GreaterOrEqual(t, suppressed, 1,
		"AMQP-URL-REDACTION-FUNNEL-01: the sanitizer stopAt boundary suppressed 0 "+
			"violations (enabled count not lower than disabled count) — "+
			"a sanitizer name in amqpSanitizerNames may be stale/mistyped, or the "+
			"production sanitizer-wrapped URL-in-sink callsites were removed; the "+
			"funnel is no longer proven to redact anything")
}
