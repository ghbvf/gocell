// Importable rule bodies for outbox invariants migrated to non-test .go
// (M3 #1302 PR-5) so external Cell repos can import and run them via
// StandardCellRules / RunStandardCellRules.  The dogfood + RED-fixture
// precision gates live in outbox_invariants_test.go.
//
// # OUTBOX-HANDLERESULT-FACTORY-PREFERRED-01
//
// Production code (non-_test.go) must use kernel/outbox factories
// Ack/Requeue/Reject instead of constructing outbox.HandleResult{...}
// composite literals, except for the files in handleResultLiteralAllowlist
// (factories themselves, kernel internal plumbing, shared conformance harness).
//
// External Cell repo semantics: the allowlist entries
// (kernel/outbox/result.go, kernel/outbox/consumer_base.go,
// kernel/outbox/outboxtest/conformance.go) belong to the GoCell platform module
// and never exist in a consumer module, so the allowlist never matches in an
// external repo — the rule degrades to a PURE BAN on HandleResult{} literals,
// which is the intended behavior: business handlers must use the typed
// factories.
//
// # OUTBOX-TOPIC-FAILOPEN-01
//
// An outbox.Entry composite literal whose Topic or EventType string constant
// matches one of the security-sensitive prefixes (session.*, user.*, role.*,
// audit.* and their event.* contract forms) must not set FailurePolicy:
// outbox.FailurePolicyFailOpen.
//
// NOT registered in StandardCellRules (vacuous-external): kernel/outbox.Entry is
// a sealed type — all fields are unexported and a populated outbox.Entry{...}
// composite literal outside kernel/outbox is a COMPILE ERROR
// (OUTBOX-ENTRY-SEALED-CONSTRUCTION-01, kernel/outbox/outbox.go). No production
// package — in GoCell or in any external Cell repo — can construct the literals
// this rule scans for, so the production scan is vacuous everywhere; the rule's
// real coverage is the fixturetest/outbox fake-package fixtures under testdata/.
// It is migrated here for PlatformModulePath parameterization + fork-safety only
// (analogous to SCAFFOLD-LISTENER-MARKER-TYPED-CONST-01), enforced in GoCell via
// the dogfood + fixture sub-tests in outbox_invariants_test.go.
//
// # AI-robust ratings
//
// Both rules are Medium downstream (archtest type-aware caller-allowlist /
// type-identity scan); Hard upstream is not reachable — Go cannot express
// "callers in cells/ must use factory names" without unexporting HandleResult
// (which breaks kernel-internal plumbing), nor can Go prevent an outbox.Entry
// composite literal from setting any public field.
package archtest

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"regexp"
	"sort"
	"strings"
	"testing"

	kerneloutbox "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// Rule ID constants — kept in the non-test file so the CellRule descriptors in
// external.go can reference them without a _test.go import.
const (
	ruleOutboxHandleResultFactoryPreferred01 = "OUTBOX-HANDLERESULT-FACTORY-PREFERRED-01"
	ruleOutboxTopicFailopen01                = "OUTBOX-TOPIC-FAILOPEN-01"
)

// outboxKernelPkgPath is the import path of kernel/outbox, derived from
// PlatformModulePath so a module rename / /v2 bump updates one place.
const outboxKernelPkgPath = PlatformModulePath + "/kernel/outbox"

// fixtureOutboxPackagePath is the fake outbox package path used by the
// topic-failopen fixture packages under testdata/topic_const_fixtures/.
// Declared here (non-test) because outboxFailOpenConstValues (also non-test)
// references it.  The fixture sub-tests in _test.go also reference it.
const fixtureOutboxPackagePath = "fixturetest/outbox"

// Per-field names used by the TOPIC-FAILOPEN scanner.
const (
	outboxTopicRuleFailOpen         = "OUTBOX-TOPIC-FAILOPEN-01_security_topics_must_not_opt_in_fail_open"
	outboxTopicForbiddenPolicyField = "FailurePolicy"
	outboxTopicEntryField           = "Topic"
	outboxTopicEventTypeField       = "EventType"
	outboxEntryTypeName             = "Entry"
	outboxFailurePolicyTypeName     = "FailurePolicy"
)

