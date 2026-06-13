package postgres

// policy_codec_test.go — AI-robust guard for the durable policy JSON format
// (#1346 PR-8, plan C2). The string-coded `policies.rules` representation is an
// implicit constraint (the on-disk format of every stored policy); these tests
// keep it from drifting into a Soft "remember to add a code" convention:
//
//   - Golden (Hard-on-diff): the enum→code maps and a full rule's JSON bytes are
//     frozen. Changing a code (e.g. "allow"→"permit") — which would silently
//     break every stored policy — is an immediate test failure.
//   - Exhaustive round-trip (Medium, anti-drift): every valid value of each enum
//     must encode to a non-empty code and decode back to itself. A new enum value
//     added without a codec entry makes encode error → test RED. Anti-vacuity:
//     the iterated count is asserted ≥ the known value count.
//   - Fail-closed: unknown codes (decode) and out-of-range enums (encode) error.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// ─── golden: frozen enum→code maps ─────────────────────────────────────────

func TestPolicyCodec_GoldenCodeMaps(t *testing.T) {
	assert.Equal(t, map[authz.Effect]string{
		authz.EffectAllow: "allow",
		authz.EffectDeny:  "deny",
	}, effectToCode, "effect codes are a durable format — a change breaks every stored policy")

	assert.Equal(t, map[abac.Operator]string{
		abac.OpEquals:     "eq",
		abac.OpNotEquals:  "neq",
		abac.OpIn:         "in",
		abac.OpNotIn:      "not_in",
		abac.OpEqualsAttr: "eq_attr",
	}, operatorToCode, "operator codes are frozen")

	assert.Equal(t, map[abac.AttributeSource]string{
		abac.SourceSubject:     "subject",
		abac.SourceResource:    "resource",
		abac.SourceEnvironment: "environment",
	}, sourceToCode, "attribute source codes are frozen")

	assert.Equal(t, map[tenant.RowScope]string{
		tenant.RowScopeSelf:   "self",
		tenant.RowScopeDevice: "device",
		tenant.RowScopeTenant: "tenant",
		tenant.RowScopeAll:    "all",
	}, rowScopeToCode, "rowScope codes are frozen")
}

// TestPolicyCodec_CodesAreUnique guards invertCodeMap's precondition: each
// forward map's codes are distinct, so the derived reverse map loses no entry. A
// duplicate code (two enum values sharing one string) would silently corrupt the
// decode direction — caught here because the reverse map would then be shorter.
func TestPolicyCodec_CodesAreUnique(t *testing.T) {
	assert.Len(t, effectFromCode, len(effectToCode), "effect codes must be unique")
	assert.Len(t, operatorFromCode, len(operatorToCode), "operator codes must be unique")
	assert.Len(t, sourceFromCode, len(sourceToCode), "attribute source codes must be unique")
	assert.Len(t, rowScopeFromCode, len(rowScopeToCode), "rowScope codes must be unique")
}

// ─── exhaustive round-trip + anti-vacuity ──────────────────────────────────

func TestPolicyCodec_EffectExhaustiveRoundTrip(t *testing.T) {
	n := 0
	for e := authz.Effect(1); e.Valid(); e++ {
		n++
		code, err := encodeEffect(e)
		require.NoErrorf(t, err, "effect %d must encode", e)
		require.NotEmptyf(t, code, "effect %d code must be non-empty", e)
		got, err := decodeEffect(code)
		require.NoErrorf(t, err, "code %q must decode", code)
		require.Equalf(t, e, got, "effect %d must round-trip", e)
	}
	require.GreaterOrEqual(t, n, 2, "anti-vacuity: every valid Effect must be exercised")
}

func TestPolicyCodec_OperatorExhaustiveRoundTrip(t *testing.T) {
	n := 0
	for op := abac.Operator(1); op.Valid(); op++ {
		n++
		code, err := encodeOperator(op)
		require.NoErrorf(t, err, "operator %d must encode", op)
		require.NotEmpty(t, code)
		got, err := decodeOperator(code)
		require.NoError(t, err)
		require.Equalf(t, op, got, "operator %d must round-trip", op)
	}
	require.GreaterOrEqual(t, n, 5, "anti-vacuity: every valid Operator must be exercised (5 after eq_attr)")
}

