package governance

// rules_misc_strict.go consolidates two rule clusters tied to the strict
// pipeline:
//
//   - strict-only rules registered in allRules (PhaseStrict): FMT-16, FMT-17,
//     FMT-19, DOC-NAME-01 — run only with `gocell validate --strict`.
//   - FMT-20 / FMT-21 / FMT-22 / FMT-23 / FMT-25 schema-walking rules
//     (formerly rules_strict_extra.go) — registered in allRules (PhaseBase)
//     but lineage-coupled to the strict scaffolding (FMT-20/25 reuse
//     walkSchemaTreeDepth helpers, FMT-23 shares the deprecation date
//     semantics with FMT-strict cleanup). FMT-A1 / FMT-C1 are also
//     PhaseBase in allRules.
//
// validateFMT19 (wrapper package-state, rules_misc_advisory.go) and
// validateDOCNAME01 (doc literals, rules_misc_advisory.go) are also
// registered in allRules (PhaseStrict) as cross-file calls within the same
// package.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// =============================================================================
// strict-only registry + FMT-16/17 + FMT-A1/C1 unconditional (formerly rules_strict.go)
// =============================================================================

// validateFMT16 checks that no slice, cell, or assembly directory contains
// '-' (kebab-case). The check reads the filesystem directory segment
// captured by the parser (SliceMeta.Dir / CellMeta.Dir / AssemblyMeta.Dir),
// not the map key or yaml id. This matters: a directory can live under a
// kebab name while declaring a no-dash id in yaml, and pre-Dir
// implementations that read only the id let kebab directories slip
// through. Entries synthesized in tests without a Dir are skipped
// (Dir != "" is the "parsed from disk" signal). The rule is PhaseStrict in
// allRules — ValidateStrict gates invocation via --strict.
func (v *Validator) validateFMT16() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		results = append(results, v.checkKebabDir(s.Dir, s.ID, sliceFile(s), "slice")...)
	}
	for _, c := range v.project.Cells {
		results = append(results, v.checkKebabDir(c.Dir, c.ID, cellFile(c), "cell")...)
	}
	for _, a := range v.project.Assemblies {
		results = append(results, v.checkKebabDir(a.Dir, a.ID, assemblyFile(a), "assembly")...)
	}
	return results
}

// checkKebabDir returns a FMT-16 error if dir is non-empty and contains '-'.
// kind is one of "slice", "cell", "assembly" — used only in the error message.
func (v *Validator) checkKebabDir(dir, id, file, kind string) []ValidationResult {
	if dir == "" || !strings.Contains(dir, "-") {
		return nil
	}
	return []ValidationResult{v.newError(
		codeFMT16, IssueInvalid,
		file,
		"id",
		fmt.Sprintf(
			"%s %q uses kebab-case directory %q; kebab-case %s directories are disallowed in strict mode",
			kind, id, dir, kind,
		),
		fmt.Sprintf("rename the directory to %q", strings.ReplaceAll(dir, "-", "")),
	)}
}

// validateFMT17 checks that the first entry in slice.yaml allowedFiles matches
// the canonical slice directory path. Expected path is derived from
// SliceMeta.Dir / CellDir (filesystem truth) so a faked-path/faked-id
// pairing cannot slip through. PhaseStrict in allRules — ValidateStrict
// gates invocation via --strict.
func (v *Validator) validateFMT17() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		if len(s.AllowedFiles) == 0 {
			// FMT-14 already covers missing allowedFiles; skip here.
			continue
		}
		if s.Dir == "" || s.CellDir == "" {
			continue
		}
		expected := fmt.Sprintf("cells/%s/slices/%s/", s.CellDir, s.Dir)
		if s.File != "" {
			expected = strings.TrimSuffix(s.File, "slice.yaml")
		}
		first := s.AllowedFiles[0]
		// Normalize: strip trailing ** or glob suffix for comparison.
		normalized := strings.TrimSuffix(first, "**")
		normalized = strings.TrimSuffix(normalized, "*")
		if !strings.HasPrefix(normalized, expected) && normalized != expected {
			results = append(results, v.newError(
				codeFMT17, IssueMismatch,
				sliceFile(s),
				"allowedFiles[0]",
				fmt.Sprintf(
					"slice %q allowedFiles first entry %q does not match slice directory %q (want prefix %q)",
					s.ID, first, s.Dir, expected,
				),
				fmt.Sprintf("set allowedFiles[0] to %q (or a glob rooted at it)", expected),
			))
		}
	}
	return results
}

// validateFMTC1 checks that every cell.yaml id satisfies
// metadata.CellIDPattern (^[a-z][a-z0-9]+$). It runs unconditionally:
// the rule mirrors a schema-level constraint (schemas/cell.schema.json
// properties.id.pattern, kept byte-equal by TestSchemaConstantsMatchSchema
// Literals) and schema-aware tooling rejects the same values without a
// strict toggle. Gating this check on --strict would leave default
// `gocell validate` users on a different contract than the schema, mirroring
// the single-gatekeeper model declared in docs/architecture/202605061800-
// adr-assembly-yaml-minimal-derivation.md §"Schema 约束单源".
//
// This is the same pattern as validateFMTA1 (assembly id) — both are
// registered in allRules (PhaseBase).
//
// FMT-C1 complements FMT-16: FMT-16 catches kebab filesystem directories,
// while FMT-C1 catches non-conforming yaml ids (kebab, uppercase, single
// char, leading digit) even when the directory is already no-dash. A clean
// project passes both.
func (v *Validator) validateFMTC1() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		if metadata.MatchCellID(c.ID) {
			continue
		}
		results = append(results, v.newError(
			codeFMTC1, IssueInvalid,
			cellFile(c),
			"id",
			fmt.Sprintf(
				"cell id %q does not match %s",
				c.ID, metadata.CellIDPattern,
			),
			"use lowercase ASCII letters + digits, ≥2 chars, starting with a letter",
		))
	}
	return results
}