// outboxFailOpenConstValues maps outbox package paths (real + fixture) to the
// integer value of their FailurePolicyFailOpen constant.  Kept in the non-test
// file so the scanner helper functions can reference it without a _test.go
// dependency.
var outboxFailOpenConstValues = map[string]int64{
	outboxKernelPkgPath:      int64(kerneloutbox.FailurePolicyFailOpen),
	fixtureOutboxPackagePath: 1,
}

// outboxSecurityTopicPattern matches topics that carry security or audit-chain
// semantics. Events matching these prefixes must not opt into
// FailurePolicyFailOpen — dropping them silently removes audit/security
// signals from downstream consumers.
//
// ref: kubernetes apiserver/pkg/audit — audit events default to Fail policy;
// operators opt into Ignore per backend, not per event type.
var outboxSecurityTopicPattern = regexp.MustCompile(`^(event\.)?(session|user|role|audit)\.`)

// CheckOutboxHandleResultFactoryPreferred01 enforces
// OUTBOX-HANDLERESULT-FACTORY-PREFERRED-01 downstream: production code
// (non-_test.go) in the running module must not construct
// outbox.HandleResult{...} composite literals outside the files in
// handleResultLiteralAllowlist.
//
// It scans the running module's production code (Production → findModuleRoot),
// covering tag-gated files via cfg.BuildTags, and returns the diagnostics it
// observes.  It does NOT call t.Errorf — the caller (Report / RunStandardCellRules)
// does that.
//
// scanner.Canonical deduplicates the overlap between the default-tag pass and
// the tagged pass (identical to CheckScaffoldDerivedForceOverwrite's contract).
func CheckOutboxHandleResultFactoryPreferred01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	out := Run(t, Production(TypedOpts{}), collectHandleResultLiteralViolations)
	if len(cfg.BuildTags) > 0 {
		out = append(out, Run(t, Production(TypedOpts{Tags: cfg.BuildTags}), collectHandleResultLiteralViolations)...)
	}
	return scanner.Canonical(out)
}

// collectHandleResultLiteralViolations is the per-Pass scanner shared by the
// production Check and the fixture precision gate (no parallel rule body).
func collectHandleResultLiteralViolations(p *Pass) []Diagnostic {
	if p.TypesInfo == nil || p.Fset == nil {
		return nil
	}
	var hits []string
	for _, file := range p.Files {
		rel := p.Rel(file)
		if _, ok := handleResultLiteralAllowlist[rel]; ok {
			continue
		}
		hits = append(hits, scanForHandleResultLiterals(p.Fset, p.TypesInfo, file, rel, outboxKernelPkgPath)...)
	}
	sort.Strings(hits)
	var out []Diagnostic
	for _, h := range hits {
		out = append(out, Diagnostic{
			Message: fmt.Sprintf("OUTBOX-HANDLERESULT-FACTORY-PREFERRED-01: %s — use outbox.Ack() / "+
				"outbox.Requeue(err) / outbox.Reject(err) instead of constructing the "+
				"struct literal; if you genuinely need ProcessReason or SettlementObservers, "+
				"extend handleResultLiteralAllowlist with a code-comment justification", h),
		})
	}
	return out
}

// CheckOutboxTopicFailopen01 enforces OUTBOX-TOPIC-FAILOPEN-01: an
// outbox.Entry composite literal whose Topic or EventType string constant
// matches one of the security-sensitive prefixes must not set FailurePolicy:
// outbox.FailurePolicyFailOpen.
//
// It scans the running module's production code (Production → findModuleRoot),
// covering tag-gated files via cfg.BuildTags, and returns the diagnostics it
// observes.  It does NOT call t.Errorf — the caller (Report /
// RunStandardCellRules) does that.
//
// External Cell repo: external Cells use platform outbox at the stable
// PlatformModulePath import path; the type-identity check via go/types
// resolves regardless of import alias — the rule applies uniformly.
func CheckOutboxTopicFailopen01(t *testing.T, cfg ConfigForExternalCell) []Diagnostic {
	t.Helper()
	out := Run(t, Production(TypedOpts{}), collectOutboxTopicFailopenViolations)
	if len(cfg.BuildTags) > 0 {
		out = append(out, Run(t, Production(TypedOpts{Tags: cfg.BuildTags}), collectOutboxTopicFailopenViolations)...)
	}
	return scanner.Canonical(out)
}

