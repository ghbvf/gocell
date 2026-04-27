package governance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/contracts"
)

// Rule ID constants for FMT-20..FMT-25. Extracted so that each rule ID string
// is declared in exactly one place; Sonar code-smell rule S1192 (duplicate
// string literals) no longer fires for these identifiers.
const (
	ruleFMT20 = "FMT-20"
	ruleFMT21 = "FMT-21"
	ruleFMT22 = "FMT-22"
	ruleFMT23 = "FMT-23"
	ruleFMT25 = "FMT-25"
)

// --- FMT-20 (formerly FMT-RESPONSE-STRICT-01) ---

// validateFMTResponseStrict01 scans every HTTP-kind contract's request/response
// JSON schemas. For each "type":"object" node in the schema (recursive), if it
// lacks "additionalProperties": false, a violation is emitted.
//
// Rule ID: FMT-20.
// Severity: Error, IssueRequired.
// ref: k8s.io/apiserver admission/plugin/schema — strict schema validation pattern.
func (v *Validator) validateFMTResponseStrict01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != "http" {
			continue
		}
		for _, schemaRef := range collectHTTPSchemaPaths(c) {
			absPath := filepath.Join(v.root, schemaRef)
			missing, err := scanSchemaForStrictMissing(absPath)
			if err != nil {
				if os.IsNotExist(err) {
					// Missing schema file is reported by FMT-09 / REF rules; skip here.
					continue
				}
				// Parse/IO errors are definitive FMT-20 violations (fail-closed).
				results = append(results, v.newResult(
					ruleFMT20, SeverityError, IssueInvalid,
					schemaRef, "$",
					fmt.Sprintf("contract %q schema %q failed to parse: %v", c.ID, schemaRef, err),
				))
				continue
			}
			for _, loc := range missing {
				results = append(results, v.newResult(
					ruleFMT20, SeverityError, IssueRequired,
					schemaRef, loc,
					fmt.Sprintf("contract %q schema must declare additionalProperties explicitly (true=open, false=strict) at %s", c.ID, loc),
				))
			}
		}
	}
	return results
}