// validateFMTA1 checks that every assembly.yaml id satisfies
// metadata.AssemblyIDPattern (^[a-z][a-z0-9]+$). The rule mirrors a
// schema-level constraint (schemas/assembly.schema.json properties.id.
// pattern, kept byte-equal by TestSchemaConstantsMatchSchemaLiterals) and
// schema-aware tooling rejects the same values without a strict toggle.
// Gating this check on --strict would leave default `gocell validate`
// users on a different contract than the schema and FMT-30 (deployTemplate
// enum), violating the single-gatekeeper model declared in
// docs/architecture/202605061800-adr-assembly-yaml-minimal-derivation.md
// §"Schema 约束单源". Registered in allRules (PhaseBase).
//
// FMT-16 / FMT-17 stay PhaseStrict in allRules because they catch stylistic
// concerns (kebab-case filesystem directories, allowedFiles drift) that
// schemas do not directly mirror; FMT-C1 was migrated to PhaseBase in
// allRules alongside cell.schema.json properties.id.pattern收紧 (PR-2
// PR-PROM-HARDEN-3).
func (v *Validator) validateFMTA1() []ValidationResult {
	var results []ValidationResult
	for _, a := range v.project.Assemblies {
		if metadata.MatchAssemblyID(a.ID) {
			continue
		}
		results = append(results, v.newError(
			codeFMTA1, IssueInvalid,
			assemblyFile(a),
			"id",
			fmt.Sprintf(
				"assembly id %q does not match %s",
				a.ID, metadata.AssemblyIDPattern,
			),
			"rename to use lowercase ASCII letters + digits, ≥2 chars, starting with a letter",
		))
	}
	return results
}

// =============================================================================
// FMT-20 / FMT-21 / FMT-22 / FMT-23 / FMT-25 (formerly rules_strict_extra.go)
// =============================================================================

// defaultDeprecationGracePeriod is the maximum allowed time between a contract's
// deprecatedAt date and the validation run before FMT-23 fires a warning.
const defaultDeprecationGracePeriod = 90 * 24 * time.Hour

// --- FMT-20 (request schema strict additionalProperties) ---

// validateFMTRequestStrict01 scans every HTTP-kind contract's request schema.
// For each "type":"object" node in the schema (recursive), if it lacks
// "additionalProperties": false, a violation is emitted. Response and event
// schemas are intentionally lenient per ADR-202605031600 (v1 schema evolution).
//
// Rule ID: FMT-20.
// Severity: Error, IssueRequired.
// ref: kubernetes/kubernetes apiserver — StrictSerializer applies to request
// decoding only; response encoding bypasses fieldValidation.
func (v *Validator) validateFMTRequestStrict01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != "http" {
			continue
		}
		results = append(results, v.validateFMTRequestStrictContract(c)...)
	}
	return results
}

func (v *Validator) validateFMTRequestStrictContract(c *metadata.ContractMeta) []ValidationResult {
	var results []ValidationResult
	for _, ref := range metadata.ContractSchemaRefs(c) {
		// FMT-20 only scans request schemas; response and
		// endpoints.http.responses[*] are intentionally excluded per
		// ADR-202605031600 (v1 schema evolution).
		if ref.Field != "schemaRefs.request" || ref.Ref == "" {
			continue
		}
		results = append(results, v.validateFMTRequestStrictRef(c, ref)...)
	}
	return results
}

func (v *Validator) validateFMTRequestStrictRef(c *metadata.ContractMeta, ref metadata.ContractSchemaRef) []ValidationResult {
	resolved, resolveErr := metadata.ResolveContractSchemaRef(v.root, c, ref)
	if resolveErr != nil {
		return []ValidationResult{v.newError(
			codeFMT20, IssueInvalid,
			contractFile(c), ref.Field,
			fmt.Sprintf("contract %q schema %q failed to resolve: %v", c.ID, ref.Ref, resolveErr),
			"ensure the schema path is correct and the file exists relative to the contract dir",
		)}
	}
	missing, err := scanSchemaForStrictMissing(resolved.AbsPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Missing schema file is reported by FMT-09 / REF rules; skip here.
			return nil
		}
		// Parse/IO errors are definitive FMT-20 violations (fail-closed).
		return []ValidationResult{v.newError(
			codeFMT20, IssueInvalid,
			resolved.ProjectRel, "$",
			fmt.Sprintf("contract %q schema %q failed to parse: %v", c.ID, ref.Ref, err),
			"ensure the schema file is well-formed JSON Schema (Draft 2020-12)",
		)}
	}
	return v.fmt20MissingSchemaResults(c, resolved.ProjectRel, missing)
}

func (v *Validator) fmt20MissingSchemaResults(c *metadata.ContractMeta, rel string, missing []string) []ValidationResult {
	results := make([]ValidationResult, 0, len(missing))
	for _, loc := range missing {
		results = append(results, v.newError(
			codeFMT20, IssueRequired,
			rel, loc,
			fmt.Sprintf("contract %q request schema must declare additionalProperties:false at %s "+
				"(strict per FMT-20 / ADR-202605031600)", c.ID, loc),
			fmt.Sprintf("add \"additionalProperties\": false to the object at %s", loc),
		))
	}
	return results
}

// scanSchemaForStrictMissing reads a JSON schema file and recursively walks it.
// For every object node whose "type" equals "object", it checks whether
// "additionalProperties" is set to false. It collects JSON-pointer-style paths
// of violations (e.g. "$", "$.data", "$.data.items").
func scanSchemaForStrictMissing(absPath string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Clean(absPath))
	if err != nil {
		return nil, err
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf(invalidJSONSchemaMsgFmt, absPath, err)
	}
	var missing []string
	walkSchemaObject(schema, "$", &missing)
	return missing, nil
}