// collectOutboxTopicFailopenViolations is the per-Pass scanner shared by the
// production Check and the fixture sub-tests.
func collectOutboxTopicFailopenViolations(p *Pass) []Diagnostic {
	if p.TypesInfo == nil || p.Fset == nil {
		return nil
	}
	var out []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if skipOutboxTopicProductionScan(rel) {
			continue
		}
		for _, v := range scanOutboxTopicFailOpenAST(p.Fset, file, rel, p.TypesInfo) {
			out = append(out, Diagnostic{
				Rel:     v.File,
				Line:    v.Line,
				Message: fmt.Sprintf("%s: %s", v.Rule, v.Message),
			})
		}
	}
	return out
}

// handleResultLiteralAllowlist lists the production (non-_test.go) files that
// may construct outbox.HandleResult{...} composite literals. Every other
// production file must use the Ack/Requeue/Reject factories from
// kernel/outbox/result.go.
//
// Why these three:
//   - kernel/outbox/result.go         — defines the factories themselves.
//   - kernel/outbox/consumer_base.go  — kernel internal retry/settle plumbing
//     constructs HandleResult with ProcessReason / SettlementObservers, which
//     the factories do not expose (see eventbus.md "回落字面量").
//   - kernel/outbox/outboxtest/conformance.go — shared conformance harness;
//     non-_test.go by package convention but used only from test binaries.
//
// Adding a new entry requires the justification to live **next to the map
// entry below as a Go comment** (not in the file being scanned, since that
// file is the subject of the rule).
var handleResultLiteralAllowlist = map[string]struct{}{
	"kernel/outbox/result.go":                 {},
	"kernel/outbox/consumer_base.go":          {},
	"kernel/outbox/outboxtest/conformance.go": {},
}

// outboxTopicViolation is a single OUTBOX-TOPIC-FAILOPEN-01 finding.
type outboxTopicViolation struct {
	Rule    string
	File    string
	Line    int
	Message string
}

func (v outboxTopicViolation) String() string {
	return fmt.Sprintf("%s: %s:%d: %s", v.Rule, v.File, v.Line, v.Message)
}

// outboxTopicFieldValue holds the result of evaluating a Topic/EventType field.
type outboxTopicFieldValue struct {
	present bool
	ok      bool
	value   string
}

func (f outboxTopicFieldValue) unknown() bool {
	return f.present && !f.ok
}

// outboxFailurePolicyStatus classifies what FailurePolicy is set to.
type outboxFailurePolicyStatus int

const (
	outboxPolicyAbsent outboxFailurePolicyStatus = iota
	outboxPolicyKnownOther
	outboxPolicyKnownFailOpen
	outboxPolicyUnknown
)

func (s outboxFailurePolicyStatus) safe() bool {
	return s == outboxPolicyAbsent || s == outboxPolicyKnownOther
}

// scanOutboxTopicFailOpenAST is the core AST-matching routine. Given a parsed
// file, fileset, types.Info and a file label, it returns every outbox.Entry
// composite literal that opts into FailurePolicyFailOpen with a Topic or
// EventType matching the security-sensitive prefix regex.
//
// Topic/EventType field values are evaluated via EvaluateConstString,
// covering BasicLit, same-package const Ident, and cross-package SelectorExpr.
func scanOutboxTopicFailOpenAST(fset *token.FileSet, file *ast.File, fileLabel string, info *types.Info) []outboxTopicViolation {
	var violations []outboxTopicViolation
	EachInSubtree[ast.CompositeLit](file, func(lit *ast.CompositeLit) {
		if !isOutboxEntryLiteral(info, lit) {
			return
		}
		policy := extractFailurePolicy(info, lit)
		if policy.safe() {
			return
		}
		topic := extractStringField(info, lit, outboxTopicEntryField)
		eventType := extractStringField(info, lit, outboxTopicEventTypeField)
		route := effectiveOutboxRoute(topic, eventType)

		switch {
		case route.ok && outboxSecurityTopicPattern.MatchString(route.value):
			violations = append(violations, outboxTopicViolation{
				Rule:    outboxTopicRuleFailOpen,
				File:    fileLabel,
				Line:    fset.Position(lit.Pos()).Line,
				Message: outboxPolicyViolationMessage(policy, route.value),
			})
		case route.unknown() || !route.present:
			violations = append(violations, outboxTopicViolation{
				Rule:    outboxTopicRuleFailOpen,
				File:    fileLabel,
				Line:    fset.Position(lit.Pos()).Line,
				Message: outboxUnknownRouteViolationMessage(policy),
			})
		}
	})
	return violations
}

