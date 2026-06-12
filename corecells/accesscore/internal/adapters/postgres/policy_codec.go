package postgres

// policy_codec.go — the durable JSON representation of an ABAC policy's rule list
// (#1346 PR-8). The `policies.rules` JSONB column stores rules with explicit
// string enum codes (effect:"allow", op:"eq", source:"subject", rowScope:"self")
// rather than the in-memory uint8 ordinals.
//
// Why string codes (not json.Marshal of the domain structs): the abac/authz
// enums are iota-based uint8. Persisting ordinals would mean that reordering an
// iota const block silently changes the meaning of every stored policy
// (allow↔deny) — a corruption that Policy.Validate cannot detect because both
// values stay individually valid. String codes are self-describing and
// reorder-proof, matching every reference policy engine's persistence (Cedar
// text / XACML URNs / OPA Rego / OpenFGA JSON). Decode is fail-closed: an unknown
// code is an error, so a corrupt or forward-incompatible row fails the policy
// load (and the PR-7 evaluator then denies) instead of silently mis-deciding.
//
// The enum→code mapping is frozen by policy_codec_test.go (golden + exhaustive
// round-trip + anti-vacuity). The encode direction delegates to each enum's
// String() method; the decode direction delegates to the domain Parse* helpers
// (authz.ParseEffect, abac.ParseOperator, abac.ParseAttributeSource,
// tenant.ParseRowScope) — single-sourced so the PG codec and the policy HTTP
// handler share the same wire↔enum conversion logic.

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// invertCodeMap derives the reverse (code→enum) map from a forward (enum→code)
// map. The forward map is the single source of truth for each enum's persisted
// codes; deriving the reverse guarantees the two directions can never drift.
//
// Precondition: the forward map's values (codes) MUST be unique. Two enum values
// sharing one code would silently lose one entry in the reverse map. This is
// guarded by TestPolicyCodec_CodesAreUnique, not enforced here, so the helper
// stays a pure derivation.
func invertCodeMap[E comparable](forward map[E]string) map[string]E {
	rev := make(map[string]E, len(forward))
	for e, code := range forward {
		rev[code] = e
	}
	return rev
}

// effectToCode / operatorToCode / sourceToCode / rowScopeToCode are retained as
// golden anchors for the codec test (TestPolicyCodec_GoldenCodeMaps). The
// encode functions delegate to the domain String() methods; the decode functions
// delegate to the domain Parse* helpers. The maps exist only so the test can
// assert that the domain code set matches the frozen persistence format.
var (
	effectToCode = map[authz.Effect]string{
		authz.EffectAllow: "allow",
		authz.EffectDeny:  "deny",
	}
	effectFromCode = invertCodeMap(effectToCode)

	operatorToCode = map[abac.Operator]string{
		abac.OpEquals:    "eq",
		abac.OpNotEquals: "neq",
		abac.OpIn:        "in",
		abac.OpNotIn:     "not_in",
	}
	operatorFromCode = invertCodeMap(operatorToCode)

	sourceToCode = map[abac.AttributeSource]string{
		abac.SourceSubject:     "subject",
		abac.SourceResource:    "resource",
		abac.SourceEnvironment: "environment",
	}
	sourceFromCode = invertCodeMap(sourceToCode)

	// rowScopeToCode maps only the NON-zero RowScope values. The zero value is a
	// valid obligation meaning "no row-scope constraint" (authz.Obligations); it
	// encodes to "" and is handled explicitly in encode/decodeRowScope, not here.
	rowScopeToCode = map[tenant.RowScope]string{
		tenant.RowScopeSelf:   "self",
		tenant.RowScopeDevice: "device",
		tenant.RowScopeTenant: "tenant",
		tenant.RowScopeAll:    "all",
	}
	rowScopeFromCode = invertCodeMap(rowScopeToCode)
)

// ─── persistence DTOs (string-coded; camelCase JSON) ───────────────────────

type ruleJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Action is the optional action-target set (PR-10a #1348). Persisted with
	// omitempty: an untargeted rule (nil Action = match-all) writes no `action`
	// key, so existing rows (authored before the field existed) stay byte-identical
	// and an action-scoped rule round-trips its target instead of silently
	// widening to match-all on read (F3). Decode is bounded by DisallowUnknownFields
	// like every other field.
	Action      []string        `json:"action,omitempty"`
	Effect      string          `json:"effect"`
	Conditions  []conditionJSON `json:"conditions,omitempty"`
	Obligations obligationsJSON `json:"obligations"`
}

type conditionJSON struct {
	Source   string   `json:"source"`
	Key      string   `json:"key"`
	Operator string   `json:"op"`
	Values   []string `json:"values"`
}

type obligationsJSON struct {
	// RowScope is "" when the rule imposes no row-scope obligation (zero value).
	RowScope  string   `json:"rowScope,omitempty"`
	FieldMask []string `json:"fieldMask,omitempty"`
}

// ─── enum codecs (fail-closed) ─────────────────────────────────────────────
//
// Encode delegates to each enum's String() method; decode delegates to the
// corresponding domain Parse* helper. The maps above (effectToCode etc.) serve
// as golden anchors for the codec test only — they are NOT the encode path.

func encodeEffect(e authz.Effect) (string, error) {
	if _, ok := effectToCode[e]; !ok {
		return "", fmt.Errorf("policy_codec: cannot encode unknown effect %d", uint8(e))
	}
	return e.String(), nil
}

func decodeEffect(c string) (authz.Effect, error) {
	e, err := authz.ParseEffect(c)
	if err != nil {
		return 0, fmt.Errorf("policy_codec: unknown effect code %q", c)
	}
	return e, nil
}

func encodeOperator(op abac.Operator) (string, error) {
	if _, ok := operatorToCode[op]; !ok {
		return "", fmt.Errorf("policy_codec: cannot encode unknown operator %d", uint8(op))
	}
	return op.String(), nil
}

func decodeOperator(c string) (abac.Operator, error) {
	op, err := abac.ParseOperator(c)
	if err != nil {
		return 0, fmt.Errorf("policy_codec: unknown operator code %q", c)
	}
	return op, nil
}

func encodeSource(s abac.AttributeSource) (string, error) {
	if _, ok := sourceToCode[s]; !ok {
		return "", fmt.Errorf("policy_codec: cannot encode unknown attribute source %d", uint8(s))
	}
	return s.String(), nil
}

func decodeSource(c string) (abac.AttributeSource, error) {
	s, err := abac.ParseAttributeSource(c)
	if err != nil {
		return 0, fmt.Errorf("policy_codec: unknown attribute source code %q", c)
	}
	return s, nil
}

// encodeRowScope maps the zero value (no obligation) to "" and every other valid
// scope to its frozen code via String(); an out-of-range value is an error (fail-closed).
func encodeRowScope(rs tenant.RowScope) (string, error) {
	if rs == 0 {
		return "", nil
	}
	if _, ok := rowScopeToCode[rs]; !ok {
		return "", fmt.Errorf("policy_codec: cannot encode unknown rowScope %d", uint8(rs))
	}
	return rs.String(), nil
}

// decodeRowScope delegates to tenant.ParseRowScope which already implements the
// empty-string → zero value (no obligation) semantics. An unknown non-empty code
// is an error (fail-closed).
func decodeRowScope(c string) (tenant.RowScope, error) {
	rs, err := tenant.ParseRowScope(c)
	if err != nil {
		return 0, fmt.Errorf("policy_codec: unknown rowScope code %q", c)
	}
	return rs, nil
}

// ─── rule list ⇄ JSONB ─────────────────────────────────────────────────────

// marshalRules encodes a rule list to the JSONB column representation. Returns an
// error (fail-closed) if any enum value is unknown, so a structurally-invalid
// policy can never be persisted with a silently-wrong code.
func marshalRules(rules []abac.Rule) ([]byte, error) {
	dtos := make([]ruleJSON, len(rules))
	for i, r := range rules {
		dto, err := encodeRule(r)
		if err != nil {
			return nil, err
		}
		dtos[i] = dto
	}
	return json.Marshal(dtos)
}