// walkSchemaObject recursively walks a schema node and applies
// checkAdditionalProperties at each "type":"object" node. Implemented via the
// shared walkSchemaTreeDepth framework.
func walkSchemaObject(node map[string]any, path string, missing *[]string) {
	walkSchemaTreeDepth(node, path, 0, func(n map[string]any, p string) {
		if t, _ := n["type"].(string); t == "object" {
			checkAdditionalProperties(n, p, missing)
		}
	})
}

// walkSchemaTreeDepth is the shared depth-guarded JSON-schema visitor used by
// FMT-20 (additionalProperties) and FMT-25 (input constraints). It applies
// `visit` at every node, resolves local $ref targets, then recurses through
// object properties, array items, patternProperties, and common composition
// keywords. depth > 32 causes early return to prevent unbounded recursion on
// pathological schemas.
//
// Note: visit is called at every node — branch on node["type"] inside visit
// if a check applies only to objects/strings/integers etc.
func walkSchemaTreeDepth(node map[string]any, path string, depth int, visit func(node map[string]any, path string)) {
	walkSchemaTreeDepthRoot(node, node, path, depth, map[string]bool{}, visit)
}

func walkSchemaTreeDepthRoot(
	root, node map[string]any, path string, depth int,
	seenRefs map[string]bool, visit func(node map[string]any, path string),
) {
	if depth > 32 {
		return
	}
	visit(node, path)
	if ref, ok := node["$ref"].(string); ok && !seenRefs[ref] {
		if target, ok := resolveLocalSchemaRef(root, ref); ok {
			seenRefs[ref] = true
			walkSchemaTreeDepthRoot(root, target, path, depth+1, seenRefs, visit)
			delete(seenRefs, ref)
		}
	}
	walkSchemaNamedMapChildren(root, node, path, depth, seenRefs, visit)
	walkSchemaNamedObjectChildren(root, node, path, depth, seenRefs, visit)
	walkSchemaNamedArrayChildren(root, node, path, depth, seenRefs, visit)
}

func walkSchemaNamedMapChildren(
	root, node map[string]any, path string, depth int,
	seenRefs map[string]bool, visit func(node map[string]any, path string),
) {
	for _, keyword := range []string{"properties", "patternProperties", "dependentSchemas"} {
		children, ok := node[keyword].(map[string]any)
		if !ok {
			continue
		}
		for _, key := range sortedAnyMapKeys(children) {
			if child, ok := children[key].(map[string]any); ok {
				childPath := path + "." + key
				if keyword != "properties" {
					childPath = path + "." + keyword + "." + key
				}
				walkSchemaTreeDepthRoot(root, child, childPath, depth+1, seenRefs, visit)
			}
		}
	}
}

func walkSchemaNamedObjectChildren(
	root, node map[string]any, path string, depth int,
	seenRefs map[string]bool, visit func(node map[string]any, path string),
) {
	for _, keyword := range []string{
		"items",
		"additionalProperties",
		"contains",
		"propertyNames",
		"not",
		"if",
		"then",
		"else",
		"unevaluatedProperties",
		"unevaluatedItems",
	} {
		if child, ok := node[keyword].(map[string]any); ok {
			walkSchemaTreeDepthRoot(root, child, path+"."+keyword, depth+1, seenRefs, visit)
		}
	}
}

func walkSchemaNamedArrayChildren(
	root, node map[string]any, path string, depth int,
	seenRefs map[string]bool, visit func(node map[string]any, path string),
) {
	for _, keyword := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		children, ok := node[keyword].([]any)
		if !ok {
			continue
		}
		for i, val := range children {
			if child, ok := val.(map[string]any); ok {
				walkSchemaTreeDepthRoot(root, child, fmt.Sprintf("%s.%s[%d]", path, keyword, i), depth+1, seenRefs, visit)
			}
		}
	}
}

func sortedAnyMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func resolveLocalSchemaRef(root map[string]any, ref string) (map[string]any, bool) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, false
	}
	var cur any = root
	for part := range strings.SplitSeq(strings.TrimPrefix(ref, "#/"), "/") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[decodeJSONPointerToken(part)]
		if !ok {
			return nil, false
		}
	}
	target, ok := cur.(map[string]any)
	return target, ok
}

func decodeJSONPointerToken(s string) string {
	s = strings.ReplaceAll(s, "~1", "/")
	return strings.ReplaceAll(s, "~0", "~")
}

// checkAdditionalProperties emits a violation unless the node declares
// `additionalProperties: false`. Per ADR-202605031600, FMT-20 only scans
// request schemas, where the goal is strictly closed shape — `true` (explicit
// open) is just as much a bypass as the missing-key case, so both fail. An
// object value (e.g. {"type":"string"}) is also rejected because it is a
// constraint on extra-property values, not a closed-shape declaration.
func checkAdditionalProperties(node map[string]any, path string, missing *[]string) {
	ap, hasAP := node["additionalProperties"]
	if !hasAP {
		// No declaration at all — emit violation.
		*missing = append(*missing, path)
		return
	}
	if b, ok := ap.(bool); ok && !b {
		// Only `additionalProperties: false` satisfies FMT-20.
		return
	}
	// `true`, object value, or any other shape is a violation: request schemas
	// must be strictly closed.
	*missing = append(*missing, path)
}

// --- FMT-21 (formerly FMT-CONTRACT-DIR-ID-MATCH-01; also satisfies FMT-CONTRACT-PATH-ID-MAPPING-01) ---

