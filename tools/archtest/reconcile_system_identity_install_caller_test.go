//go:build archtest

// INVARIANT: RECONCILE-SYSTEM-IDENTITY-INSTALL-CALLER-01
//
// RECONCILE-SYSTEM-IDENTITY-INSTALL-CALLER-01 pins the reconcile system
// producer identity to one chokepoint:
//
//   - installSystemProducerIdentity may be referenced only from
//     (*kernel/reconcile.Loop).process.
//   - The principal ctx-key setters inside package kernel/reconcile may be
//     referenced only from installSystemProducerIdentity.
//
// This closes the former file-level blind spot in
// CTXKEYS-PRINCIPAL-WRITE-CALLER-01 where a second function added to
// kernel/reconcile/identity.go could write principal ctx keys without tripping
// the file allowlist.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	reconcileSystemIdentityPkg               = PlatformFrameworkModulePath + "/kernel/reconcile"
	reconcileSystemIdentityInstallFunc       = "installSystemProducerIdentity"
	reconcileSystemIdentityAllowedInstallRef = "(*" + reconcileSystemIdentityPkg + ".Loop).process"
	reconcileSystemIdentityAllowedSetterRef  = reconcileSystemIdentityPkg + "." + reconcileSystemIdentityInstallFunc
)

var reconcileSystemIdentitySetters = []string{
	"WithActorID",
	"WithSubjectID",
	"WithTenantID",
	"WithSessionID",
}

type reconcileSystemIdentityObserved struct {
	installerCallers map[string]struct{}
	setterCallers    map[string]map[string]struct{}
}

func newReconcileSystemIdentityObserved() *reconcileSystemIdentityObserved {
	return &reconcileSystemIdentityObserved{
		installerCallers: map[string]struct{}{},
		setterCallers:    map[string]map[string]struct{}{},
	}
}

func TestReconcileSystemIdentityInstallCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := newReconcileSystemIdentityObserved()
	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		return scanReconcileSystemIdentityInstallCaller01(p, observed)
	})
	diags = append(diags, staleReconcileSystemIdentityDiags(observed)...)
	Report(t, "RECONCILE-SYSTEM-IDENTITY-INSTALL-CALLER-01", diags)
}

func TestReconcileSystemIdentityInstallCaller01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := filepath.Join(findModuleRoot(t), "tools", "archtest", "testdata", "reconcile_system_identity_violate")
	observed := newReconcileSystemIdentityObserved()
	var found int
	_ = Run(t, StandaloneModule(root, TypedOpts{}, []string{"./..."}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		found += len(scanReconcileSystemIdentityInstallCaller01(p, observed))
		return nil
	})
	assert.Equal(t, 3, found,
		"RECONCILE-SYSTEM-IDENTITY-INSTALL-CALLER-01 RED fixture must report exactly "+
			"three violations: one non-Loop.process installer reference, one selector-form "+
			"principal setter reference, and one dot-import bare principal setter reference "+
			"outside installSystemProducerIdentity")
}

func scanReconcileSystemIdentityInstallCaller01(
	p *Pass,
	observed *reconcileSystemIdentityObserved,
) []Diagnostic {
	if p.Pkg == nil || p.Pkg.Path() != reconcileSystemIdentityPkg {
		return nil
	}
	var diags []Diagnostic
	diags = append(diags, scanReconcileSystemIdentityInstallerRefs(p, observed)...)
	diags = append(diags, scanReconcileSystemIdentitySetterRefs(p, observed)...)
	return diags
}

