// Package archtest_test — contract_schema_naming_invariants_test.go
//
// File invariants:
//   - INVARIANT: CONTRACT-WIRE-FIELD-CAMELCASE-01
//   - INVARIANT: CONTRACT-PAGINATION-PARAM-LIMIT-01
//   - INVARIANT: CONTRACT-EVENT-IDEMPOTENCY-KEY-EVENTID-01
//
// Lock the camelCase wire-field convention declared in CLAUDE.md §"Go 编码规范"
// ("DB 字段 snake_case，JSON/Query/Path camelCase") at the contract schema
// truth source so future contracts cannot regress to snake_case.
//
// Scope: only files under `contracts/` that describe HTTP/event wire shape.
// DB column names, slog log keys, and outbox in-process metadata keys are
// outside scope (CLAUDE.md mandates snake_case there).
//
// AI-rebust: Hard — value-level check on parsed YAML/JSON; no string-anchor
// escape; new contract files automatically scanned via EachContentFile. See
// .claude/rules/gocell/ai-collab.md §"载体决策原则" item 3
// (元数据 / YAML 派生 → archtest.EachContentFile + 解析).
//
// ref: J-04 (#632) CONTRACT-SCHEMA-NAMING-NORMALIZE; J-03 (#695) co-shipped.
// ref: docs/architecture/202605211200-adr-pre-v1.0-direct-v1-evolution.md.

