package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

const (
	outboxTopicRuleFailOpen          = "OUTBOX-TOPIC-FAILOPEN-01_security_topics_must_not_opt_in_fail_open"
	outboxTopicForbiddenPolicyIdent  = "FailurePolicyFailOpen"
	outboxTopicForbiddenPolicyField  = "FailurePolicy"
	outboxTopicEntryField            = "Topic"
	outboxTopicEventTypeField        = "EventType"
	outboxTopicEntryCompositeTypeSfx = "outbox.Entry"
)

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
// Scope: scans all non-test .go files under cells/** via packages.Load.
//
// ref: kubernetes apiserver/pkg/audit Backend.FailurePolicy (Ignore/Fail)
// ref: ThreeDotsLabs/watermill message/router/middleware/retry.go
func TestSecurityTopicsDoNotOptInFailOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode (loads ./cells/... module-wide, ~5-10s)")
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

// checkOutboxTopicFailOpenRule loads all cells/ packages with full type info
// and scans every non-test Go file for OUTBOX-TOPIC-FAILOPEN-01 violations.
func checkOutboxTopicFailOpenRule(root string) ([]outboxTopicViolation, error) {
	r, err := typeseval.SharedResolver(root, "./cells/...")
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
		if !isOutboxEntryLiteral(lit) {
			return true
		}
		topic, topicOK := extractStringField(pkg, lit, outboxTopicEntryField)
		eventType, eventTypeOK := extractStringField(pkg, lit, outboxTopicEventTypeField)

		var matched string
		switch {
		case topicOK && outboxSecurityTopicPattern.MatchString(topic):
			matched = topic
		case eventTypeOK && outboxSecurityTopicPattern.MatchString(eventType):
			matched = eventType
		default:
			return true
		}
		if !entryHasFailOpenPolicy(lit) {
			return true
		}
		violations = append(violations, outboxTopicViolation{
			Rule:    outboxTopicRuleFailOpen,
			File:    fileLabel,
			Line:    fset.Position(lit.Pos()).Line,
			Message: fmt.Sprintf("outbox.Entry for topic %q opts into FailurePolicyFailOpen; security/audit events must remain FailClosed (leave FailurePolicy unset)", matched),
		})
		return true
	})
	return violations
}

// isOutboxEntryLiteral matches `outbox.Entry{...}` and its variants
// (e.g. kernel-outbox aliases). Matches type expressions ending in
// `.Entry` from any imported outbox package alias.
func isOutboxEntryLiteral(lit *ast.CompositeLit) bool {
	switch t := lit.Type.(type) {
	case *ast.SelectorExpr:
		// X.Entry where X is some package identifier — require identifier
		// name to contain "outbox" to avoid false positives on unrelated
		// Entry types (e.g. log.Entry).
		if t.Sel.Name != "Entry" {
			return false
		}
		id, ok := t.X.(*ast.Ident)
		if !ok {
			return false
		}
		return strings.Contains(strings.ToLower(id.Name), "outbox")
	case *ast.Ident:
		// Same-package literal `Entry{...}`. Only meaningful inside the
		// outbox package itself — scope excludes kernel/, so this arm is
		// unused in practice but kept for completeness.
		return t.Name == "Entry"
	}
	return false
}

// extractStringField returns the compile-time constant string value for the
// named field of a composite literal, evaluated via typeseval.EvaluateConstString.
// Covers BasicLit, same-package const Ident, and cross-package SelectorExpr.
// Returns ok=false when the field is missing or its value is not a constant string.
func extractStringField(pkg *packages.Package, lit *ast.CompositeLit, fieldName string) (string, bool) {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != fieldName {
			continue
		}
		return typeseval.EvaluateConstString(pkg.TypesInfo, kv.Value)
	}
	return "", false
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
	}

	for _, c := range cases {
		c := c
		t.Run(c.pattern, func(t *testing.T) {
			r, err := typeseval.NewResolver(fixturesRoot, c.pattern)
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

// entryHasFailOpenPolicy checks whether the composite literal sets
// `FailurePolicy: <something referencing FailurePolicyFailOpen>`.
func entryHasFailOpenPolicy(lit *ast.CompositeLit) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != outboxTopicForbiddenPolicyField {
			continue
		}
		switch v := kv.Value.(type) {
		case *ast.Ident:
			if v.Name == outboxTopicForbiddenPolicyIdent {
				return true
			}
		case *ast.SelectorExpr:
			if v.Sel != nil && v.Sel.Name == outboxTopicForbiddenPolicyIdent {
				return true
			}
		}
	}
	return false
}

// findArchTestDir returns the absolute path of the tools/archtest directory,
// used to locate testdata fixtures at test runtime.
func findArchTestDir(t *testing.T) string {
	t.Helper()
	root := findModuleRoot(t)
	return filepath.Join(root, "tools", "archtest")
}