// validateFMTContractDirIDMatch01 enforces the bijection between a contract's
// declared ID and its filesystem location. For "http.auth.login.v1" the
// contract must live at "contracts/http/auth/login/v1". This rule is the
// canonical PATH-ID-MAPPING governance contract: a dash-vs-slash mismatch
// (e.g. id "http.config.internal-get.v1" at "contracts/http/config/internal/
// get/v1") fires here. See PR-CFG-G1-FU6-RECYCLE for the subsumption record.
//
// Contracts under example projects (e.g. "examples/iotdevice/contracts/…") are
// accepted as long as the segment after the last "contracts/" separator matches
// the ID-derived suffix. A Dir that contains no "contracts/" component at all
// is itself a violation. Contracts with empty Dir are skipped: the parser
// (kernel/metadata.parseContract) only walks "contracts/…" and
// "examples/*/contracts/…" paths, so empty Dir is unreachable in production
// project loads and skipping is safe.
//
// Severity: Error, IssueMismatch.
func (v *Validator) validateFMTContractDirIDMatch01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Dir == "" {
			continue
		}
		derived := filepath.Clean(contractDirFromID(c.ID)) // e.g. "contracts/http/auth/login/v1"
		actualClean := filepath.Clean(c.Dir)

		// Find the last path segment equal to "contracts" so that paths like
		// "examples/iotdevice/contracts/http/foo/v1" match the same derived suffix
		// as a top-level "contracts/http/foo/v1". Use segment-aware matching to
		// avoid matching "mycontracts/" as if it were a "contracts/" root.
		parts := strings.Split(filepath.ToSlash(actualClean), "/")
		lastIdx := -1
		for i, p := range parts {
			if p == "contracts" {
				lastIdx = i
			}
		}
		if lastIdx < 0 {
			// No "contracts" segment anywhere → definite mismatch.
			results = append(results, v.newError(
				codeFMT21, IssueMismatch,
				contractFile(c), "id",
				fmt.Sprintf("contract %q dir %q does not match derived %q", c.ID, c.Dir, derived),
				fmt.Sprintf("move the contract under %q or update the contract id to match its directory layout", derived),
			))
			continue
		}
		actualSuffix := filepath.Join(parts[lastIdx:]...) // "contracts/http/auth/login/v1"
		if actualSuffix != derived {
			results = append(results, v.newError(
				codeFMT21, IssueMismatch,
				contractFile(c), "id",
				fmt.Sprintf("contract %q dir %q does not match derived %q", c.ID, c.Dir, derived),
				fmt.Sprintf("align directory layout to %q (or update the contract id segments to match the dir)", derived),
			))
		}
	}
	return results
}

// --- FMT-22 (formerly STATUSBOARD-STATE-ENUM-01) ---

// validStatusBoardStates contains the accepted state values for status-board entries.
var validStatusBoardStates = map[string]bool{
	"todo":  true,
	"doing": true,
	"done":  true,
}

// validateStatusBoardStateEnum01 checks that every status-board entry has a
// state value present in validStatusBoardStates (defined above).
//
// Severity: Error, IssueInvalid.
func (v *Validator) validateStatusBoardStateEnum01() []ValidationResult {
	var results []ValidationResult
	for i, e := range v.project.StatusBoard {
		if !validStatusBoardStates[e.State] {
			results = append(results, v.newError(
				codeFMT22, IssueInvalid,
				"journeys/status-board.yaml",
				fmt.Sprintf("[%d].state", i),
				fmt.Sprintf(
					"status-board entry %q state %q must be one of {todo, doing, done}",
					e.JourneyID, e.State,
				),
				"set state to todo, doing, or done",
			))
		}
	}
	return results
}

// --- FMT-23 (formerly CONTRACT-DEPRECATED-CLEANUP-01) ---

// validateContractDeprecatedCleanup01 enforces that deprecated contracts carry a
// valid deprecatedAt date and are not stale (>90 days since deprecation).
//
// Three cases:
//   - deprecated + empty deprecatedAt → Error, IssueRequired
//   - deprecated + malformed date → Error, IssueInvalid
//   - deprecated + date >90d ago → Warning, IssueForbidden
func (v *Validator) validateContractDeprecatedCleanup01() []ValidationResult {
	var results []ValidationResult
	now := v.clk.Now()
	for _, c := range v.project.Contracts {
		if c.Lifecycle != "deprecated" {
			continue
		}
		if c.DeprecatedAt == "" {
			results = append(results, v.newError(
				codeFMT23, IssueRequired,
				contractFile(c), "deprecatedAt",
				fmt.Sprintf("contract %q is deprecated but missing deprecatedAt", c.ID),
				"add deprecatedAt: YYYY-MM-DD to contract.yaml (the day deprecation started)",
			))
			continue
		}
		ts, err := time.ParseInLocation("2006-01-02", c.DeprecatedAt, time.UTC)
		if err != nil {
			results = append(results, v.newError(
				codeFMT23, IssueInvalid,
				contractFile(c), "deprecatedAt",
				fmt.Sprintf("contract %q deprecatedAt %q is not YYYY-MM-DD", c.ID, c.DeprecatedAt),
				"reformat deprecatedAt as a four-digit-year ISO date (e.g. 2026-05-13)",
			))
			continue
		}
		if now.UTC().Sub(ts) > defaultDeprecationGracePeriod {
			results = append(results, v.newWarning(
				codeFMT23, IssueForbidden,
				contractFile(c), "lifecycle",
				fmt.Sprintf(
					"contract %q has been deprecated for >90d (since %s)",
					c.ID, c.DeprecatedAt,
				),
				"delete the contract and migrate all consumers, or refresh deprecatedAt to today after re-evaluating the deprecation timeline",
			))
		}
	}
	return results
}

// --- FMT-25 (input constraint enforcement) ---

