package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
var outboxSecurityTopicPattern = regexp.MustCompile(`^(session|user|role|audit)\.`)

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
// an outbox.Entry composite literal whose Topic or EventType string literal
// matches one of the security-sensitive prefixes (session.*, user.*, role.*,
// audit.*) must not set FailurePolicy: outbox.FailurePolicyFailOpen.
//
// This guards the F9 per-entry failure policy contract: Cells default to
// DirectPublishFailClosed; individual entries opt into FailOpen for
// observational sinks. Security / audit-chain events must not opt in —
// dropping them silently loses the audit invariant.
//
// Scope: scans all non-test .go files under cells/** (both Cell top-level
// packages and slices/internal service packages). Excludes _test.go files
// so fixtures can exercise the negative path without tripping the rule.
//
// ref: kubernetes apiserver/pkg/audit Backend.FailurePolicy (Ignore/Fail)
// — failure policy is per-event category, not per-sink.
// ref: ThreeDotsLabs/watermill message/router/middleware/retry.go —
// per-message disposition rather than publisher-level switch.
func TestSecurityTopicsDoNotOptInFailOpen(t *testing.T) {
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
		"security-sensitive topics (session.*, user.*, role.*, audit.*) must not "+
			"set FailurePolicy: outbox.FailurePolicyFailOpen on the outbox.Entry "+
			"literal; drop silently = lose audit invariant. Leave FailurePolicy "+
			"unset (= Default, falls through to Cell ctor default = FailClosed).")
}

func checkOutboxTopicFailOpenRule(root string) ([]outboxTopicViolation, error) {
	files, err := findCellProductionGoFiles(root)
	if err != nil {
		return nil, err
	}
	var violations []outboxTopicViolation
	for _, file := range files {
		fileViolations, err := scanOutboxTopicFailOpen(root, file)
		if err != nil {
			return nil, err
		}
		violations = append(violations, fileViolations...)
	}
	return violations, nil
}

// findCellProductionGoFiles walks cells/** for production .go files
// (excluding _test.go + vendor + .git).
func findCellProductionGoFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(filepath.Join(root, "cells"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files, err
}

// scanOutboxTopicFailOpen parses a single .go file and returns any
// composite-literal violations of OUTBOX-TOPIC-FAILOPEN-01.
func scanOutboxTopicFailOpen(root, path string) ([]outboxTopicViolation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	rel = filepath.ToSlash(rel)

	var violations []outboxTopicViolation
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if !isOutboxEntryLiteral(lit) {
			return true
		}
		topic, topicOK := extractStringField(lit, outboxTopicEntryField)
		eventType, eventTypeOK := extractStringField(lit, outboxTopicEventTypeField)

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
			File:    rel,
			Line:    fset.Position(lit.Pos()).Line,
			Message: fmt.Sprintf("outbox.Entry for topic %q opts into FailurePolicyFailOpen; security/audit events must remain FailClosed (leave FailurePolicy unset)", matched),
		})
		return true
	})
	return violations, nil
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

// extractStringField returns the constant-string value for the named field
// of a composite literal, if present. Returns ok=false when the field is
// missing or its value is not a string literal.
func extractStringField(lit *ast.CompositeLit, fieldName string) (string, bool) {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != fieldName {
			continue
		}
		bl, ok := kv.Value.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return "", false
		}
		unquoted, err := strconv.Unquote(bl.Value)
		if err != nil {
			return "", false
		}
		return unquoted, true
	}
	return "", false
}

// TestSecurityTopicsDoNotOptInFailOpen_RegressionFixture asserts that the
// scanner actually flags a bad pattern. This complements the repo-wide
// scan (which reports zero violations today) by proving the rule retains
// teeth — a silent 0-violation run on an empty ruleset would pass too.
func TestSecurityTopicsDoNotOptInFailOpen_RegressionFixture(t *testing.T) {
	fixtures := map[string]struct {
		src       string
		wantMatch bool
	}{
		"session_with_failopen_is_flagged": {
			src: `package fixture
import "github.com/ghbvf/gocell/kernel/outbox"
var _ = outbox.Entry{
	Topic:         "session.created.v1",
	EventType:     "session.created.v1",
	ID:            "x",
	FailurePolicy: outbox.FailurePolicyFailOpen,
}
`,
			wantMatch: true,
		},
		"audit_event_type_with_failopen_flagged": {
			src: `package fixture
import "github.com/ghbvf/gocell/kernel/outbox"
var _ = outbox.Entry{
	EventType:     "audit.appended.v1",
	ID:            "x",
	FailurePolicy: outbox.FailurePolicyFailOpen,
}
`,
			wantMatch: true,
		},
		"session_with_failclosed_passes": {
			src: `package fixture
import "github.com/ghbvf/gocell/kernel/outbox"
var _ = outbox.Entry{
	Topic:         "session.created.v1",
	ID:            "x",
	FailurePolicy: outbox.FailurePolicyFailClosed,
}
`,
			wantMatch: false,
		},
		"session_with_default_policy_passes": {
			src: `package fixture
import "github.com/ghbvf/gocell/kernel/outbox"
var _ = outbox.Entry{
	Topic: "session.created.v1",
	ID:    "x",
}
`,
			wantMatch: false,
		},
		"non_security_topic_with_failopen_passes": {
			src: `package fixture
import "github.com/ghbvf/gocell/kernel/outbox"
var _ = outbox.Entry{
	Topic:         "metric.recorded.v1",
	ID:            "x",
	FailurePolicy: outbox.FailurePolicyFailOpen,
}
`,
			wantMatch: false,
		},
	}

	for name, tc := range fixtures {
		tc := tc
		t.Run(name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, name+".go", tc.src, parser.SkipObjectResolution)
			require.NoError(t, err)
			var violations []outboxTopicViolation
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				if !isOutboxEntryLiteral(lit) {
					return true
				}
				topic, topicOK := extractStringField(lit, outboxTopicEntryField)
				eventType, eventTypeOK := extractStringField(lit, outboxTopicEventTypeField)
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
					File:    name + ".go",
					Line:    fset.Position(lit.Pos()).Line,
					Message: matched,
				})
				return true
			})
			if tc.wantMatch {
				assert.NotEmpty(t, violations, "fixture %q should have triggered the rule", name)
			} else {
				assert.Empty(t, violations, "fixture %q should not have triggered the rule, got: %v", name, violations)
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