// unmarshalRules decodes the JSONB column representation back to a rule list.
// Returns an error (fail-closed) on malformed JSON, an unknown enum code, OR an
// unknown JSON field at any level (rule / condition / obligation). The
// DisallowUnknownFields decoder closes the schema field-set: a policy written by
// a future version that adds a security-bearing field would otherwise be read by
// an older binary with that field silently dropped (encoding/json ignores unknown
// keys by default) and authorized under the weaker old semantics. Rejecting the
// row instead routes through ErrPGSchemaShape → the evaluator denies (fail-closed).
//
// ref: encoding/json Decoder.DisallowUnknownFields; AWS Cedar tightened arbitrary
// fields from ignored to rejected.
func unmarshalRules(data []byte) ([]abac.Rule, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var dtos []ruleJSON
	if err := dec.Decode(&dtos); err != nil {
		return nil, fmt.Errorf("policy_codec: unmarshal rules: %w", err)
	}
	rules := make([]abac.Rule, len(dtos))
	for i, dto := range dtos {
		r, err := decodeRule(dto)
		if err != nil {
			return nil, err
		}
		rules[i] = r
	}
	return rules, nil
}

func encodeRule(r abac.Rule) (ruleJSON, error) {
	effectCode, err := encodeEffect(r.Effect)
	if err != nil {
		return ruleJSON{}, err
	}
	conds, err := encodeConditions(r.Conditions)
	if err != nil {
		return ruleJSON{}, err
	}
	rowScopeCode, err := encodeRowScope(r.Obligations.RowScope)
	if err != nil {
		return ruleJSON{}, err
	}
	return ruleJSON{
		ID:         r.ID,
		Name:       r.Name,
		Action:     r.Action,
		Effect:     effectCode,
		Conditions: conds,
		Obligations: obligationsJSON{
			RowScope:  rowScopeCode,
			FieldMask: r.Obligations.FieldMask.Fields,
		},
	}, nil
}

func encodeConditions(conds []abac.Condition) ([]conditionJSON, error) {
	if len(conds) == 0 {
		return nil, nil
	}
	out := make([]conditionJSON, len(conds))
	for i, c := range conds {
		srcCode, err := encodeSource(c.Source)
		if err != nil {
			return nil, err
		}
		opCode, err := encodeOperator(c.Operator)
		if err != nil {
			return nil, err
		}
		out[i] = conditionJSON{Source: srcCode, Key: c.Key, Operator: opCode, Values: c.Values}
	}
	return out, nil
}

func decodeRule(dto ruleJSON) (abac.Rule, error) {
	effect, err := decodeEffect(dto.Effect)
	if err != nil {
		return abac.Rule{}, err
	}
	conds, err := decodeConditions(dto.Conditions)
	if err != nil {
		return abac.Rule{}, err
	}
	rowScope, err := decodeRowScope(dto.Obligations.RowScope)
	if err != nil {
		return abac.Rule{}, err
	}
	var fm authz.FieldMask
	if len(dto.Obligations.FieldMask) > 0 {
		fm = authz.FieldMask{Fields: dto.Obligations.FieldMask}
	}
	return abac.Rule{
		ID:         dto.ID,
		Name:       dto.Name,
		Action:     dto.Action,
		Effect:     effect,
		Conditions: conds,
		Obligations: authz.Obligations{
			RowScope:  rowScope,
			FieldMask: fm,
		},
	}, nil
}

func decodeConditions(conds []conditionJSON) ([]abac.Condition, error) {
	if len(conds) == 0 {
		return nil, nil
	}
	out := make([]abac.Condition, len(conds))
	for i, c := range conds {
		src, err := decodeSource(c.Source)
		if err != nil {
			return nil, err
		}
		op, err := decodeOperator(c.Operator)
		if err != nil {
			return nil, err
		}
		out[i] = abac.Condition{Source: src, Key: c.Key, Operator: op, Values: c.Values}
	}
	return out, nil
}
