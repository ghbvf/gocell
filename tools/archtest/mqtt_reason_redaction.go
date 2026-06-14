// INVARIANT: REASON-NAME-REDACTION-01
//
// mqtt_reason_redaction.go — importable REASON-NAME-REDACTION-01 rule logic.
//
// This is the non-test home of the REASON-NAME-REDACTION-01 scanner so it
// can be compiled by external Cell repositories through the CellRule pattern
// (Go never compiles a dependency's _test.go, so rule logic that external repos
// must run cannot live in a _test.go file). GoCell's own
// TestMQTTReasonNameRedaction_FunnelOnly /
// TestMQTTReasonNameRedaction_ScannerNonVacuous (mqtt_reason_redaction_test.go)
// call the same shared helpers — single source, no parallel rule body.
//
// register=no — gocell-internal-layout (scans adapters/mqtt), NOT in
// StandardCellRules() and NOT promised to run externally — this dogfood-only
// rule targets a package an external repo lacks, so a manual ExtraRules caller
// does not get a clean pass; migrated for unified PlatformModulePath
// parameterization +
// fork-safety, dogfooded via the per-rule Tests).
//
// Platform-symbol paths are anchored to [PlatformModulePath] so a module
// rename updates exactly one place and no bare literal appears here.
package archtest

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"
)

const (
	mqttReasonNameDetailKey    = "reasonName"
	mqttReasonDetailFunnelFunc = "reasonDetailOptions"
	// mqttErrcodePkgPath is the canonical import path of the errcode package —
	// derived from PlatformModulePath so no bare literal appears here.
	mqttErrcodePkgPath          = PlatformFrameworkModulePath + "/pkg/errcode"
	mqttErrcodePublicStringName = "PublicString"
)

// mqttIsReasonNamePublicStringCall reports whether call is
// errcode.PublicString("reasonName", …): the callee is resolved to the errcode
// package via go/types and the first argument is const-evaluated to "reasonName"
// (so a const-named key like reasonDetailKeyName is also matched).
func mqttIsReasonNamePublicStringCall(p *Pass, call *ast.CallExpr) bool {
	if len(call.Args) == 0 {
		return false
	}
	pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
	if !ok || pkgPath != mqttErrcodePkgPath || name != mqttErrcodePublicStringName {
		return false
	}
	key, ok := EvaluateConstString(p.TypesInfo, call.Args[0])
	return ok && key == mqttReasonNameDetailKey
}

// collectReasonNameDiags is the per-Pass body for CheckMQTTReasonNameRedaction,
// extracted to keep the Check* func within gocognit ≤15.
func collectReasonNameDiags(p *Pass, diags *[]Diagnostic) {
	if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != mqttPkgPath {
		return
	}
	for _, f := range p.Files {
		rel := p.Rel(f)
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
			if !mqttIsReasonNamePublicStringCall(p, call) {
				return
			}
			if mqttEnclosingFuncName(f, call.Pos()) == mqttReasonDetailFunnelFunc {
				return
			}
			pos := p.Fset.Position(call.Pos())
			*diags = append(*diags, Diagnostic{
				Rel:  rel,
				Line: pos.Line,
				Message: fmt.Sprintf(
					"REASON-NAME-REDACTION-01: errcode.PublicString(%q, …) at %s:%d outside %s — "+
						"reasonName must be built only via %s so auth-code redaction cannot be bypassed",
					mqttReasonNameDetailKey, rel, pos.Line, mqttReasonDetailFunnelFunc, mqttReasonDetailFunnelFunc,
				),
			})
		})
	}
}

// CheckMQTTReasonNameRedaction runs the REASON-NAME-REDACTION-01 scan and
// returns its diagnostics.
//
// register=no — gocell-internal-layout (scans adapters/mqtt), NOT in
// StandardCellRules() and NOT promised to run externally — this dogfood-only
// rule targets a package an external repo lacks, so a manual ExtraRules caller
// does not get a clean pass; migrated for unified PlatformModulePath
// parameterization +
// fork-safety, dogfooded via the per-rule Tests).
func CheckMQTTReasonNameRedaction(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: cfg.BuildTags},
		[]string{mqttPkgPath}),
		func(p *Pass) []Diagnostic {
			collectReasonNameDiags(p, &diags)
			return nil
		})
	return diags
}