func TestPolicyCodec_SourceExhaustiveRoundTrip(t *testing.T) {
	n := 0
	for s := abac.AttributeSource(1); s.Valid(); s++ {
		n++
		code, err := encodeSource(s)
		require.NoErrorf(t, err, "source %d must encode", s)
		require.NotEmpty(t, code)
		got, err := decodeSource(code)
		require.NoError(t, err)
		require.Equalf(t, s, got, "source %d must round-trip", s)
	}
	require.GreaterOrEqual(t, n, 3, "anti-vacuity: every valid AttributeSource must be exercised")
}

func TestPolicyCodec_RowScopeExhaustiveRoundTrip(t *testing.T) {
	// Zero value (no obligation) round-trips through the empty code.
	code, err := encodeRowScope(0)
	require.NoError(t, err)
	require.Equal(t, "", code, "zero RowScope (no obligation) encodes to empty")
	rs, err := decodeRowScope("")
	require.NoError(t, err)
	require.Equal(t, tenant.RowScope(0), rs, "empty code decodes to zero RowScope")

	n := 0
	for s := tenant.RowScope(1); s.Valid(); s++ {
		n++
		c, err := encodeRowScope(s)
		require.NoErrorf(t, err, "rowScope %d must encode", s)
		require.NotEmpty(t, c)
		got, err := decodeRowScope(c)
		require.NoError(t, err)
		require.Equalf(t, s, got, "rowScope %d must round-trip", s)
	}
	require.GreaterOrEqual(t, n, 4, "anti-vacuity: every valid RowScope must be exercised")
}

// ─── fail-closed ───────────────────────────────────────────────────────────

func TestPolicyCodec_UnknownCodeDecodeFails(t *testing.T) {
	_, err := decodeEffect("permit")
	assert.Error(t, err, "unknown effect code must fail closed")
	_, err = decodeOperator("contains")
	assert.Error(t, err)
	_, err = decodeSource("action")
	assert.Error(t, err)
	_, err = decodeRowScope("global")
	assert.Error(t, err)
}