// inputConstraintViolation captures a single missing-constraint finding from
// either a request schema (path = JSON-pointer) or a contract.yaml param
// (path = "endpoints.http.queryParams.<name>.<facet>" /
// "endpoints.http.pathParams.<name>.<facet>").
//
// Two violation shapes share this type:
//
//   - Missing-facet: `missing` is the facet name; `relMin` and `relMax`
//     are empty.
//   - Min > Max relation fault: `relMin` and `relMax` carry the facet
//     names (e.g. minLength / maxLength); `missing` is empty. The
//     producer (appendSchemaBoundRelationViolation) sets issueType to
//     IssueInvalid; the FMT-25 emission site calls newError with an explicit
//     fix argument (INV-3 GOVERNANCE-RULE-ERROR-FIX-FIELD-01 checks the fix
//     arg, not a Message substring).
type inputConstraintViolation struct {
	location  string // JSON pointer or full metadata field path.
	missing   string // "minLength" | "maxLength" | "minimum" | "maximum" — empty for relation faults.
	relMin    string // non-empty when this is a relation fault; pairs with relMax.
	relMax    string // non-empty when this is a relation fault; pairs with relMin.
	issueType IssueType
}

type schemaWalkError struct {
	path string
	msg  string
}

func (e *schemaWalkError) Error() string {
	return fmt.Sprintf("%s at %s", e.msg, e.path)
}

// validateFMTInputConstraint01 enforces input-side schema constraints on
// HTTP-kind contracts:
//   - request.schema.json: every "type":"string" leaf must declare both
//     minLength and maxLength; every "type":"integer" or "type":"number" leaf
//     must declare both minimum and maximum. JSON Schema type arrays are
//     interpreted semantically, so ["string","null"] is still governed as a
//     string input.
//   - contract.yaml.queryParams / pathParams: same rules apply to each
//     ParamSchema, with one exemption: Format == "uuid" skips minLength /
//     maxLength enforcement (RFC 4122 fixes UUIDs at 36 chars).
//
// Severity: Error, IssueRequired (missing facets fail the build; existing
// declarations of explicit zero are accepted). Non-local or unresolved $ref
// targets and depth-limit truncation are IssueInvalid fail-closed diagnostics:
// FMT-25 must not silently pass schemas it could not fully inspect.
//
// Rule ID: FMT-25.
//
// ref: OWASP API Security Top 10 — API4:2019 Lack of Resources & Rate Limiting
// (input size bounds defend against DoS and overlong-payload attacks).
// ref: JSON Schema Draft 2020-12 string/numeric validation vocabulary.
func (v *Validator) validateFMTInputConstraint01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		results = append(results, v.validateContractInputConstraints(c)...)
	}
	return results
}

func (v *Validator) validateContractInputConstraints(c *metadata.ContractMeta) []ValidationResult {
	if c.Kind != "http" {
		return nil
	}
	var results []ValidationResult
	results = append(results, v.validateRequestSchemaInputConstraints(c)...)
	results = append(results, v.validateParamInputConstraints(c)...)
	return results
}

func (v *Validator) validateRequestSchemaInputConstraints(c *metadata.ContractMeta) []ValidationResult {
	if c.SchemaRefs.Request == "" {
		return nil
	}
	ref := metadata.ContractSchemaRef{
		Field: "schemaRefs.request",
		Ref:   c.SchemaRefs.Request,
		Scope: metadata.SchemaRefScopeContractDir,
	}
	resolved, resolveErr := metadata.ResolveContractSchemaRef(v.root, c, ref)
	if resolveErr != nil {
		return []ValidationResult{v.newError(
			codeFMT25, IssueInvalid,
			contractFile(c), ref.Field,
			fmt.Sprintf("contract %q request schema %q failed to resolve: %v", c.ID, c.SchemaRefs.Request, resolveErr),
			"ensure schemaRefs.request points at an existing schema file under the contract dir",
		)}
	}
	missing, err := scanSchemaForInputConstraints(resolved.AbsPath, v.root)
	if err != nil {
		if os.IsNotExist(err) {
			return []ValidationResult{v.newError(
				codeFMT25, IssueRefNotFound,
				contractFile(c), ref.Field,
				fmt.Sprintf("contract %q request schema points to missing file %q", c.ID, c.SchemaRefs.Request),
				"create the schema file at the referenced path or correct schemaRefs.request",
			)}
		}
		field := "$"
		var walkErr *schemaWalkError
		if errors.As(err, &walkErr) {
			field = walkErr.path
		}
		return []ValidationResult{v.newError(
			codeFMT25, IssueInvalid,
			resolved.ProjectRel, field,
			fmt.Sprintf("contract %q request schema %q failed to parse: %v", c.ID, c.SchemaRefs.Request, err),
			"ensure the schema is well-formed JSON Schema and all $ref targets resolve locally",
		)}
	}
	var results []ValidationResult
	for _, viol := range missing {
		issueType := viol.issueType
		if issueType == "" {
			issueType = IssueRequired
		}
		if viol.relMin != "" {
			results = append(results, v.newError(
				codeFMT25, issueType,
				resolved.ProjectRel, viol.location,
				fmt.Sprintf("contract %q request schema field %s has %s > %s",
					c.ID, viol.location, viol.relMin, viol.relMax),
				fmt.Sprintf("ensure %s <= %s on the schema node at %s", viol.relMin, viol.relMax, viol.location),
			))
			continue
		}
		results = append(results, v.newError(
			codeFMT25, issueType,
			resolved.ProjectRel, viol.location,
			fmt.Sprintf("contract %q request schema field %s missing %s", c.ID, viol.location, viol.missing),
			fmt.Sprintf("declare %s on the schema node at %s", viol.missing, viol.location),
		))
	}
	return results
}

func (v *Validator) validateParamInputConstraints(c *metadata.ContractMeta) []ValidationResult {
	if c.Endpoints.HTTP == nil {
		return nil
	}
	h := c.Endpoints.HTTP
	results := v.checkParamSchemaConstraints(c, h.QueryParams, "queryParams")
	if pathParamsReadyForInputConstraints(h) {
		results = append(results, v.checkParamSchemaConstraints(c, h.PathParams, "pathParams")...)
	}
	return results
}

