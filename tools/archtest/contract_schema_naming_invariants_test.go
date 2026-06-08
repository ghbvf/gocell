// Package archtest_test — contract_schema_naming_invariants_test.go
//
// File invariants:
//   - INVARIANT: CONTRACT-WIRE-FIELD-CAMELCASE-01
//   - INVARIANT: CONTRACT-PAGINATION-PARAM-LIMIT-01
//   - INVARIANT: CONTRACT-PAGINATION-LIMIT-MAXIMUM-01
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
// AI-robust: Hard — value-level check on parsed YAML/JSON; no string-anchor
// escape; new contract files automatically scanned via EachContentFile. See
// .claude/rules/gocell/ai-robust.md §"载体决策原则" item 3
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

// contractRootBases lists every directory archtest treats as a contract truth
// source. The platform `contracts/` root plus every `examples/<name>/contracts/`
// directory — kernel/metadata/parser.matchContractYAML accepts both shapes,
// so archtest must scan both or example contracts can regress to snake_case
// without tripping the invariant.
//
// Hardcoded by deliberate choice: the SCANNER-FRAMEWORK-USAGE-01 archtest
// (forbiddenWalkSymbols) bans os.ReadDir/filepath.Glob/etc. in tools/archtest/*_test.go,
// so runtime directory enumeration is not an option. The trade-off is offset by
// TestArchtest_ContractRoots_CoversAllExampleProjects below, which uses the
// scanner framework — itself the archtest-approved truth source for filesystem
// inputs — to discover every examples/<name>/contracts/* containing a contract.yaml,
// then asserts contractRootBases covers each one. Adding a new example without
// updating this list turns that probe red.
var contractRootBases = []string{
	"contracts",
	"examples/corebundlestarter/contracts",
	"examples/demo/contracts",
	"examples/iotdevice/contracts",
	"examples/orderfulfillment/contracts",
	"examples/ssobff/contracts",
	"examples/todoorder/contracts",
	"examples/webhookdemo/contracts",
}

// contractRoots returns the relative paths archtest should scan for contract
// truth at the given subpath ("" scans all contracts, "http" only HTTP, "event"
// only events). The list mirrors contractRootBases, joined with subpath.
func contractRoots(_ *testing.T, _ string, subpath string) []string {
	out := make([]string, len(contractRootBases))
	for i, base := range contractRootBases {
		if subpath == "" {
			out[i] = base
		} else {
			out[i] = filepath.Join(base, subpath)
		}
	}
	return out
}

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
// the event headers envelope. Strict equality is enforced — templated forms
// like `<topic>:{eventId}` are rejected to keep a single canonical style
// across platform and example contracts. Per-topic namespacing happens at
// runtime via `{ConsumerGroup}:{entry.ID}` in ConsumerBase, not via the
// contract.yaml idempotencyKey string (which is a pure metadata reference).
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