// isOutboxEntryLiteral matches real kernel/outbox.Entry composite literals by
// type identity. Import aliases and type aliases are resolved by go/types;
// unrelated Entry structs are rejected even when they share field names.
func isOutboxEntryLiteral(info *types.Info, lit *ast.CompositeLit) bool {
	if info == nil || lit.Type == nil {
		return false
	}
	tv, ok := info.Types[lit.Type]
	if !ok {
		return false
	}
	return isOutboxNamedType(tv.Type, outboxEntryTypeName)
}

// effectiveOutboxRoute returns the routing key: Topic takes precedence; if
// Topic is absent or empty, EventType is used.
func effectiveOutboxRoute(topic, eventType outboxTopicFieldValue) outboxTopicFieldValue {
	if topic.present {
		if topic.ok && topic.value == "" {
			return eventType
		}
		return topic
	}
	return eventType
}

// extractStringField returns the compile-time constant string value for the
// named field of a composite literal, evaluated via EvaluateConstString.
// Covers BasicLit, same-package const Ident, and cross-package SelectorExpr.
// Returns ok=false when the field is missing or its value is not a constant string.
//
// EachInChildren visits only lit's direct children, so a same-named field
// nested inside a sub-struct (e.g. `Spec: SubSpec{Topic:"a"}`) does not
// pollute lit's reading.
func extractStringField(info *types.Info, lit *ast.CompositeLit, fieldName string) outboxTopicFieldValue {
	kv, ok := FindFirstChild[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) bool {
		id, isID := kv.Key.(*ast.Ident)
		return isID && id.Name == fieldName
	})
	if !ok {
		return outboxTopicFieldValue{}
	}
	value, vok := EvaluateConstString(info, kv.Value)
	return outboxTopicFieldValue{present: true, ok: vok, value: value}
}

func outboxPolicyViolationMessage(policy outboxFailurePolicyStatus, topic string) string {
	if policy == outboxPolicyUnknown {
		return fmt.Sprintf(
			"outbox.Entry for topic %q uses non-constant FailurePolicy;"+
				" security/audit events must statically remain FailClosed", topic,
		)
	}
	return fmt.Sprintf(
		"outbox.Entry for topic %q opts into FailurePolicyFailOpen;"+
			" security/audit events must remain FailClosed (leave FailurePolicy unset)", topic,
	)
}

func outboxUnknownRouteViolationMessage(policy outboxFailurePolicyStatus) string {
	if policy == outboxPolicyUnknown {
		return "outbox.Entry uses non-constant FailurePolicy and Topic/EventType is not statically known;" +
			" security/audit fail-open policy must be statically ruled out"
	}
	return "outbox.Entry opts into FailurePolicyFailOpen but Topic/EventType is not statically known;" +
		" fail-open requires a statically non-security topic"
}