func pathParamsReadyForInputConstraints(h *metadata.HTTPTransportMeta) bool {
	if h.Path == "" || !strings.HasPrefix(h.Path, "/") {
		return false
	}
	placeholders := extractPathPlaceholders(h.Path)
	placeholderSet := make(map[string]bool, len(placeholders))
	for _, name := range placeholders {
		placeholderSet[name] = true
		if _, ok := h.PathParams[name]; !ok {
			return false
		}
	}
	for _, name := range sortedParamKeys(h.PathParams) {
		if !placeholderSet[name] {
			return false
		}
		p := h.PathParams[name]
		if p.Type == "" || !metadata.ParamTypes[p.Type] {
			return false
		}
		if p.Required != nil && !*p.Required {
			return false
		}
	}
	return true
}

// scanSchemaForInputConstraints reads a JSON schema file and walks every node,
// emitting a violation for each missing minLength/maxLength on strings and
// minimum/maximum on integer/number nodes. Paths use the same JSON-pointer style as
// scanSchemaForStrictMissing (e.g. "$", "$.user.name", "$.tags.items").
//
// Cross-file $ref nodes (e.g. a shared CAS expectedVersion mixin under
// contracts/shared/) are inlined first via inlineCrossFileSchemaRefs so the
// constraint check sees the referenced node's type/minimum/maximum wherever the
// single source actually lives. Same-file (#/...) $ref resolution is left to the
// existing local walker. projectRoot bounds the path-traversal guard.
func scanSchemaForInputConstraints(absPath, projectRoot string) ([]inputConstraintViolation, error) {
	raw, err := os.ReadFile(filepath.Clean(absPath))
	if err != nil {
		return nil, err
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf(invalidJSONSchemaMsgFmt, absPath, err)
	}
	inlined, err := inlineCrossFileSchemaRefs(schema, filepath.Dir(absPath), projectRoot, map[string]bool{})
	if err != nil {
		return nil, err
	}
	schema, _ = inlined.(map[string]any)
	var missing []inputConstraintViolation
	if err := walkSchemaTreeDepthInput(schema, "$", func(n map[string]any, p string) {
		checkInputConstraints(n, p, &missing)
	}); err != nil {
		return nil, err
	}
	// Sort for deterministic output across runs (map iteration is unordered).
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].location != missing[j].location {
			return missing[i].location < missing[j].location
		}
		return missing[i].missing < missing[j].missing
	})
	return missing, nil
}

// inlineCrossFileSchemaRefs recursively replaces every cross-file $ref node
// (a relative path like "../../shared/cas/v1/expected_version.schema.json") with
// the referenced schema's content, so downstream constraint checks see the real
// type/minimum/maximum. Same-file refs ("#/...") and absolute URLs are left
// untouched. dir is the directory of the file currently being walked; projectRoot
// bounds the path-traversal guard; seen breaks cross-file cycles by absolute path.
//
// The passed node (when a map) is mutated in place. Callers must pass a
// freshly-unmarshalled map, not a shared or cached map, to avoid corrupting
// other consumers.
func inlineCrossFileSchemaRefs(node any, dir, projectRoot string, seen map[string]bool) (any, error) {
	switch n := node.(type) {
	case map[string]any:
		return inlineRefsInMap(n, dir, projectRoot, seen)
	case []any:
		return inlineRefsInSlice(n, dir, projectRoot, seen)
	default:
		return node, nil
	}
}

// inlineRefsInMap replaces a cross-file $ref map with its referenced content, or
// recurses into each child value. It mutates n in place for non-$ref maps;
// callers must pass a freshly-unmarshalled map, not a shared or cached one.
func inlineRefsInMap(n map[string]any, dir, projectRoot string, seen map[string]bool) (any, error) {
	if ref, ok := n["$ref"].(string); ok && isCrossFileSchemaRef(ref) {
		return resolveCrossFileSchemaRef(ref, dir, projectRoot, seen)
	}
	for key, child := range n {
		resolved, err := inlineCrossFileSchemaRefs(child, dir, projectRoot, seen)
		if err != nil {
			return nil, err
		}
		n[key] = resolved
	}
	return n, nil
}

// inlineRefsInSlice recurses into each element of a schema array (allOf, etc.).
func inlineRefsInSlice(n []any, dir, projectRoot string, seen map[string]bool) (any, error) {
	for i, child := range n {
		resolved, err := inlineCrossFileSchemaRefs(child, dir, projectRoot, seen)
		if err != nil {
			return nil, err
		}
		n[i] = resolved
	}
	return n, nil
}

// isCrossFileSchemaRef reports whether ref points at another file (not a
// same-file JSON pointer and not an absolute URL).
func isCrossFileSchemaRef(ref string) bool {
	return ref != "" &&
		!strings.HasPrefix(ref, "#") &&
		!strings.HasPrefix(ref, "http://") &&
		!strings.HasPrefix(ref, "https://")
}

// invalidJSONSchemaMsgFmt is the shared fmt.Errorf format for an unparseable
// JSON schema file (main request schema or a cross-file $ref target).
const invalidJSONSchemaMsgFmt = "invalid JSON schema %s: %w"

// isAbsoluteSchemaRef reports whether a forward-slash $ref is absolute on ANY
// OS: a leading "/" (POSIX, via path.IsAbs) or a Windows drive/UNC root (via
// filepath.IsAbs after FromSlash). Contract $refs are forward-slash by
// convention, so "/etc/passwd" must be detected as absolute regardless of the
// host OS — filepath.IsAbs alone returns false for "/x" on Windows, which let an
// absolute ref slip past on windows-latest.
func isAbsoluteSchemaRef(ref string) bool {
	return path.IsAbs(ref) || filepath.IsAbs(filepath.FromSlash(ref))
}