// TestArchtest_ContractWireFieldCamelCase scans every contracts/**/*.schema.json
// via EachContentFile and calls walkForCamelCase on the parsed document.
//
// Blind spots (AST/schema forms outside EachContentFile+walkForCamelCase scope):
//   - JSON Schema `patternProperties` keys are NOT scanned: regex-keyed property
//     names are not field names in the wire sense — intentional carve-out.
//   - `definitions`, `$defs`, `allOf`/`anyOf`/`oneOf`, `if`/`then`/`else`,
//     `items.properties` are all covered via the generic recursive branch in
//     walkForCamelCase (the `for key, child := range v` loop that recurses into
//     any map value not already handled as `properties`/`required`). The negative
//     probes TestArchtest_ContractWireFieldCamelCase_NegativeProbe_* below pin
//     each of these paths explicitly.
func TestArchtest_ContractWireFieldCamelCase(t *testing.T) {
	root := findModuleRoot(t)
	scope := scanner.DirsScope(root, contractRoots(t, root, ""))
	scanner.EachContentFile(t, scope, []string{".schema.json"}, func(t *testing.T, cc scanner.ContentContext) {
		var doc any
		require.NoError(t, json.Unmarshal(cc.Bytes, &doc),
			"CONTRACT-WIRE-FIELD-CAMELCASE-01: %s: parse JSON", cc.Rel)

		violations := walkForCamelCase(doc, "")
		for _, v := range violations {
			assert.Fail(
				t,
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

// TestArchtest_ContractWireFieldCamelCase_NegativeProbe_NestedDefinitions
// asserts that a snake_case property under definitions.*.properties is caught.
func TestArchtest_ContractWireFieldCamelCase_NegativeProbe_NestedDefinitions(t *testing.T) {
	doc := map[string]any{
		"definitions": map[string]any{
			"User": map[string]any{
				"properties": map[string]any{
					"user_id": map[string]any{"type": "string"},
				},
			},
		},
	}
	violations := walkForCamelCase(doc, "")
	if len(violations) == 0 {
		t.Fatal("expected violation for snake_case property under definitions, got none")
	}
	found := false
	for _, v := range violations {
		if v.name == "user_id" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected violation name=user_id, got %+v", violations)
	}
}

// TestArchtest_ContractWireFieldCamelCase_NegativeProbe_IfThenElse asserts that
// snake_case properties inside if.properties and then.properties are caught.
func TestArchtest_ContractWireFieldCamelCase_NegativeProbe_IfThenElse(t *testing.T) {
	doc := map[string]any{
		"if": map[string]any{
			"properties": map[string]any{
				"cond_a": map[string]any{"type": "boolean"},
			},
		},
		"then": map[string]any{
			"properties": map[string]any{
				"fail_field": map[string]any{"type": "string"},
			},
		},
	}
	violations := walkForCamelCase(doc, "")
	if len(violations) < 2 {
		t.Fatalf("expected at least 2 violations (cond_a, fail_field), got %+v", violations)
	}
	names := make(map[string]bool, len(violations))
	for _, v := range violations {
		names[v.name] = true
	}
	if !names["cond_a"] || !names["fail_field"] {
		t.Fatalf("expected violations for cond_a and fail_field, got %+v", violations)
	}
}

// TestArchtest_ContractWireFieldCamelCase_NegativeProbe_AllOf asserts that a
// snake_case property nested inside an allOf entry is caught.
func TestArchtest_ContractWireFieldCamelCase_NegativeProbe_AllOf(t *testing.T) {
	doc := map[string]any{
		"allOf": []any{
			map[string]any{
				"properties": map[string]any{
					"bad_name": map[string]any{"type": "string"},
				},
			},
		},
	}
	violations := walkForCamelCase(doc, "")
	if len(violations) == 0 {
		t.Fatal("expected violation for snake_case property under allOf, got none")
	}
	found := false
	for _, v := range violations {
		if v.name == "bad_name" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected violation name=bad_name, got %+v", violations)
	}
}

// TestArchtest_ContractWireFieldCamelCase_NegativeProbe_ItemsObjectArray asserts
// that a snake_case property inside properties.list.items.properties is caught.
func TestArchtest_ContractWireFieldCamelCase_NegativeProbe_ItemsObjectArray(t *testing.T) {
	doc := map[string]any{
		"properties": map[string]any{
			"list": map[string]any{
				"type": "array",
				"items": map[string]any{
					"properties": map[string]any{
						"snake_case_field": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
	violations := walkForCamelCase(doc, "")
	if len(violations) == 0 {
		t.Fatal("expected violation for snake_case property under items.properties, got none")
	}
	found := false
	for _, v := range violations {
		if v.name == "snake_case_field" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected violation name=snake_case_field, got %+v", violations)
	}
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

// TestArchtest_ContractPaginationParamLimit scans every contracts/http/**/contract.yaml
// and checks that no queryParam key is in forbiddenPaginationParams.
//
// Blind spots:
//   - Heuristic limitation: forbiddenPaginationParams is a closed-set name
//     allowlist. If a contract author invents a new non-canonical pagination
//     name (e.g. `pageNum2`, `offsetv2`), this archtest will not catch it.
//     Extending the forbidden set is the explicit upgrade path when a new
//     forbidden name is identified.
//   - The 3 negative probes below pin:
//     (a) a known-bad name (pageSize) is caught,
//     (b) a contract with no queryParams block produces zero violations,
//     (c) canonical names (limit, cursor) are accepted without violation.
func TestArchtest_ContractPaginationParamLimit(t *testing.T) {
	root := findModuleRoot(t)
	scope := scanner.DirsScope(
		root, contractRoots(t, root, "http"),
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
				assert.Fail(
					t,
					"CONTRACT-PAGINATION-PARAM-LIMIT-01 violation",
					"file=%s queryParam=%q is a forbidden pagination name; use cursor+limit",
					cc.Rel, name,
				)
			}
		}
	})
}

// TestArchtest_ContractPaginationParamLimit_NegativeProbe_BadName asserts that a
// contract with queryParams: {pageSize: ...} is caught as a violation.
func TestArchtest_ContractPaginationParamLimit_NegativeProbe_BadName(t *testing.T) {
	raw := []byte(`
endpoints:
  http:
    queryParams:
      pageSize:
        type: integer
`)
	var doc httpContractYAML
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	var violations []string
	for name := range doc.Endpoints.HTTP.QueryParams {
		if _, forbidden := forbiddenPaginationParams[name]; forbidden {
			violations = append(violations, name)
		}
	}
	if len(violations) == 0 {
		t.Fatal("expected violation for queryParam pageSize, got none")
	}
}

// TestArchtest_ContractPaginationParamLimit_NegativeProbe_NoQueryParams asserts
// that a contract with no queryParams block produces zero violations (zero-state
// safety: absent section must not trigger false positives).
func TestArchtest_ContractPaginationParamLimit_NegativeProbe_NoQueryParams(t *testing.T) {
	raw := []byte(`
endpoints:
  http: {}
`)
	var doc httpContractYAML
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	for name := range doc.Endpoints.HTTP.QueryParams {
		if _, forbidden := forbiddenPaginationParams[name]; forbidden {
			t.Fatalf("unexpected violation for queryParam %q in contract with no queryParams", name)
		}
	}
}

// TestArchtest_ContractPaginationParamLimit_NegativeProbe_AllowedNames asserts
// that canonical pagination names (limit, cursor) produce no violation.
func TestArchtest_ContractPaginationParamLimit_NegativeProbe_AllowedNames(t *testing.T) {
	raw := []byte(`
endpoints:
  http:
    queryParams:
      limit:
        type: integer
      cursor:
        type: string
`)
	var doc httpContractYAML
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	for name := range doc.Endpoints.HTTP.QueryParams {
		if _, forbidden := forbiddenPaginationParams[name]; forbidden {
			t.Fatalf("unexpected violation for canonical queryParam %q", name)
		}
	}
}

// ---------------------------------------------------------------------------
// INVARIANT: CONTRACT-EVENT-IDEMPOTENCY-KEY-EVENTID-01 (Hard)
//
// Every `contracts/event/**/contract.yaml` (and `examples/*/contracts/event/...`)
// MUST declare `idempotencyKey: eventId` (matching the wire field defined in
// `headers.schema.json`). Strict equality — empty values, the legacy
// snake_case `event_id`, casing variants (`EventId`/`eventID`), and templated
// forms like `<topic>:{eventId}` are all rejected. Per-topic namespacing
// happens at runtime in ConsumerBase via `{ConsumerGroup}:{entry.ID}`, not
// at the contract.yaml metadata layer; keeping the YAML to a single canonical
// reference avoids two equivalent dialects.
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

// TestArchtest_ContractEventIdempotencyKeyEventID scans every
// contracts/event/**/contract.yaml (and examples/*/contracts/event/...) and
// asserts idempotencyKey == "eventId".
//
// Blind spots:
//   - Strict equality against canonicalEventIdempotencyKey ("eventId"). Whitespace
//     variants (e.g. " eventId"), casing variants ("EventId", "eventID"), templated
//     forms like "<topic>:{eventId}", or future legitimate alternate key names
//     (e.g. "messageId") would fail and require an explicit ADR + update to
//     canonicalEventIdempotencyKey.
//   - The 3 negative probes below pin:
//     (a) missing idempotencyKey (empty string) is caught,
//     (b) legacy snake_case value "event_id" is caught,
//     (c) canonical value "eventId" is accepted without violation.
func TestArchtest_ContractEventIdempotencyKeyEventID(t *testing.T) {
	root := findModuleRoot(t)
	scope := scanner.DirsScope(
		root, contractRoots(t, root, "event"),
		scanner.MatchRels(func(rel string) bool {
			return filepath.Base(rel) == "contract.yaml"
		}),
	)
	scanner.EachContentFile(t, scope, []string{".yaml"}, func(t *testing.T, cc scanner.ContentContext) {
		var doc eventContractYAML
		require.NoError(t, yaml.Unmarshal(cc.Bytes, &doc),
			"CONTRACT-EVENT-IDEMPOTENCY-KEY-EVENTID-01: %s: parse YAML", cc.Rel)

		assert.Equal(
			t, canonicalEventIdempotencyKey, doc.IdempotencyKey,
			"CONTRACT-EVENT-IDEMPOTENCY-KEY-EVENTID-01 violation: file=%s idempotencyKey=%q must be %q",
			cc.Rel, doc.IdempotencyKey, canonicalEventIdempotencyKey,
		)
	})
}

// TestArchtest_ContractEventIdempotencyKeyEventID_NegativeProbe_EmptyValue asserts
// that a contract YAML with no idempotencyKey field (zero-value empty string) is
// caught as a violation.
func TestArchtest_ContractEventIdempotencyKeyEventID_NegativeProbe_EmptyValue(t *testing.T) {
	raw := []byte(`
id: event.test.v1
kind: event
`)
	var doc eventContractYAML
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	if doc.IdempotencyKey == canonicalEventIdempotencyKey {
		t.Fatalf("expected empty idempotencyKey to differ from canonical %q", canonicalEventIdempotencyKey)
	}
}

// TestArchtest_ContractEventIdempotencyKeyEventID_NegativeProbe_OldSnakeCaseValue
// asserts that the legacy pre-rename value "event_id" is caught as a violation.
func TestArchtest_ContractEventIdempotencyKeyEventID_NegativeProbe_OldSnakeCaseValue(t *testing.T) {
	raw := []byte(`
idempotencyKey: event_id
`)
	var doc eventContractYAML
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	if doc.IdempotencyKey == canonicalEventIdempotencyKey {
		t.Fatalf("expected legacy value %q to differ from canonical %q", doc.IdempotencyKey, canonicalEventIdempotencyKey)
	}
}

// TestArchtest_ContractEventIdempotencyKeyEventID_NegativeProbe_TemplatedFormRejected
// asserts that templated forms like "<topic>:{eventId}" are rejected — strict
// equality keeps one canonical style across platform + examples contracts.
func TestArchtest_ContractEventIdempotencyKeyEventID_NegativeProbe_TemplatedFormRejected(t *testing.T) {
	raw := []byte(`
idempotencyKey: device-registered:{eventId}
`)
	var doc eventContractYAML
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	if doc.IdempotencyKey == canonicalEventIdempotencyKey {
		t.Fatalf("expected templated form %q to differ from canonical %q", doc.IdempotencyKey, canonicalEventIdempotencyKey)
	}
}

// TestArchtest_ContractEventIdempotencyKeyEventID_NegativeProbe_CorrectValue
// asserts that the canonical value "eventId" produces no violation.
func TestArchtest_ContractEventIdempotencyKeyEventID_NegativeProbe_CorrectValue(t *testing.T) {
	raw := []byte(`
idempotencyKey: eventId
`)
	var doc eventContractYAML
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	if doc.IdempotencyKey != canonicalEventIdempotencyKey {
		t.Fatalf("expected canonical value %q, got %q", canonicalEventIdempotencyKey, doc.IdempotencyKey)
	}
}

// ---------------------------------------------------------------------------
// INVARIANT: CONTRACT-PAGINATION-LIMIT-MAXIMUM-01 (Hard)
//
// Every `contracts/http/**/contract.yaml` (and `examples/*/contracts/http/...`)
// that declares a `limit` queryParam MUST set `limit.maximum`, and the value
// MUST be <= MaxPaginationLimit (500). This pins the security ceiling
// documented in `.claude/rules/gocell/go-standards.md` §"安全检查点" so a
// contract author cannot silently raise the per-list page size beyond the
// project-wide bound. Pair invariant with CONTRACT-PAGINATION-PARAM-LIMIT-01:
// the latter forbids offset/page-style names, this one bounds the canonical
// `limit` value.
//
// Failure shape: report the file + the missing-or-overshoot limit.maximum value.
//
// ---------------------------------------------------------------------------

// MaxPaginationLimit is the per-list page ceiling. ref: .claude/rules/gocell/go-standards.md.
const MaxPaginationLimit = 500

// httpLimitContractYAML decodes only the structural slice we need: any
// queryParam value carrying a `maximum` field. Other queryParams (cursor,
// arbitrary filters) decode with zero-value Maximum and are ignored by the
// archtest body, which keys exclusively on "limit".
type httpLimitContractYAML struct {
	Endpoints struct {
		HTTP struct {
			QueryParams map[string]struct {
				Type    string `yaml:"type"`
				Maximum *int   `yaml:"maximum"`
			} `yaml:"queryParams"`
		} `yaml:"http"`
	} `yaml:"endpoints"`
}

// TestArchtest_ContractPaginationLimitMaximum scans every
// contracts/http/**/contract.yaml (and examples/*/contracts/http/...) and
// asserts that when queryParams.limit exists, queryParams.limit.maximum is
// declared and <= MaxPaginationLimit.
//
// Blind spots:
//   - Only "limit" is bounded; other integer queryParams (per-endpoint custom
//     caps) are out of scope — this is intentional, the rule applies to the
//     canonical pagination knob only.
//   - The 3 negative probes pin: missing-maximum / over-bound / canonical-500.
func TestArchtest_ContractPaginationLimitMaximum(t *testing.T) {
	root := findModuleRoot(t)
	scope := scanner.DirsScope(
		root, contractRoots(t, root, "http"),
		scanner.MatchRels(func(rel string) bool {
			return filepath.Base(rel) == "contract.yaml"
		}),
	)
	scanner.EachContentFile(t, scope, []string{".yaml"}, func(t *testing.T, cc scanner.ContentContext) {
		var doc httpLimitContractYAML
		require.NoError(t, yaml.Unmarshal(cc.Bytes, &doc),
			"CONTRACT-PAGINATION-LIMIT-MAXIMUM-01: %s: parse YAML", cc.Rel)

		limit, ok := doc.Endpoints.HTTP.QueryParams["limit"]
		if !ok {
			return
		}
		if limit.Maximum == nil {
			assert.Fail(
				t,
				"CONTRACT-PAGINATION-LIMIT-MAXIMUM-01 violation",
				"file=%s queryParam=limit must declare maximum<=%d", cc.Rel, MaxPaginationLimit,
			)
			return
		}
		if *limit.Maximum > MaxPaginationLimit {
			assert.Fail(
				t,
				"CONTRACT-PAGINATION-LIMIT-MAXIMUM-01 violation",
				"file=%s queryParam=limit maximum=%d exceeds bound %d",
				cc.Rel, *limit.Maximum, MaxPaginationLimit,
			)
		}
	})
}

// TestArchtest_ContractPaginationLimitMaximum_NegativeProbe_MissingMaximum
// asserts that a contract declaring limit without maximum is caught.
func TestArchtest_ContractPaginationLimitMaximum_NegativeProbe_MissingMaximum(t *testing.T) {
	raw := []byte(`
endpoints:
  http:
    queryParams:
      limit:
        type: integer
        minimum: 1
`)
	var doc httpLimitContractYAML
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	limit, ok := doc.Endpoints.HTTP.QueryParams["limit"]
	if !ok {
		t.Fatal("fixture must contain limit queryParam")
	}
	if limit.Maximum != nil {
		t.Fatalf("expected nil maximum, got %d", *limit.Maximum)
	}
}

// TestArchtest_ContractPaginationLimitMaximum_NegativeProbe_OverBound asserts
// that a maximum greater than MaxPaginationLimit is caught.
func TestArchtest_ContractPaginationLimitMaximum_NegativeProbe_OverBound(t *testing.T) {
	raw := []byte(`
endpoints:
  http:
    queryParams:
      limit:
        type: integer
        minimum: 1
        maximum: 5000
`)
	var doc httpLimitContractYAML
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	limit, ok := doc.Endpoints.HTTP.QueryParams["limit"]
	if !ok || limit.Maximum == nil {
		t.Fatalf("fixture parse failed: limit=%+v", limit)
	}
	if *limit.Maximum <= MaxPaginationLimit {
		t.Fatalf("expected over-bound maximum, got %d (bound=%d)", *limit.Maximum, MaxPaginationLimit)
	}
}

// TestArchtest_ContractPaginationLimitMaximum_NegativeProbe_CanonicalAllowed
// asserts that the canonical limit/maximum=500 fixture produces no violation.
func TestArchtest_ContractPaginationLimitMaximum_NegativeProbe_CanonicalAllowed(t *testing.T) {
	raw := []byte(`
endpoints:
  http:
    queryParams:
      limit:
        type: integer
        minimum: 1
        maximum: 500
`)
	var doc httpLimitContractYAML
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	limit, ok := doc.Endpoints.HTTP.QueryParams["limit"]
	if !ok || limit.Maximum == nil {
		t.Fatalf("fixture parse failed: limit=%+v", limit)
	}
	if *limit.Maximum != MaxPaginationLimit {
		t.Fatalf("expected canonical maximum %d, got %d", MaxPaginationLimit, *limit.Maximum)
	}
}

// ---------------------------------------------------------------------------
// Probes for the contractRootBases list — guarantee the hardcoded list covers
// every example project currently under examples/*/contracts/. SCANNER-FRAMEWORK-USAGE-01
// bans os.ReadDir-based directory enumeration, so the truth is discovered via
// scanner.DirsScope over examples/, which is the archtest-approved filesystem
// truth source. The "上游 Hard" half of the funnel: kernel/metadata/parser.matchContractYAML
// treats examples/*/contracts as truth, and this probe ensures the archtest scope
// covers what the parser does. Adding a new example/<name>/ project with a
// contract.yaml turns this probe red until contractRootBases is extended.
// ---------------------------------------------------------------------------

// TestArchtest_ContractRoots_CoversAllExampleProjects discovers every
// examples/<name>/contracts/* directory that actually contains a contract.yaml
// (using scanner.DirsScope — no os.ReadDir) and asserts contractRootBases
// includes "examples/<name>/contracts" for each.
func TestArchtest_ContractRoots_CoversAllExampleProjects(t *testing.T) {
	root := findModuleRoot(t)

	scope := scanner.DirsScope(
		root, []string{"examples"},
		scanner.MatchRels(func(rel string) bool {
			return filepath.Base(rel) == "contract.yaml" &&
				strings.Contains(filepath.ToSlash(rel), "/contracts/")
		}),
	)
	scanner.EachContentFile(t, scope, []string{".yaml"}, func(t *testing.T, cc scanner.ContentContext) {
		parts := strings.Split(filepath.ToSlash(cc.Rel), "/")
		if len(parts) < 3 || parts[0] != "examples" || parts[2] != "contracts" {
			return
		}
		exampleContractsDir := strings.Join(parts[:3], "/")

		covered := false
		for _, base := range contractRootBases {
			if filepath.ToSlash(base) == exampleContractsDir {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("contractRootBases missing %q (discovered via scanner from contract.yaml at %s); "+
				"new example project requires extending contractRootBases", exampleContractsDir, cc.Rel)
		}
	})
}

// TestArchtest_ContractRoots_SubpathHTTP asserts the subpath form returns
// contracts/http plus examples/<name>/contracts/http for every base.
func TestArchtest_ContractRoots_SubpathHTTP(t *testing.T) {
	got := contractRoots(t, "", "http")
	if len(got) != len(contractRootBases) {
		t.Fatalf("contractRoots(\"http\") len=%d, want %d (matching contractRootBases)", len(got), len(contractRootBases))
	}
	if filepath.ToSlash(got[0]) != "contracts/http" {
		t.Fatalf("contractRoots(\"http\")[0] must be %q, got %q", "contracts/http", got[0])
	}
	for i, p := range got {
		want := filepath.ToSlash(filepath.Join(contractRootBases[i], "http"))
		if filepath.ToSlash(p) != want {
			t.Errorf("contractRoots(\"http\")[%d] = %q, want %q", i, p, want)
		}
	}
}

// TestArchtest_ContractRoots_EmptySubpath asserts the bare form returns
// exactly contractRootBases (no subpath suffix).
func TestArchtest_ContractRoots_EmptySubpath(t *testing.T) {
	got := contractRoots(t, "", "")
	if len(got) != len(contractRootBases) {
		t.Fatalf("contractRoots(\"\") len=%d, want %d", len(got), len(contractRootBases))
	}
	for i, p := range got {
		if filepath.ToSlash(p) != filepath.ToSlash(contractRootBases[i]) {
			t.Errorf("contractRoots(\"\")[%d] = %q, want %q", i, p, contractRootBases[i])
		}
	}
}
