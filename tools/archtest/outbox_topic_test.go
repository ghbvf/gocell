package archtest

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	kerneloutbox "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

const (
	outboxTopicRuleFailOpen         = "OUTBOX-TOPIC-FAILOPEN-01_security_topics_must_not_opt_in_fail_open"
	outboxTopicForbiddenPolicyField = "FailurePolicy"
	outboxTopicEntryField           = "Topic"
	outboxTopicEventTypeField       = "EventType"
	outboxEntryTypeName             = "Entry"
	outboxFailurePolicyTypeName     = "FailurePolicy"
	gocellOutboxPackagePath         = "github.com/ghbvf/gocell/kernel/outbox"
	fixtureOutboxPackagePath        = "fixturetest/outbox"
)

var outboxFailOpenConstValues = map[string]int64{
	gocellOutboxPackagePath:  int64(kerneloutbox.FailurePolicyFailOpen),
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

type outboxTopicViolation struct {
	Rule    string
	File    string
	Line    int
	Message string
}

func (v outboxTopicViolation) String() string {
	return fmt.Sprintf("%s: %s:%d: %s", v.Rule, v.File, v.Line, v.Message)
}

// TestSecurityTopicsDoNotOptInFailOpen enforces OUTBOX-TOPIC-FAILOPEN-01:
// an outbox.Entry composite literal whose Topic or EventType string constant
// matches one of the security-sensitive prefixes (session.*, user.*, role.*,
// audit.* and their event.* contract forms) must not set FailurePolicy:
// outbox.FailurePolicyFailOpen.
//
// The scanner uses go/types TypesInfo to evaluate Topic/EventType field
// expressions, covering BasicLit, same-package const Idents, and cross-package
// SelectorExprs (e.g. dto.TopicSessionCreated). go/types' built-in constant
// folding provides full intra-module const propagation without manual SSA.
//
// Scope: scans all production non-test .go files via packages.Load.
//
// ref: kubernetes apiserver/pkg/audit Backend.FailurePolicy (Ignore/Fail)
// ref: ThreeDotsLabs/watermill message/router/middleware/retry.go
func TestSecurityTopicsDoNotOptInFailOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode (loads production packages module-wide, ~5-10s)")
	}
	root := findModuleRoot(t)

	violations, err := checkOutboxTopicFailOpenRule(root)
	require.NoError(t, err)

	if len(violations) > 0 {
		t.Logf("Found %d OUTBOX-TOPIC-FAILOPEN-01 violation(s):", len(violations))
		for _, v := range violations {
			t.Logf("  %s", v)
		}
	}

	assert.Empty(t, violations,
		"security-sensitive topics (session.*, user.*, role.*, audit.*, event.* security contracts) "+
			"must not set FailurePolicy: outbox.FailurePolicyFailOpen on the outbox.Entry "+
			"literal; drop silently = lose audit invariant. Leave FailurePolicy "+
			"unset (= Default, falls through to Cell ctor default = FailClosed).")
}

// checkOutboxTopicFailOpenRule loads module packages with full type info and
// scans production Go files for OUTBOX-TOPIC-FAILOPEN-01 violations.
func checkOutboxTopicFailOpenRule(root string) ([]outboxTopicViolation, error) {
	r, err := typeseval.SharedResolver(root, false, nil, prodscan.Patterns(root)...)
	if err != nil {
		return nil, err
	}
	var violations []outboxTopicViolation
	for _, p := range r.Packages() {
		pkgViolations, err := scanPackage(root, p)
		if err != nil {
			return nil, err
		}
		violations = append(violations, pkgViolations...)
	}
	return violations, nil
}

// scanPackage scans all non-test Go files in a loaded package for violations.
// packages.Package.Syntax is aligned with GoFiles via Fset.Position.
func scanPackage(root string, p *packages.Package) ([]outboxTopicViolation, error) {
	var violations []outboxTopicViolation
	for _, file := range p.Syntax {
		absPath := p.Fset.Position(file.Pos()).Filename
		if strings.HasSuffix(absPath, "_test.go") {
			continue
		}
		rel, err := filepath.Rel(root, absPath)
		if err != nil {
			return nil, fmt.Errorf("filepath.Rel: %w", err)
		}
		rel = filepath.ToSlash(rel)
		if skipOutboxTopicProductionScan(rel) {
			continue
		}
		violations = append(violations, scanOutboxTopicFailOpenAST(p.Fset, file, rel, p)...)
	}
	return violations, nil
}