// resolveCrossFileSchemaRef reads the referenced file (guarded within
// projectRoot) and returns its fully-inlined content.
func resolveCrossFileSchemaRef(ref, dir, projectRoot string, seen map[string]bool) (any, error) {
	// Reject absolute-path $ref values before any path join. filepath.Join drops
	// the base directory when the second argument is absolute (e.g.
	// filepath.Join("/a/b", "/etc/passwd") == "/etc/passwd"), so we must check
	// before joining. This mirrors the contractgen bundler behavior
	// (tools/codegen/contractgen/refbundle.go::validateRefString).
	if isAbsoluteSchemaRef(ref) {
		return nil, &schemaWalkError{path: ref, msg: fmt.Sprintf("cross-file $ref %q must be relative", ref)}
	}
	targetAbs := filepath.Clean(filepath.Join(dir, filepath.FromSlash(ref)))
	if projectRoot != "" && !IsWithinRoot(projectRoot, targetAbs) {
		return nil, &schemaWalkError{path: ref, msg: fmt.Sprintf("cross-file $ref %q escapes project root", ref)}
	}
	if seen[targetAbs] {
		return nil, &schemaWalkError{path: ref, msg: fmt.Sprintf("cyclic cross-file $ref %q", ref)}
	}
	raw, err := os.ReadFile(filepath.Clean(targetAbs))
	if err != nil {
		// Wrap as schemaWalkError so the caller's os.IsNotExist branch does not
		// mis-attribute the missing file to the main schema. The walkErr branch
		// (~line 746) surfaces the $ref path in the field + message instead.
		return nil, &schemaWalkError{path: ref, msg: fmt.Sprintf("cross-file $ref target %q not found: %v", targetAbs, err)}
	}
	var target any
	if err := json.Unmarshal(raw, &target); err != nil {
		return nil, fmt.Errorf(invalidJSONSchemaMsgFmt, targetAbs, err)
	}
	seen[targetAbs] = true
	resolved, err := inlineCrossFileSchemaRefs(target, filepath.Dir(targetAbs), projectRoot, seen)
	delete(seen, targetAbs)
	return resolved, err
}

// checkInputConstraints branches on node["type"] and records missing facets.
// Strings missing minLength or maxLength → violations.
// Integers/numbers missing minimum or maximum → violations.
// Other types (boolean, object, array) are unaffected.
func checkInputConstraints(node map[string]any, path string, missing *[]inputConstraintViolation) {
	types := schemaTypeSet(node["type"])
	if types["string"] {
		if _, ok := node["minLength"]; !ok {
			*missing = append(*missing, inputConstraintViolation{location: path, missing: "minLength"})
		}
		if _, ok := node["maxLength"]; !ok {
			*missing = append(*missing, inputConstraintViolation{location: path, missing: "maxLength"})
		}
		appendSchemaBoundRelationViolation(node, path, "minLength", "maxLength", missing)
	}
	if types["integer"] || types["number"] {
		if _, ok := node["minimum"]; !ok {
			*missing = append(*missing, inputConstraintViolation{location: path, missing: "minimum"})
		}
		if _, ok := node["maximum"]; !ok {
			*missing = append(*missing, inputConstraintViolation{location: path, missing: "maximum"})
		}
		appendSchemaBoundRelationViolation(node, path, "minimum", "maximum", missing)
	}
}

func schemaTypeSet(raw any) map[string]bool {
	types := map[string]bool{}
	switch val := raw.(type) {
	case string:
		types[val] = true
	case []any:
		for _, item := range val {
			if typ, ok := item.(string); ok {
				types[typ] = true
			}
		}
	}
	return types
}

func appendSchemaBoundRelationViolation(node map[string]any, path, minKey, maxKey string, out *[]inputConstraintViolation) {
	min, hasMin := schemaNumericFacet(node, minKey)
	max, hasMax := schemaNumericFacet(node, maxKey)
	if !hasMin || !hasMax || min <= max {
		return
	}
	*out = append(*out, inputConstraintViolation{
		location:  path,
		relMin:    minKey,
		relMax:    maxKey,
		issueType: IssueInvalid,
	})
}