package archtest

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// camelCasePropertyRE matches a valid camelCase wire field name:
// starts with lowercase letter, followed by alphanumeric.
// Examples accepted: id, userId, eventId, nextCursor, isoTimestamp.
// Examples rejected: event_id (snake_case), EventID (PascalCase), _internal,
// 2fa (leading digit), nameWith-dash, name.with.dot.
var camelCasePropertyRE = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`)

// forbiddenPaginationParams are query parameter names disallowed on any HTTP
// contract — the project convention is keyset pagination via (cursor, limit).
// Adding offset/page-style pagination is a contract change that must be
// discussed and added to the canonical pair before this list is relaxed.
var forbiddenPaginationParams = map[string]struct{}{
	"pageSize":  {},
	"page":      {},
	"pageNum":   {},
	"pageIndex": {},
	"size":      {},
	"offset":    {},
}

// canonicalEventIdempotencyKey is the wire field name every event contract's
// idempotencyKey must reference. It identifies the per-event UUID carried in
// the event headers envelope.
const canonicalEventIdempotencyKey = "eventId"

// ---------------------------------------------------------------------------
// INVARIANT: CONTRACT-WIRE-FIELD-CAMELCASE-01 (Hard)
//
// Every `properties.<name>` and `required[*]` element across every
// `contracts/**/*.schema.json` must match camelCasePropertyRE. The check is
// recursive: it walks every nested `properties` object at any depth, including
// `items.properties` for array-of-object schemas and `definitions.*.properties`
// for shared sub-schemas. JSON Schema meta keys (`type`, `items`, `enum`,
// `description`, etc.) are not property NAMES and are not scanned.
//
// Failure shape: report the file + the offending property name + which
// container (root | items | definitions[X]) it appeared under.
//
// ---------------------------------------------------------------------------

func TestArchtest_ContractWireFieldCamelCase(t *testing.T) {
	root := findModuleRoot(t)
	scope := scanner.DirsScope(root, []string{"contracts"})
	scanner.EachContentFile(t, scope, []string{".schema.json"}, func(t *testing.T, cc scanner.ContentContext) {
		var doc any
		require.NoError(t, json.Unmarshal(cc.Bytes, &doc),
			"CONTRACT-WIRE-FIELD-CAMELCASE-01: %s: parse JSON", cc.Rel)

		violations := walkForCamelCase(doc, "")
		for _, v := range violations {
			assert.Fail(t,
				"CONTRACT-WIRE-FIELD-CAMELCASE-01 violation",
				"file=%s path=%s property=%q does not match camelCase pattern %q",
				cc.Rel, v.containerPath, v.name, camelCasePropertyRE.String(),
			)
		}
	})
}

type camelCaseViolation struct {
	containerPath string
	name          string
}

// walkForCamelCase recursively inspects a JSON Schema document and returns
// every property name (under a "properties" object or in a "required" array)
// that fails the camelCase pattern. containerPath records the JSON pointer-ish
// breadcrumb to the violating object for diagnostics.
func walkForCamelCase(node any, containerPath string) []camelCaseViolation {
	var out []camelCaseViolation
	switch v := node.(type) {
	case map[string]any:
		if props, ok := v["properties"].(map[string]any); ok {
			for name, sub := range props {
				if !camelCasePropertyRE.MatchString(name) {
					out = append(out, camelCaseViolation{
						containerPath: joinPath(containerPath, "properties"),
						name:          name,
					})
				}
				out = append(out, walkForCamelCase(sub, joinPath(containerPath, "properties", name))...)
			}
		}
		if req, ok := v["required"].([]any); ok {
			for _, item := range req {
				if name, ok := item.(string); ok && !camelCasePropertyRE.MatchString(name) {
					out = append(out, camelCaseViolation{
						containerPath: joinPath(containerPath, "required"),
						name:          name,
					})
				}
			}
		}
		// Recurse into any nested object value that wasn't already handled
		// (items, definitions, $defs, allOf, oneOf, anyOf siblings).
		for key, child := range v {
			if key == "properties" || key == "required" {
				continue
			}
			out = append(out, walkForCamelCase(child, joinPath(containerPath, key))...)
		}
	case []any:
		for i, child := range v {
			out = append(out, walkForCamelCase(child, joinPathIdx(containerPath, i))...)
		}
	}
	return out
}

func joinPath(parts ...string) string {
	nonEmpty := parts[:0]
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return strings.Join(nonEmpty, ".")
}

func joinPathIdx(prefix string, i int) string {
	if prefix == "" {
		return "[" + intToStr(i) + "]"
	}
	return prefix + "[" + intToStr(i) + "]"
}

func intToStr(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// ---------------------------------------------------------------------------
// INVARIANT: CONTRACT-PAGINATION-PARAM-LIMIT-01 (Hard)
//
// Every `contracts/http/**/contract.yaml` MUST NOT declare any of
// {pageSize, page, pageNum, pageIndex, size, offset} as a queryParam.
// Keyset pagination via (cursor, limit) is the only sanctioned pattern.
// The check is on queryParam *names* — descriptions / examples are not
// constrained.
//
// Failure shape: report the file + the forbidden queryParam name.
//
// ---------------------------------------------------------------------------

// httpContractYAML captures only the queryParams names — keys are arbitrary
// param names so we model the inner block as map[string]any (we ignore the
// per-param schema details, only the keys matter).
type httpContractYAML struct {
	Endpoints struct {
		HTTP struct {
			QueryParams map[string]any `yaml:"queryParams"`
		} `yaml:"http"`
	} `yaml:"endpoints"`
}

func TestArchtest_ContractPaginationParamLimit(t *testing.T) {
	root := findModuleRoot(t)
	scope := scanner.DirsScope(root, []string{"contracts/http"},
		scanner.MatchRels(func(rel string) bool {
			return filepath.Base(rel) == "contract.yaml"
		}),
	)
	scanner.EachContentFile(t, scope, []string{".yaml"}, func(t *testing.T, cc scanner.ContentContext) {
		var doc httpContractYAML
		require.NoError(t, yaml.Unmarshal(cc.Bytes, &doc),
			"CONTRACT-PAGINATION-PARAM-LIMIT-01: %s: parse YAML", cc.Rel)

		for name := range doc.Endpoints.HTTP.QueryParams {
			if _, forbidden := forbiddenPaginationParams[name]; forbidden {
				assert.Fail(t,
					"CONTRACT-PAGINATION-PARAM-LIMIT-01 violation",
					"file=%s queryParam=%q is a forbidden pagination name; use cursor+limit",
					cc.Rel, name,
				)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// INVARIANT: CONTRACT-EVENT-IDEMPOTENCY-KEY-EVENTID-01 (Hard)
//
// Every `contracts/event/**/contract.yaml` MUST declare
// `idempotencyKey: eventId` (matching the wire field defined in
// `headers.schema.json`). Empty / mismatched values fail.
//
// Rationale: idempotencyKey points at the wire field used to dedupe consumed
// events. After J-04 normalization the canonical field is `eventId` (was
// `event_id`). Keeping the YAML reference in lock-step with the schema is the
// only way to keep ConsumerBase idempotency key construction
// (`{ConsumerGroup}:{entry.ID}` via `kernel/governance/rules_fmt.go`) honest.
//
// Failure shape: report the file + the offending idempotencyKey value.
//
// ---------------------------------------------------------------------------

type eventContractYAML struct {
	IdempotencyKey string `yaml:"idempotencyKey"`
}

func TestArchtest_ContractEventIdempotencyKeyEventID(t *testing.T) {
	root := findModuleRoot(t)
	scope := scanner.DirsScope(root, []string{"contracts/event"},
		scanner.MatchRels(func(rel string) bool {
			return filepath.Base(rel) == "contract.yaml"
		}),
	)
	scanner.EachContentFile(t, scope, []string{".yaml"}, func(t *testing.T, cc scanner.ContentContext) {
		var doc eventContractYAML
		require.NoError(t, yaml.Unmarshal(cc.Bytes, &doc),
			"CONTRACT-EVENT-IDEMPOTENCY-KEY-EVENTID-01: %s: parse YAML", cc.Rel)

		assert.Equal(t, canonicalEventIdempotencyKey, doc.IdempotencyKey,
			"CONTRACT-EVENT-IDEMPOTENCY-KEY-EVENTID-01 violation: file=%s idempotencyKey=%q must be %q",
			cc.Rel, doc.IdempotencyKey, canonicalEventIdempotencyKey,
		)
	})
}