// scanOutboxTopicFailOpenAST is the core AST-matching routine. Given a parsed
// file, fileset, and the owning package (for TypesInfo lookup), it returns
// every outbox.Entry composite literal that opts into FailurePolicyFailOpen
// with a Topic or EventType matching the security-sensitive prefix regex.
//
// Topic/EventType field values are evaluated via typeseval.EvaluateConstString,
// covering BasicLit, same-package const Ident, and cross-package SelectorExpr.
func scanOutboxTopicFailOpenAST(fset *token.FileSet, file *ast.File, fileLabel string, pkg *packages.Package) []outboxTopicViolation {
	var violations []outboxTopicViolation
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if !isOutboxEntryLiteral(pkg, lit) {
			return true
		}
		policy := extractFailurePolicy(pkg, lit)
		if policy.safe() {
			return true
		}
		topic := extractStringField(pkg, lit, outboxTopicEntryField)
		eventType := extractStringField(pkg, lit, outboxTopicEventTypeField)
		route := effectiveOutboxRoute(topic, eventType)

		switch {
		case route.ok && outboxSecurityTopicPattern.MatchString(route.value):
			violations = append(violations, outboxTopicViolation{
				Rule:    outboxTopicRuleFailOpen,
				File:    fileLabel,
				Line:    fset.Position(lit.Pos()).Line,
				Message: outboxPolicyViolationMessage(policy, route.value),
			})
			return true
		case route.unknown() || !route.present:
			violations = append(violations, outboxTopicViolation{
				Rule:    outboxTopicRuleFailOpen,
				File:    fileLabel,
				Line:    fset.Position(lit.Pos()).Line,
				Message: outboxUnknownRouteViolationMessage(policy),
			})
			return true
		default:
			return true
		}
	})
	return violations
}

// isOutboxEntryLiteral matches real kernel/outbox.Entry composite literals by
// type identity. Import aliases and type aliases are resolved by go/types;
// unrelated Entry structs are rejected even when they share field names.
func isOutboxEntryLiteral(pkg *packages.Package, lit *ast.CompositeLit) bool {
	if pkg.TypesInfo == nil || lit.Type == nil {
		return false
	}
	tv, ok := pkg.TypesInfo.Types[lit.Type]
	if !ok {
		return false
	}
	return isOutboxNamedType(tv.Type, outboxEntryTypeName)
}

type outboxTopicFieldValue struct {
	present bool
	ok      bool
	value   string
}

func (f outboxTopicFieldValue) unknown() bool {
	return f.present && !f.ok
}

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
// named field of a composite literal, evaluated via typeseval.EvaluateConstString.
// Covers BasicLit, same-package const Ident, and cross-package SelectorExpr.
// Returns ok=false when the field is missing or its value is not a constant string.
func extractStringField(pkg *packages.Package, lit *ast.CompositeLit, fieldName string) outboxTopicFieldValue {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != fieldName {
			continue
		}
		value, ok := typeseval.EvaluateConstString(pkg.TypesInfo, kv.Value)
		return outboxTopicFieldValue{present: true, ok: ok, value: value}
	}
	return outboxTopicFieldValue{}
}

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

func outboxPolicyViolationMessage(policy outboxFailurePolicyStatus, topic string) string {
	if policy == outboxPolicyUnknown {
		return fmt.Sprintf(
			"outbox.Entry for topic %q uses non-constant FailurePolicy;"+
				" security/audit events must statically remain FailClosed", topic)
	}
	return fmt.Sprintf(
		"outbox.Entry for topic %q opts into FailurePolicyFailOpen;"+
			" security/audit events must remain FailClosed (leave FailurePolicy unset)", topic)
}

func outboxUnknownRouteViolationMessage(policy outboxFailurePolicyStatus) string {
	if policy == outboxPolicyUnknown {
		return "outbox.Entry uses non-constant FailurePolicy and Topic/EventType is not statically known;" +
			" security/audit fail-open policy must be statically ruled out"
	}
	return "outbox.Entry opts into FailurePolicyFailOpen but Topic/EventType is not statically known;" +
		" fail-open requires a statically non-security topic"
}