func scanReconcileSystemIdentityInstallerRefs(
	p *Pass,
	observed *reconcileSystemIdentityObserved,
) []Diagnostic {
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
			if id.Name != reconcileSystemIdentityInstallFunc {
				return
			}
			fn, ok := p.TypesInfo.Uses[id].(*types.Func)
			if !ok || fn.Pkg() == nil || fn.Pkg().Path() != reconcileSystemIdentityPkg {
				return
			}
			line := p.Fset.Position(id.Pos()).Line
			caller, ok := ResolveEnclosingFunc(p.TypesInfo, file, id)
			if !ok {
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: line,
					Message: "RECONCILE-SYSTEM-IDENTITY-INSTALL-CALLER-01: " +
						"installSystemProducerIdentity is referenced outside any FuncDecl; " +
						"move the reference into (*kernel/reconcile.Loop).process.",
				})
				return
			}
			callerID := caller.FullName()
			observed.installerCallers[callerID] = struct{}{}
			if callerID == reconcileSystemIdentityAllowedInstallRef {
				return
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"RECONCILE-SYSTEM-IDENTITY-INSTALL-CALLER-01: "+
						"installSystemProducerIdentity is referenced from caller %q, which is "+
						"not sanctioned. The reconcile system producer identity must be installed "+
						"only at the Loop.process chokepoint so every Reconcile gets the same "+
						"tenantless system principal boundary.",
					callerID,
				),
			})
		})
	}
	return diags
}

func scanReconcileSystemIdentitySetterRefs(
	p *Pass,
	observed *reconcileSystemIdentityObserved,
) []Diagnostic {
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
			setter, matched := principalSetterIdentName(p.TypesInfo, id)
			if !matched {
				return
			}
			line := p.Fset.Position(id.Pos()).Line
			caller, ok := ResolveEnclosingFunc(p.TypesInfo, file, id)
			if !ok {
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: line,
					Message: fmt.Sprintf(
						"RECONCILE-SYSTEM-IDENTITY-INSTALL-CALLER-01: ctxkeys.%s is referenced "+
							"from package kernel/reconcile outside any FuncDecl; principal ctx-key "+
							"writes in reconcile must live only in installSystemProducerIdentity.",
						setter,
					),
				})
				return
			}
			callerID := caller.FullName()
			if observed.setterCallers[setter] == nil {
				observed.setterCallers[setter] = map[string]struct{}{}
			}
			observed.setterCallers[setter][callerID] = struct{}{}
			if callerID == reconcileSystemIdentityAllowedSetterRef {
				return
			}
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"RECONCILE-SYSTEM-IDENTITY-INSTALL-CALLER-01: ctxkeys.%s is referenced "+
						"from caller %q. The reconcile package may write principal ctx keys only "+
						"inside installSystemProducerIdentity; adding a second writer would bypass "+
						"the Loop.process system-principal chokepoint.",
					setter, callerID,
				),
			})
		})
	}
	return diags
}

func principalSetterIdentName(info *types.Info, id *ast.Ident) (string, bool) {
	fn, ok := info.Uses[id].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != ctxkeysPkgPath {
		return "", false
	}
	if _, isSetter := principalSetterAllowlist[fn.Name()]; !isSetter {
		return "", false
	}
	return fn.Name(), true
}

func staleReconcileSystemIdentityDiags(observed *reconcileSystemIdentityObserved) []Diagnostic {
	var diags []Diagnostic
	if _, seen := observed.installerCallers[reconcileSystemIdentityAllowedInstallRef]; !seen {
		diags = append(diags, Diagnostic{
			Message: fmt.Sprintf(
				"RECONCILE-SYSTEM-IDENTITY-INSTALL-CALLER-01: allowlist entry %q is STALE "+
					"— no live reference to installSystemProducerIdentity observed from that caller.",
				reconcileSystemIdentityAllowedInstallRef,
			),
		})
	}
	setters := append([]string(nil), reconcileSystemIdentitySetters...)
	sort.Strings(setters)
	for _, setter := range setters {
		if _, seen := observed.setterCallers[setter][reconcileSystemIdentityAllowedSetterRef]; seen {
			continue
		}
		diags = append(diags, Diagnostic{
			Message: fmt.Sprintf(
				"RECONCILE-SYSTEM-IDENTITY-INSTALL-CALLER-01: allowlist entry %q for "+
					"ctxkeys.%s is STALE — no live setter reference observed from that caller.",
				reconcileSystemIdentityAllowedSetterRef, setter,
			),
		})
	}
	return diags
}