// collectHTTPSchemaPaths returns the relative schema paths for an HTTP contract,
// resolved relative to the project root. It includes the top-level request and
// response refs from schemaRefs, plus the per-status schemaRef from
// endpoints.http.responses[*] so FMT-20 also covers error response schemas.
func collectHTTPSchemaPaths(c *metadata.ContractMeta) []string {
	var paths []string
	if c.SchemaRefs.Request != "" {
		paths = append(paths, filepath.Join(c.Dir, c.SchemaRefs.Request))
	}
	if c.SchemaRefs.Response != "" {
		paths = append(paths, filepath.Join(c.Dir, c.SchemaRefs.Response))
	}
	if c.Endpoints.HTTP != nil && len(c.Endpoints.HTTP.Responses) > 0 {
		// Sort by int key for deterministic violation ordering across runs.
		keys := make([]int, 0, len(c.Endpoints.HTTP.Responses))
		for k := range c.Endpoints.HTTP.Responses {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		for _, k := range keys {
			r := c.Endpoints.HTTP.Responses[k]
			if r.SchemaRef != "" {
				paths = append(paths, filepath.Join(c.Dir, r.SchemaRef))
			}
		}
	}
	return paths
}

// scanSchemaForStrictMissing reads a JSON schema file and recursively walks it.
// For every object node whose "type" equals "object", it checks whether
// "additionalProperties" is set to false. It collects JSON-pointer-style paths
// of violations (e.g. "$", "$.data", "$.data.items").
func scanSchemaForStrictMissing(absPath string) ([]string, error) {
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("invalid JSON schema %s: %w", absPath, err)
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

func walkSchemaTreeDepthRoot(root, node map[string]any, path string, depth int, seenRefs map[string]bool, visit func(node map[string]any, path string)) {
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

func walkSchemaNamedMapChildren(root, node map[string]any, path string, depth int, seenRefs map[string]bool, visit func(node map[string]any, path string)) {
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

func walkSchemaNamedObjectChildren(root, node map[string]any, path string, depth int, seenRefs map[string]bool, visit func(node map[string]any, path string)) {
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

func walkSchemaNamedArrayChildren(root, node map[string]any, path string, depth int, seenRefs map[string]bool, visit func(node map[string]any, path string)) {
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
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
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

// checkAdditionalProperties emits a violation when the node has no
// "additionalProperties" key at all. An explicit bool (true or false) is
// accepted — the schema author consciously opted in or out of extra properties.
// An object value (e.g. {"type":"string"}) is treated as missing because it is
// not a deliberate open/closed declaration.
func checkAdditionalProperties(node map[string]any, path string, missing *[]string) {
	ap, hasAP := node["additionalProperties"]
	if !hasAP {
		// No declaration at all — emit violation.
		*missing = append(*missing, path)
		return
	}
	// Explicit bool (true = open, false = strict) — author made a choice, accept.
	if _, ok := ap.(bool); ok {
		return
	}
	// Object value (schema form) is not a deliberate open/closed declaration.
	*missing = append(*missing, path)
}

// --- FMT-21 (formerly FMT-CONTRACT-DIR-ID-MATCH-01) ---

// validateFMTContractDirIDMatch01 checks that every contract's Dir matches the
// directory derived from its ID. For example, "http.auth.login.v1" must live at
// "contracts/http/auth/login/v1".
//
// Contracts under example projects (e.g. "examples/iotdevice/contracts/…") are
// accepted as long as the segment after the last "contracts/" separator matches
// the ID-derived suffix. A Dir that contains no "contracts/" component at all
// is itself a violation.
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
			results = append(results, v.newResult(
				ruleFMT21, SeverityError, IssueMismatch,
				contractFile(c), "id",
				fmt.Sprintf("contract %q dir %q does not match derived %q", c.ID, c.Dir, derived),
			))
			continue
		}
		actualSuffix := filepath.Join(parts[lastIdx:]...) // "contracts/http/auth/login/v1"
		if actualSuffix != derived {
			results = append(results, v.newResult(
				ruleFMT21, SeverityError, IssueMismatch,
				contractFile(c), "id",
				fmt.Sprintf("contract %q dir %q does not match derived %q", c.ID, c.Dir, derived),
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
			results = append(results, v.newResult(
				ruleFMT22, SeverityError, IssueInvalid,
				"journeys/status-board.yaml",
				fmt.Sprintf("[%d].state", i),
				fmt.Sprintf(
					"status-board entry %q state %q must be one of {todo, doing, done}",
					e.JourneyID, e.State,
				),
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
	now := v.now()
	for _, c := range v.project.Contracts {
		if c.Lifecycle != "deprecated" {
			continue
		}
		if c.DeprecatedAt == "" {
			results = append(results, v.newResult(
				ruleFMT23, SeverityError, IssueRequired,
				contractFile(c), "deprecatedAt",
				fmt.Sprintf("contract %q is deprecated but missing deprecatedAt", c.ID),
			))
			continue
		}
		ts, err := time.ParseInLocation("2006-01-02", c.DeprecatedAt, time.UTC)
		if err != nil {
			results = append(results, v.newResult(
				ruleFMT23, SeverityError, IssueInvalid,
				contractFile(c), "deprecatedAt",
				fmt.Sprintf("contract %q deprecatedAt %q is not YYYY-MM-DD", c.ID, c.DeprecatedAt),
			))
			continue
		}
		if now.UTC().Sub(ts) > 90*24*time.Hour {
			results = append(results, v.newResult(
				ruleFMT23, SeverityWarning, IssueForbidden,
				contractFile(c), "lifecycle",
				fmt.Sprintf(
					"contract %q has been deprecated for >90d (since %s); remove or extend",
					c.ID, c.DeprecatedAt,
				),
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
type inputConstraintViolation struct {
	location  string // JSON pointer or full metadata field path.
	missing   string // "minLength" | "maxLength" | "minimum" | "maximum"
	issueType IssueType
	message   string
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
	schemaFile := filepath.Join(c.Dir, c.SchemaRefs.Request)
	absPath := filepath.Join(v.root, schemaFile)
	missing, err := scanSchemaForInputConstraints(absPath)
	if err != nil && !os.IsNotExist(err) {
		field := "$"
		var walkErr *schemaWalkError
		if errors.As(err, &walkErr) {
			field = walkErr.path
		}
		return []ValidationResult{v.newResult(
			ruleFMT25, SeverityError, IssueInvalid,
			schemaFile, field,
			fmt.Sprintf("contract %q request schema %q failed to parse: %v",
				c.ID, c.SchemaRefs.Request, err),
		)}
	}
	var results []ValidationResult
	for _, viol := range missing {
		issueType := viol.issueType
		if issueType == "" {
			issueType = IssueRequired
		}
		msg := viol.message
		if msg == "" {
			msg = fmt.Sprintf("contract %q request schema field %s missing %s",
				c.ID, viol.location, viol.missing)
		}
		results = append(results, v.newResult(
			ruleFMT25, SeverityError, issueType,
			schemaFile, viol.location,
			msg,
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
		if p.Type == "" || !contracts.ParamTypes[p.Type] {
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
func scanSchemaForInputConstraints(absPath string) ([]inputConstraintViolation, error) {
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("invalid JSON schema %s: %w", absPath, err)
	}
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
		issueType: IssueInvalid,
		message:   fmt.Sprintf("request schema field %s has %s > %s", path, minKey, maxKey),
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

func walkSchemaTreeDepthInputRoot(root, node map[string]any, path string, depth int, seenRefs map[string]bool, visit func(node map[string]any, path string)) error {
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

func walkSchemaInputMapChildren(root, node map[string]any, path string, depth int, seenRefs map[string]bool, visit func(node map[string]any, path string)) error {
	for _, keyword := range []string{"properties", "patternProperties", "dependentSchemas"} {
		if err := walkSchemaInputMapKeywordChildren(root, node, path, keyword, depth, seenRefs, visit); err != nil {
			return err
		}
	}
	return nil
}

func walkSchemaInputMapKeywordChildren(root, node map[string]any, path, keyword string, depth int, seenRefs map[string]bool, visit func(node map[string]any, path string)) error {
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

func walkSchemaInputObjectChildren(root, node map[string]any, path string, depth int, seenRefs map[string]bool, visit func(node map[string]any, path string)) error {
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

func walkSchemaInputArrayChildren(root, node map[string]any, path string, depth int, seenRefs map[string]bool, visit func(node map[string]any, path string)) error {
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
func (v *Validator) checkParamSchemaConstraints(c *metadata.ContractMeta, params map[string]contracts.ParamSchema, paramKind string) []ValidationResult {
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
func (v *Validator) checkSingleParamConstraints(c *metadata.ContractMeta, p contracts.ParamSchema, field string) []ValidationResult {
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
func (v *Validator) emitMissingFacets(c *metadata.ContractMeta, fieldBase string, facets []missingFacet) []ValidationResult {
	var results []ValidationResult
	for _, f := range facets {
		if !f.missing {
			continue
		}
		field := fieldBase + "." + f.name
		results = append(results, v.newResult(
			ruleFMT25, SeverityError, IssueRequired,
			contractFile(c), field,
			fmt.Sprintf("contract %q %s missing %s", c.ID, fieldBase, f.name),
		))
	}
	return results
}

func (v *Validator) emitInvalidParamRelation(c *metadata.ContractMeta, fieldBase, minName string, min *int, maxName string, max *int) []ValidationResult {
	if min == nil || max == nil || *min <= *max {
		return nil
	}
	return []ValidationResult{v.newResult(
		ruleFMT25, SeverityError, IssueInvalid,
		contractFile(c), fieldBase,
		fmt.Sprintf("contract %q %s has %s > %s", c.ID, fieldBase, minName, maxName),
	)}
}