// extractFailurePolicy classifies the FailurePolicy field. A dynamic policy is
// treated as unknown, and callers fail closed when the route is security-like or
// not statically known.
//
// EachInChildren visits only lit's direct children, so a FailurePolicy buried
// inside a nested struct is not hoisted to lit's level.
func extractFailurePolicy(info *types.Info, lit *ast.CompositeLit) outboxFailurePolicyStatus {
	kv, ok := FindFirstChild[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) bool {
		key, isID := kv.Key.(*ast.Ident)
		return isID && key.Name == outboxTopicForbiddenPolicyField
	})
	if !ok {
		return outboxPolicyAbsent
	}
	if isOutboxFailOpenConst(info, kv.Value) {
		return outboxPolicyKnownFailOpen
	} else if isKnownOutboxFailurePolicyConst(info, kv.Value) {
		return outboxPolicyKnownOther
	}
	return outboxPolicyUnknown
}

func isOutboxFailOpenConst(info *types.Info, expr ast.Expr) bool {
	if info == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok || tv.Value == nil {
		return false
	}
	pkgPath, ok := outboxNamedTypePackagePath(tv.Type, outboxFailurePolicyTypeName)
	if !ok {
		return false
	}
	failOpenValue, ok := outboxFailOpenConstValues[pkgPath]
	if !ok {
		return false
	}
	value, exact := constant.Int64Val(constant.ToInt(tv.Value))
	return exact && value == failOpenValue
}

func isKnownOutboxFailurePolicyConst(info *types.Info, expr ast.Expr) bool {
	if info == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok || tv.Value == nil {
		return false
	}
	_, ok = outboxNamedTypePackagePath(tv.Type, outboxFailurePolicyTypeName)
	return ok
}

func isOutboxNamedType(t types.Type, name string) bool {
	_, ok := outboxNamedTypePackagePath(t, name)
	return ok
}

func outboxNamedTypePackagePath(t types.Type, name string) (string, bool) {
	if t == nil {
		return "", false
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return "", false
	}
	obj := named.Obj()
	if obj == nil || obj.Name() != name || obj.Pkg() == nil {
		return "", false
	}
	pkgPath := obj.Pkg().Path()
	return pkgPath, isOutboxPackagePath(pkgPath)
}

func isOutboxPackagePath(pkgPath string) bool {
	return pkgPath == outboxKernelPkgPath || pkgPath == fixtureOutboxPackagePath
}

func skipOutboxTopicProductionScan(rel string) bool {
	return strings.HasPrefix(rel, "tools/") ||
		strings.HasPrefix(rel, "tests/") ||
		strings.Contains(rel, "/testdata/") ||
		strings.HasPrefix(rel, "testdata/")
}

// scanForHandleResultLiterals scans file for HandleResult composite literals.
// Returns "<rel>:<line>" strings. Files that neither import kernel/outbox nor
// declare package outbox produce no hits. Type-aware via info.
//
// Coverage:
//   - qualified literal `outbox.HandleResult{}` — *ast.SelectorExpr resolved
//     via info.Uses[tn.Sel].(*types.TypeName); covers renamed imports
//     authoritatively (the type's owning package is reported regardless of
//     local alias).
//   - bare-Ident literal `HandleResult{}` — *ast.Ident resolved via
//     info.Uses[tn].(*types.TypeName); covers BOTH same-package use (file is
//     in package outbox) AND dot-imported use (`import . "outbox"`). The
//     latter closes the prior path A.3 bypass — symmetric with the PR-SH1
//     caller-side migration to typeseval.ResolvePackageRef for function refs.
func scanForHandleResultLiterals(fset *token.FileSet, info *types.Info, file *ast.File, rel, outboxImportPath string) []string {
	var hits []string
	EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
		var id *ast.Ident
		switch tn := cl.Type.(type) {
		case *ast.SelectorExpr:
			if tn.Sel == nil || tn.Sel.Name != "HandleResult" {
				return
			}
			id = tn.Sel
		case *ast.Ident:
			if tn.Name != "HandleResult" {
				return
			}
			id = tn
		default:
			return
		}
		obj, ok := info.Uses[id].(*types.TypeName)
		if !ok || obj.Pkg() == nil || obj.Pkg().Path() != outboxImportPath {
			return
		}
		pos := fset.Position(cl.Pos())
		hits = append(hits, fmt.Sprintf("%s:%d: HandleResult{} literal (resolved to %s)", rel, pos.Line, outboxImportPath))
	})
	return hits
}