func schemaNumericFacet(node map[string]any, key string) (float64, bool) {
	switch val := node[key].(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case json.Number:
		parsed, err := val.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func walkSchemaTreeDepthInput(node map[string]any, path string, visit func(node map[string]any, path string)) error {
	return walkSchemaTreeDepthInputRoot(node, node, path, 0, map[string]bool{}, visit)
}

func walkSchemaTreeDepthInputRoot(
	root, node map[string]any, path string, depth int,
	seenRefs map[string]bool, visit func(node map[string]any, path string),
) error {
	if depth > 32 {
		return &schemaWalkError{path: path, msg: "schema walk exceeded maximum depth 32"}
	}
	visit(node, path)
	if ref, ok := node["$ref"].(string); ok && !seenRefs[ref] {
		if !strings.HasPrefix(ref, "#/") {
			return &schemaWalkError{path: path, msg: fmt.Sprintf("non-local $ref %q is not supported by FMT-25", ref)}
		}
		target, ok := resolveLocalSchemaRef(root, ref)
		if !ok {
			return &schemaWalkError{path: path, msg: fmt.Sprintf("unresolved local $ref %q", ref)}
		}
		seenRefs[ref] = true
		if err := walkSchemaTreeDepthInputRoot(root, target, path, depth+1, seenRefs, visit); err != nil {
			return err
		}
		delete(seenRefs, ref)
	}
	if err := walkSchemaInputMapChildren(root, node, path, depth, seenRefs, visit); err != nil {
		return err
	}
	if err := walkSchemaInputObjectChildren(root, node, path, depth, seenRefs, visit); err != nil {
		return err
	}
	return walkSchemaInputArrayChildren(root, node, path, depth, seenRefs, visit)
}

func walkSchemaInputMapChildren(
	root, node map[string]any, path string, depth int,
	seenRefs map[string]bool, visit func(node map[string]any, path string),
) error {
	for _, keyword := range []string{"properties", "patternProperties", "dependentSchemas"} {
		if err := walkSchemaInputMapKeywordChildren(root, node, path, keyword, depth, seenRefs, visit); err != nil {
			return err
		}
	}
	return nil
}

func walkSchemaInputMapKeywordChildren(
	root, node map[string]any, path, keyword string, depth int,
	seenRefs map[string]bool, visit func(node map[string]any, path string),
) error {
	children, ok := node[keyword].(map[string]any)
	if !ok {
		return nil
	}
	for _, key := range sortedAnyMapKeys(children) {
		child, ok := children[key].(map[string]any)
		if !ok {
			continue
		}
		childPath := schemaInputMapChildPath(path, keyword, key)
		if err := walkSchemaTreeDepthInputRoot(root, child, childPath, depth+1, seenRefs, visit); err != nil {
			return err
		}
	}
	return nil
}

func schemaInputMapChildPath(path, keyword, key string) string {
	if keyword == "properties" {
		return path + "." + key
	}
	return path + "." + keyword + "." + key
}

func walkSchemaInputObjectChildren(
	root, node map[string]any, path string, depth int,
	seenRefs map[string]bool, visit func(node map[string]any, path string),
) error {
	for _, keyword := range []string{
		"items",
		"additionalProperties",
		"contains",
		"propertyNames",
		"not",
		"if",
		"then",
		"else",
		"unevaluatedProperties",
		"unevaluatedItems",
	} {
		if child, ok := node[keyword].(map[string]any); ok {
			if err := walkSchemaTreeDepthInputRoot(root, child, path+"."+keyword, depth+1, seenRefs, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

func walkSchemaInputArrayChildren(
	root, node map[string]any, path string, depth int,
	seenRefs map[string]bool, visit func(node map[string]any, path string),
) error {
	for _, keyword := range []string{"allOf", "anyOf", "oneOf", "prefixItems"} {
		children, ok := node[keyword].([]any)
		if !ok {
			continue
		}
		for i, val := range children {
			if child, ok := val.(map[string]any); ok {
				childPath := fmt.Sprintf("%s.%s[%d]", path, keyword, i)
				if err := walkSchemaTreeDepthInputRoot(root, child, childPath, depth+1, seenRefs, visit); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// checkParamSchemaConstraints scans a map of ParamSchema (queryParams or
// pathParams) and emits violations for missing constraints. paramKind is the
// label used in the error message ("queryParams" or "pathParams").
//
// String params with Format == "uuid" are exempt from minLength / maxLength
// enforcement: RFC 4122 fixes UUIDs at 36 characters, so schema-level length
// constraints are redundant. The exemption applies only to length checks;
// integer constraints still apply unconditionally.
func (v *Validator) checkParamSchemaConstraints(
	c *metadata.ContractMeta, params map[string]metadata.ParamSchema, paramKind string,
) []ValidationResult {
	if len(params) == 0 {
		return nil
	}
	// Stable iteration order for deterministic output.
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)

	var results []ValidationResult
	for _, name := range names {
		fieldBase := fmt.Sprintf("endpoints.http.%s.%s", paramKind, name)
		results = append(results, v.checkSingleParamConstraints(c, params[name], fieldBase)...)
	}
	return results
}

// checkSingleParamConstraints checks one ParamSchema for missing min/max
// declarations. Branches on Type (string vs integer/number); other types are
// untouched. Format == "uuid" exempts string params from length checks.
func (v *Validator) checkSingleParamConstraints(c *metadata.ContractMeta, p metadata.ParamSchema, field string) []ValidationResult {
	switch p.Type {
	case "string":
		if p.Format == "uuid" {
			return nil // RFC 4122 fixes length; schema-level constraint is redundant.
		}
		results := v.emitMissingFacets(c, field, []missingFacet{
			{p.MinLength == nil, "minLength"},
			{p.MaxLength == nil, "maxLength"},
		})
		return append(results, v.emitInvalidParamRelation(c, field, "minLength", p.MinLength, "maxLength", p.MaxLength)...)
	case "integer", "number":
		results := v.emitMissingFacets(c, field, []missingFacet{
			{p.Minimum == nil, "minimum"},
			{p.Maximum == nil, "maximum"},
		})
		return append(results, v.emitInvalidParamRelation(c, field, "minimum", p.Minimum, "maximum", p.Maximum)...)
	}
	return nil
}

// missingFacet describes a single facet check: when present is false, emit a
// violation naming the facet (e.g. "minLength").
type missingFacet struct {
	missing bool
	name    string
}

// emitMissingFacets returns one ValidationResult per missing facet.
func (v *Validator) emitMissingFacets(
	c *metadata.ContractMeta, fieldBase string, facets []missingFacet,
) []ValidationResult {
	var results []ValidationResult
	for _, f := range facets {
		if !f.missing {
			continue
		}
		field := fieldBase + "." + f.name
		results = append(results, v.newError(
			codeFMT25, IssueRequired,
			contractFile(c), field,
			fmt.Sprintf("contract %q %s missing %s", c.ID, fieldBase, f.name),
			fmt.Sprintf("declare %s on %s in contract.yaml (defends against unbounded inputs)", f.name, fieldBase),
		))
	}
	return results
}

func (v *Validator) emitInvalidParamRelation(
	c *metadata.ContractMeta, fieldBase, minName string, min *int, maxName string, max *int,
) []ValidationResult {
	if min == nil || max == nil || *min <= *max {
		return nil
	}
	return []ValidationResult{v.newError(
		codeFMT25, IssueInvalid,
		contractFile(c), fieldBase,
		fmt.Sprintf("contract %q %s has %s > %s", c.ID, fieldBase, minName, maxName),
		fmt.Sprintf("ensure %s <= %s on %s in contract.yaml", minName, maxName, fieldBase),
	)}
}