// TestPolicyCodec_UnknownFieldRejected proves the field-set is closed: an unknown
// JSON key at any nesting level (rule / condition / obligation) fails decode, so a
// forward-incompatible policy can never be read under silently-weakened semantics.
func TestPolicyCodec_UnknownFieldRejected(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"rule-level", `[{"id":"r1","name":"x","effect":"allow","bogus":1}]`},
		{"condition-level", `[{"id":"r1","name":"x","effect":"allow","conditions":` +
			`[{"source":"subject","key":"d","op":"eq","values":["e"],"bogus":1}]}]`},
		{"obligation-level", `[{"id":"r1","name":"x","effect":"allow","obligations":{"rowScope":"self","bogus":1}}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := unmarshalRules([]byte(tc.raw))
			assert.Error(t, err, "unknown JSON field must be rejected (fail-closed)")
		})
	}
	// Sanity: the same shapes WITHOUT the unknown field decode cleanly.
	_, err := unmarshalRules([]byte(`[{"id":"r1","name":"x","effect":"allow","conditions":` +
		`[{"source":"subject","key":"d","op":"eq","values":["e"]}],"obligations":{"rowScope":"self"}}]`))
	require.NoError(t, err, "well-formed rule must still decode")
}

func TestPolicyCodec_OutOfRangeEnumEncodeFails(t *testing.T) {
	_, err := encodeEffect(0)
	assert.Error(t, err, "zero Effect must fail closed")
	_, err = encodeEffect(99)
	assert.Error(t, err)
	_, err = encodeOperator(0)
	assert.Error(t, err)
	_, err = encodeSource(0)
	assert.Error(t, err)
	_, err = encodeRowScope(99)
	assert.Error(t, err)
}

// ─── full rule list round-trip + golden JSON bytes ─────────────────────────

func TestPolicyCodec_RulesRoundTrip(t *testing.T) {
	rules := []abac.Rule{
		{
			ID:   "r1",
			Name: "Allow eng non-secret",
			// Action-scoped (PR-10a #1348): the target must survive the round-trip,
			// not silently widen to match-all (F3).
			Action: []string{"audit:read", "audit:export"},
			Effect: authz.EffectAllow,
			Conditions: []abac.Condition{
				{Source: abac.SourceSubject, Key: "department", Operator: abac.OpEquals, Values: []string{"eng"}},
				{Source: abac.SourceResource, Key: "classification", Operator: abac.OpNotEquals, Values: []string{"secret"}},
			},
			Obligations: authz.Obligations{
				RowScope:  tenant.RowScopeSelf,
				FieldMask: authz.FieldMask{Fields: []string{"ssn", "email"}},
			},
		},
		{
			// No action, conditions, or obligations — exercises the nil/empty
			// branches (untargeted match-all rule, nil Action stays nil on read).
			ID:     "r2",
			Name:   "Deny all else",
			Effect: authz.EffectDeny,
		},
	}

	data, err := marshalRules(rules)
	require.NoError(t, err)
	got, err := unmarshalRules(data)
	require.NoError(t, err)
	require.Equal(t, rules, got, "rule list must survive marshal→unmarshal unchanged")
}

func TestPolicyCodec_GoldenJSON(t *testing.T) {
	rules := []abac.Rule{
		{
			ID:     "r1",
			Name:   "Allow eng",
			Effect: authz.EffectAllow,
			Conditions: []abac.Condition{
				{Source: abac.SourceSubject, Key: "department", Operator: abac.OpEquals, Values: []string{"eng"}},
			},
			Obligations: authz.Obligations{
				RowScope:  tenant.RowScopeSelf,
				FieldMask: authz.FieldMask{Fields: []string{"email"}},
			},
		},
	}
	data, err := marshalRules(rules)
	require.NoError(t, err)

	// This rule has no Action: the omitempty `action` key is absent, so rows
	// authored before the field existed serialize byte-identically (no migration).
	const want = `[{"id":"r1","name":"Allow eng","effect":"allow",` +
		`"conditions":[{"source":"subject","key":"department","op":"eq","values":["eng"]}],` +
		`"obligations":{"rowScope":"self","fieldMask":["email"]}}]`
	assert.JSONEq(t, want, string(data), "the durable JSON shape is frozen — a drift here breaks stored policies")
}

// TestPolicyCodec_GoldenJSON_ActionScoped freezes the durable shape of an
// action-scoped rule (PR-10a #1348): the `action` key carries the target set so
// it round-trips instead of decoding back to nil match-all (F3).
func TestPolicyCodec_GoldenJSON_ActionScoped(t *testing.T) {
	rules := []abac.Rule{
		{
			ID:     "r1",
			Name:   "Allow audit read",
			Action: []string{"audit:read"},
			Effect: authz.EffectAllow,
		},
	}
	data, err := marshalRules(rules)
	require.NoError(t, err)

	const want = `[{"id":"r1","name":"Allow audit read","action":["audit:read"],` +
		`"effect":"allow","obligations":{}}]`
	assert.JSONEq(t, want, string(data), "action-scoped rule durable JSON shape is frozen")
}

// ─── cross-attribute operator codec (Batch C, #1977) ──────────────────────

// TestPolicyCodec_CrossAttrCondition_RoundTrip asserts that a rule carrying a
// cross-attribute condition (OpEqualsAttr, RHSSource=resource, RHSKey="id")
// survives encode→decode with RHSSource/RHSKey preserved and Values empty.
func TestPolicyCodec_CrossAttrCondition_RoundTrip(t *testing.T) {
	rules := []abac.Rule{
		{
			ID:     "r-cross",
			Name:   "Subject is resource",
			Effect: authz.EffectAllow,
			Conditions: []abac.Condition{
				{
					Source:    abac.SourceSubject,
					Key:       "sub",
					Operator:  abac.OpEqualsAttr,
					RHSSource: abac.SourceResource,
					RHSKey:    "id",
				},
			},
		},
	}

	data, err := marshalRules(rules)
	require.NoError(t, err, "encode of cross-attr condition must succeed")

	got, err := unmarshalRules(data)
	require.NoError(t, err, "decode of cross-attr condition must succeed")

	require.Len(t, got, 1)
	require.Len(t, got[0].Conditions, 1)
	cond := got[0].Conditions[0]
	assert.Equal(t, abac.SourceSubject, cond.Source)
	assert.Equal(t, "sub", cond.Key)
	assert.Equal(t, abac.OpEqualsAttr, cond.Operator)
	assert.Equal(t, abac.SourceResource, cond.RHSSource, "RHSSource must survive round-trip")
	assert.Equal(t, "id", cond.RHSKey, "RHSKey must survive round-trip")
	assert.Empty(t, cond.Values, "Values must remain empty for cross-attr condition")
}

// TestPolicyCodec_StaticCondition_NoRHSFields asserts that a static condition
// (OpEquals + Values) does NOT emit rhsSource/rhsKey keys in the persisted JSON
// — static conditions must stay byte-identical with rows stored before #1977.
func TestPolicyCodec_StaticCondition_NoRHSFields(t *testing.T) {
	rules := []abac.Rule{
		{
			ID:     "r-static",
			Name:   "Static allow",
			Effect: authz.EffectAllow,
			Conditions: []abac.Condition{
				{
					Source:   abac.SourceSubject,
					Key:      "department",
					Operator: abac.OpEquals,
					Values:   []string{"eng"},
				},
			},
			Obligations: authz.Obligations{},
		},
	}

	data, err := marshalRules(rules)
	require.NoError(t, err)

	// The JSON for a static condition must not contain rhsSource or rhsKey keys.
	jsonStr := string(data)
	assert.NotContains(t, jsonStr, "rhsSource",
		"static condition must not emit rhsSource key (omitempty)")
	assert.NotContains(t, jsonStr, "rhsKey",
		"static condition must not emit rhsKey key (omitempty)")
}

// TestPolicyCodec_OldRowWithoutRHSFields asserts back-compat: a JSONB row
// serialised BEFORE #1977 (no rhsSource/rhsKey keys) still decodes cleanly for
// static operators, with RHSSource==0 and RHSKey=="".
func TestPolicyCodec_OldRowWithoutRHSFields(t *testing.T) {
	const oldRow = `[{"id":"r1","name":"Legacy rule","effect":"allow",` +
		`"conditions":[{"source":"subject","key":"department","op":"eq","values":["eng"]}],` +
		`"obligations":{}}]`

	got, err := unmarshalRules([]byte(oldRow))
	require.NoError(t, err, "legacy row without rhs fields must decode without error")

	require.Len(t, got, 1)
	require.Len(t, got[0].Conditions, 1)
	cond := got[0].Conditions[0]
	assert.Equal(t, abac.OpEquals, cond.Operator)
	assert.Equal(t, abac.AttributeSource(0), cond.RHSSource, "RHSSource must be zero for legacy row")
	assert.Equal(t, "", cond.RHSKey, "RHSKey must be empty for legacy row")
}

// TestPolicyCodec_UnknownFieldRejected_ConditionLevel reconfirms that an
// unknown JSON field AT THE CONDITION LEVEL is still rejected after adding
// rhsSource/rhsKey. Prevents the new fields from accidentally opening the
// schema for arbitrary extra keys.
func TestPolicyCodec_UnknownFieldRejected_ConditionLevelAfterRHS(t *testing.T) {
	// rhsSource and rhsKey are now known — this must decode cleanly.
	const validWithRHS = `[{"id":"r1","name":"x","effect":"allow","conditions":` +
		`[{"source":"subject","key":"sub","op":"eq_attr","rhsSource":"resource","rhsKey":"id"}],` +
		`"obligations":{}}]`
	_, err := unmarshalRules([]byte(validWithRHS))
	require.NoError(t, err, "condition with known rhsSource/rhsKey must decode cleanly")

	// A genuinely unknown field must still be rejected.
	const unknownField = `[{"id":"r1","name":"x","effect":"allow","conditions":` +
		`[{"source":"subject","key":"sub","op":"eq_attr","rhsSource":"resource","rhsKey":"id","bogus":1}],` +
		`"obligations":{}}]`
	_, err = unmarshalRules([]byte(unknownField))
	assert.Error(t, err, "unknown field in condition must still be rejected (fail-closed)")
}