// TestSecurityTopicsDoNotOptInFailOpen_RegressionFixtures asserts that the
// scanner correctly flags (or passes) each fixture scenario. The fixture set
// uses real Go packages under testdata/topic_const_fixtures/ loaded via
// packages.Load to exercise the full type-checking path.
//
// Cases:
//   - basicliteral: BasicLit "session.created.v1" + FailOpen -> flagged
//   - eventprefixed: BasicLit "event.session.created.v1" + FailOpen -> flagged
//   - samepackage_const: Ident sessionTopic (= "session.created.v1") + FailOpen -> flagged
//   - crosspackage_dto: SelectorExpr dto.TopicSessionCreated + FailOpen -> flagged
//   - crosspackage_event_dto: SelectorExpr dto.TopicSessionCreated + event. prefix + FailOpen -> flagged
//   - samepackage_failclosed: same-package const + FailClosed → not flagged
//   - nonsecurity_metric: non-security topic + FailOpen -> not flagged
func TestSecurityTopicsDoNotOptInFailOpen_RegressionFixtures(t *testing.T) {
	fixturesRoot := filepath.Join(findArchTestDir(t), "testdata", "topic_const_fixtures")

	cases := []struct {
		pattern   string
		wantMatch bool
	}{
		{"./basicliteral_session_failopen", true},
		{"./basicliteral_event_session_failopen", true},
		{"./samepackage_const_session_failopen", true},
		{"./crosspackage_dto_session_failopen/consumer", true},
		{"./crosspackage_event_dto_session_failopen/consumer", true},
		{"./samepackage_const_session_failclosed", false},
		{"./nonsecurity_metric_failopen_passes", false},
		// Non-session security topics (audit.*, user.*, role.*).
		{"./basicliteral_audit_failopen", true},
		// Non-security topic (config.*) with FailOpen — rule must not fire.
		{"./basicliteral_config_failopen_passes", false},
		// EventType-only path: Topic absent, EventType is a same-package const.
		{"./eventtype_only_const_audit_failopen", true},
		// Cross-package const for a non-session security topic (audit.*).
		{"./crosspackage_audit_dto/consumer", true},
		// Import alias must not affect outbox.Entry identity.
		{"./import_alias_entry_session_failopen", true},
		// Type aliases to outbox.Entry still have outbox.Entry identity.
		{"./entry_type_alias_session_failopen", true},
		// Local and cross-package aliases to the fail-open const are still fail-open.
		{"./local_failopen_alias_session", true},
		{"./crosspackage_failopen_alias/consumer", true},
		// Dynamic policy on a security route is fail-closed.
		{"./dynamic_failopen_policy_session", true},
		// Non-outbox Entry types must not be matched by name alone.
		{"./unrelated_entry_failopen_passes", false},
		// Fail-open entries with dynamic routing topics fail closed.
		{"./dynamic_topic_failopen", true},
		// Empty Topic falls back to EventType, matching outbox.Entry.RoutingTopic.
		{"./topic_empty_event_session_failopen", true},
		// Topic takes precedence over EventType, matching outbox.Entry.RoutingTopic.
		{"./topic_precedence_dynamic_event_passes", false},
	}

	for _, c := range cases {
		t.Run(c.pattern, func(t *testing.T) {
			r, err := typeseval.SharedResolver(fixturesRoot, false, nil, c.pattern)
			require.NoError(t, err, "load fixture package %s", c.pattern)

			var violations []outboxTopicViolation
			for _, p := range r.Packages() {
				for _, file := range p.Syntax {
					absPath := p.Fset.Position(file.Pos()).Filename
					if strings.HasSuffix(absPath, "_test.go") {
						continue
					}
					rel, err := filepath.Rel(fixturesRoot, absPath)
					require.NoError(t, err)
					rel = filepath.ToSlash(rel)
					violations = append(violations, scanOutboxTopicFailOpenAST(p.Fset, file, rel, p)...)
				}
			}

			if c.wantMatch {
				assert.NotEmpty(t, violations, "fixture %q should trigger OUTBOX-TOPIC-FAILOPEN-01", c.pattern)
			} else {
				assert.Empty(t, violations, "fixture %q should not trigger rule, got: %v", c.pattern, violations)
			}
		})
	}
}

// extractFailurePolicy classifies the FailurePolicy field. A dynamic policy is
// treated as unknown, and callers fail closed when the route is security-like or
// not statically known.
func extractFailurePolicy(pkg *packages.Package, lit *ast.CompositeLit) outboxFailurePolicyStatus {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != outboxTopicForbiddenPolicyField {
			continue
		}
		if isOutboxFailOpenConst(pkg.TypesInfo, kv.Value) {
			return outboxPolicyKnownFailOpen
		}
		if isKnownOutboxFailurePolicyConst(pkg.TypesInfo, kv.Value) {
			return outboxPolicyKnownOther
		}
		return outboxPolicyUnknown
	}
	return outboxPolicyAbsent
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
	return pkgPath == gocellOutboxPackagePath || pkgPath == fixtureOutboxPackagePath
}

func skipOutboxTopicProductionScan(rel string) bool {
	return strings.HasPrefix(rel, "tools/") ||
		strings.HasPrefix(rel, "tests/") ||
		strings.Contains(rel, "/testdata/") ||
		strings.HasPrefix(rel, "testdata/")
}
