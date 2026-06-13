package policymanage

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/corecells/internal/testoutbox"
	policyCreate "github.com/ghbvf/gocell/generated/contracts/http/policy/create/v1"
	policyUpdate "github.com/ghbvf/gocell/generated/contracts/http/policy/update/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

const testConvTenantStr = "30000000-0000-0000-0000-000000000001"

func testConvAdminCtx() context.Context {
	return ctxkeys.WithTenantID(auth.TestContext("conv-admin", []string{auth.RoleAdmin}), testConvTenantStr)
}

func newConverterTestService(t testing.TB) *Service {
	t.Helper()
	repo := mem.NewPolicyRepository()
	writer := &recordingWriter{}
	svc, err := NewService(clock.Real(), repo, testCursorCodec, slog.Default(), query.RunModeProd,
		WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, writer))),
		WithTxManager(persistence.WrapForCell(&noopTxRunner{})))
	require.NoError(t, err)
	return svc
}

// richCreateRules returns a rich create-contract rules slice with conditions
// and obligations populated. Exercises getSource/getKey/getOperator/getValues
// and parseObligationsFromWire on the create path.
func richCreateRules() []*policyCreate.RequestRulesItem {
	return []*policyCreate.RequestRulesItem{
		{
			ID:     "r1",
			Name:   "Rich allow rule",
			Effect: "allow",
			Conditions: []*policyCreate.RequestRulesItemConditionsItem{
				{
					Source:   "subject",
					Key:      "department",
					Operator: "eq",
					Values:   []string{"eng", "ops"},
				},
			},
			Obligations: &policyCreate.RequestRulesItemObligations{
				RowScope:  "tenant",
				FieldMask: []string{"email", "phone"},
			},
		},
	}
}

// richUpdateRules returns a rich update-contract rules slice with conditions
// and obligations populated. Exercises the update converter path.
func richUpdateRules(version int64) *policyUpdate.Request {
	return &policyUpdate.Request{
		ID:   "",
		Name: "Rich updated",
		Rules: []*policyUpdate.RequestRulesItem{
			{
				ID:     "r1",
				Name:   "Updated rich rule",
				Effect: "allow",
				Conditions: []*policyUpdate.RequestRulesItemConditionsItem{
					{
						Source:   "resource",
						Key:      "classification",
						Operator: "in",
						Values:   []string{"public", "internal"},
					},
				},
				Obligations: &policyUpdate.RequestRulesItemObligations{
					RowScope:  "self",
					FieldMask: []string{"name"},
				},
			},
		},
		ExpectedVersion: version,
	}
}

// TestConverter_RichRule_CreateRoundTrip exercises the full request→domain→wire
// conversion path for a rule with conditions and obligations via the service
// and the CreateAdapter. Covers:
//   - createCondWire.getSource/getKey/getOperator/getValues
//   - parseConditionWires (with data)
//   - createObligationsToDomain / parseObligationsFromWire (with rowScope+fieldMask)
//   - polCondsToCreateWire / polObsToCreateWire (with data)
func TestConverter_RichRule_CreateRoundTrip(t *testing.T) {
	svc := newConverterTestService(t)
	ctx := testConvAdminCtx()

	// Round-trip via adapter (exercises all create converter paths with real data).
	ad := CreateAdapter{s: svc}
	resp, err := ad.Create(ctx, &policyCreate.Request{
		Name:  "RichPolicy",
		Rules: richCreateRules(),
	})
	require.NoError(t, err)
	created, ok := resp.(policyCreate.Create201JSONResponse)
	require.True(t, ok, "expected Create201JSONResponse, got %T", resp)

	data := created.Data
	require.Len(t, data.Rules, 1, "must return one rule")
	rule := data.Rules[0]

	// Condition wire round-trip (operator "eq" is the wire spelling of OpEquals).
	require.Len(t, rule.Conditions, 1, "must return one condition")
	cond := rule.Conditions[0]
	assert.Equal(t, "subject", cond.Source)
	assert.Equal(t, "department", cond.Key)
	assert.Equal(t, "eq", cond.Operator)
	assert.Equal(t, []string{"eng", "ops"}, cond.Values)

	// Obligation wire round-trip.
	require.NotNil(t, rule.Obligations, "obligations must be non-nil")
	assert.Equal(t, "tenant", rule.Obligations.RowScope)
	assert.Equal(t, []string{"email", "phone"}, rule.Obligations.FieldMask)
}

// TestConverter_RichRule_GetRoundTrip verifies the domain→get wire path
// (polCondsToGetWire / polObsToGetWire) returns conditions and obligations.
func TestConverter_RichRule_GetRoundTrip(t *testing.T) {
	svc := newConverterTestService(t)
	ctx := testConvAdminCtx()

	// Create via service to populate the domain.
	rules := []abac.Rule{
		{
			ID:     "r1",
			Name:   "Rich rule",
			Effect: authz.EffectAllow,
			Conditions: []abac.Condition{
				{
					Source:   abac.SourceSubject,
					Key:      "department",
					Operator: abac.OpEquals,
					Values:   []string{"eng"},
				},
			},
			Obligations: authz.Obligations{
				RowScope:  3, // tenant
				FieldMask: authz.FieldMask{Fields: []string{"email"}},
			},
		},
	}
	p, err := svc.Create(ctx, CreateInput{Name: "GetRich", Rules: rules})
	require.NoError(t, err)

	got, err := svc.Get(ctx, p.ID)
	require.NoError(t, err)

	// Convert to get-wire and verify conditions and obligations are populated.
	wireData := policyToGetResponseData(got)
	require.Len(t, wireData.Rules, 1)
	rule := wireData.Rules[0]

	require.Len(t, rule.Conditions, 1, "get wire must return conditions")
	assert.Equal(t, "subject", rule.Conditions[0].Source)
	assert.Equal(t, "department", rule.Conditions[0].Key)
	assert.Equal(t, "eq", rule.Conditions[0].Operator) // OpEquals.String() == "eq"
	assert.Equal(t, []string{"eng"}, rule.Conditions[0].Values)

	require.NotNil(t, rule.Obligations, "get wire must return obligations")
	assert.Equal(t, "tenant", rule.Obligations.RowScope)
	assert.Equal(t, []string{"email"}, rule.Obligations.FieldMask)
}

// TestConverter_RichRule_UpdateRoundTrip exercises updateCondWire.getSource/getKey/
// getOperator/getValues, updateConditionsToDomain, updateObligationsToDomain,
// and polCondsToUpdateWire/polObsToUpdateWire.
func TestConverter_RichRule_UpdateRoundTrip(t *testing.T) {
	svc := newConverterTestService(t)
	ctx := testConvAdminCtx()

	// Create a minimal policy first.
	p, err := svc.Create(ctx, CreateInput{Name: "ToUpdate", Rules: minimalRules()})
	require.NoError(t, err)

	req := richUpdateRules(int64(p.Version))
	req.ID = p.ID

	ad := UpdateAdapter{s: svc}
	resp, err := ad.Update(ctx, req)
	require.NoError(t, err)
	updated, ok := resp.(policyUpdate.Update200JSONResponse)
	require.True(t, ok, "expected Update200JSONResponse, got %T", resp)

	data := updated.Data
	require.Len(t, data.Rules, 1, "must return one rule")
	rule := data.Rules[0]

	// Condition round-trip.
	require.Len(t, rule.Conditions, 1)
	assert.Equal(t, "resource", rule.Conditions[0].Source)
	assert.Equal(t, "classification", rule.Conditions[0].Key)
	assert.Equal(t, "in", rule.Conditions[0].Operator) // OpIn.String() == "in"
	assert.Equal(t, []string{"public", "internal"}, rule.Conditions[0].Values)

	// Obligation round-trip.
	require.NotNil(t, rule.Obligations)
	assert.Equal(t, "self", rule.Obligations.RowScope)
	assert.Equal(t, []string{"name"}, rule.Obligations.FieldMask)
}

// TestConverter_RichRule_ListRoundTrip verifies polCondsToListWire and
// polObsToListWire return populated data when the domain has conditions/obligations.
func TestConverter_RichRule_ListRoundTrip(t *testing.T) {
	svc := newConverterTestService(t)
	ctx := testConvAdminCtx()

	richRules := []abac.Rule{
		{
			ID:     "r1",
			Name:   "List rich",
			Effect: authz.EffectAllow,
			Conditions: []abac.Condition{
				{
					Source:   abac.SourceResource,
					Key:      "level",
					Operator: abac.OpIn,
					Values:   []string{"L3", "L4"},
				},
			},
			Obligations: authz.Obligations{
				FieldMask: authz.FieldMask{Fields: []string{"salary"}},
			},
		},
	}
	_, err := svc.Create(ctx, CreateInput{Name: "ListRich", Rules: richRules})
	require.NoError(t, err)

	result, err := svc.List(ctx, query.PageParams{Limit: 10})
	require.NoError(t, err)
	require.Len(t, result.Items, 1)

	wireItem := policyToListResponseDataItem(result.Items[0])
	require.Len(t, wireItem.Rules, 1)
	rule := wireItem.Rules[0]

	require.Len(t, rule.Conditions, 1)
	assert.Equal(t, "resource", rule.Conditions[0].Source)
	assert.Equal(t, "level", rule.Conditions[0].Key)
	assert.Equal(t, "in", rule.Conditions[0].Operator)
	assert.Equal(t, []string{"L3", "L4"}, rule.Conditions[0].Values)

	require.NotNil(t, rule.Obligations)
	assert.Equal(t, []string{"salary"}, rule.Obligations.FieldMask)
	assert.Empty(t, rule.Obligations.RowScope, "zero RowScope must not appear in wire")
}

// TestConverter_Obligations_ZeroValue verifies polObs*Wire returns nil when
// both RowScope and FieldMask are zero.
func TestConverter_Obligations_ZeroValue(t *testing.T) {
	cases := []struct {
		name string
		obs  authz.Obligations
	}{
		{"zero obligations", authz.Obligations{}},
		{"only zero rowScope no fieldMask", authz.Obligations{FieldMask: authz.FieldMask{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Nil(t, polObsToCreateWire(tc.obs), "create: zero obligations must produce nil wire")
			assert.Nil(t, polObsToGetWire(tc.obs), "get: zero obligations must produce nil wire")
			assert.Nil(t, polObsToUpdateWire(tc.obs), "update: zero obligations must produce nil wire")
			assert.Nil(t, polObsToListWire(tc.obs), "list: zero obligations must produce nil wire")
		})
	}
}

// TestConverter_ParseObligationsFromWire_EmptyRowScope verifies that an empty
// rowScope string is accepted (zero RowScope = no constraint).
func TestConverter_ParseObligationsFromWire_EmptyRowScope(t *testing.T) {
	obs, err := parseObligationsFromWire("", []string{"email"})
	require.NoError(t, err)
	assert.Equal(t, 0, int(obs.RowScope), "empty rowScope must yield zero value")
	assert.Equal(t, []string{"email"}, obs.FieldMask.Fields)
}

// TestConverter_ParseObligationsFromWire_GrantableScopes verifies every
// policy-authoring-grantable rowScope string is accepted. "all" is excluded —
// it is rejected (see TestConverter_ParseObligationsFromWire_RowScopeAllRejected).
func TestConverter_ParseObligationsFromWire_GrantableScopes(t *testing.T) {
	cases := []struct {
		rowScope string
	}{
		{"self"},
		{"device"},
		{"tenant"},
	}
	for _, tc := range cases {
		t.Run(tc.rowScope, func(t *testing.T) {
			obs, err := parseObligationsFromWire(tc.rowScope, nil)
			require.NoError(t, err)
			assert.Equal(t, tc.rowScope, obs.RowScope.String())
		})
	}
}

// TestConverter_ParseObligationsFromWire_RowScopeAllRejected verifies that
// rowScope=all — a valid wire vocabulary word but a cross-tenant obligation
// reserved for the audited super-admin path — is rejected with KindInvalid (422)
// when supplied via policy authoring. This closes the privilege-escalation gap
// where a plain admin could persist a RowScopeAll obligation bypassing the
// ROWSCOPEALL-AUDIT-FUNNEL-01 audit.
func TestConverter_ParseObligationsFromWire_RowScopeAllRejected(t *testing.T) {
	_, err := parseObligationsFromWire("all", nil)
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindInvalid, ce.Kind)
}

// TestConverter_ParseObligationsFromWire_InvalidRowScope verifies an invalid
// rowScope string returns KindInvalid.
func TestConverter_ParseObligationsFromWire_InvalidRowScope(t *testing.T) {
	_, err := parseObligationsFromWire("bogus-scope", nil)
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindInvalid, ce.Kind)
}

// TestConverter_InvalidConditionSource_Via_Adapter exercises the
// invalid-source → KindInvalid → 422 path (covers parseConditionWires error branch).
func TestConverter_InvalidConditionSource_ViaAdapter(t *testing.T) {
	svc := newConverterTestService(t)
	ctx := testConvAdminCtx()
	ad := CreateAdapter{s: svc}

	resp, err := ad.Create(ctx, &policyCreate.Request{
		Name: "BadSource",
		Rules: []*policyCreate.RequestRulesItem{
			{
				ID:     "r1",
				Name:   "Bad",
				Effect: "allow",
				Conditions: []*policyCreate.RequestRulesItemConditionsItem{
					{
						Source:   "invalid-source-xyz",
						Key:      "dept",
						Operator: "equals",
						Values:   []string{"eng"},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	_, ok := resp.(policyCreate.Create422ErrorResponse)
	assert.True(t, ok, "invalid condition source must yield 422, got %T", resp)
}

// TestConverter_InvalidConditionOperator_ViaAdapter exercises the
// invalid-operator → KindInvalid → 422 path.
func TestConverter_InvalidConditionOperator_ViaAdapter(t *testing.T) {
	svc := newConverterTestService(t)
	ctx := testConvAdminCtx()
	ad := CreateAdapter{s: svc}

	resp, err := ad.Create(ctx, &policyCreate.Request{
		Name: "BadOperator",
		Rules: []*policyCreate.RequestRulesItem{
			{
				ID:     "r1",
				Name:   "Bad",
				Effect: "allow",
				Conditions: []*policyCreate.RequestRulesItemConditionsItem{
					{
						Source:   "subject",
						Key:      "dept",
						Operator: "not-a-real-op",
						Values:   []string{"eng"},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	_, ok := resp.(policyCreate.Create422ErrorResponse)
	assert.True(t, ok, "invalid condition operator must yield 422, got %T", resp)
}

// TestConverter_InvalidRowScope_ViaAdapter exercises the invalid rowScope →
// KindInvalid → 422 path via the create adapter.
func TestConverter_InvalidRowScope_ViaAdapter(t *testing.T) {
	svc := newConverterTestService(t)
	ctx := testConvAdminCtx()
	ad := CreateAdapter{s: svc}

	resp, err := ad.Create(ctx, &policyCreate.Request{
		Name: "BadRowScope",
		Rules: []*policyCreate.RequestRulesItem{
			{
				ID:     "r1",
				Name:   "N",
				Effect: "allow",
				Obligations: &policyCreate.RequestRulesItemObligations{
					RowScope: "not-valid",
				},
			},
		},
	})
	require.NoError(t, err)
	_, ok := resp.(policyCreate.Create422ErrorResponse)
	assert.True(t, ok, "invalid rowScope must yield 422, got %T", resp)
}

// TestConverter_NullConditionItem exercises the conditions nil guard path.
func TestConverter_NullConditionItem_ViaAdapter(t *testing.T) {
	svc := newConverterTestService(t)
	ctx := testConvAdminCtx()
	ad := CreateAdapter{s: svc}

	resp, err := ad.Create(ctx, &policyCreate.Request{
		Name: "NullCond",
		Rules: []*policyCreate.RequestRulesItem{
			{
				ID:         "r1",
				Name:       "N",
				Effect:     "allow",
				Conditions: []*policyCreate.RequestRulesItemConditionsItem{nil},
			},
		},
	})
	require.NoError(t, err)
	_, ok := resp.(policyCreate.Create422ErrorResponse)
	assert.True(t, ok, "null condition item must yield 422, got %T", resp)
}

// TestConverter_UpdateInvalidConditionSource exercises the update converter's
// invalid-source → KindInvalid → 422 path.
func TestConverter_UpdateInvalidConditionSource_ViaAdapter(t *testing.T) {
	svc := newConverterTestService(t)
	ctx := testConvAdminCtx()

	p, err := svc.Create(ctx, CreateInput{Name: "P", Rules: minimalRules()})
	require.NoError(t, err)

	ad := UpdateAdapter{s: svc}
	resp, err := ad.Update(ctx, &policyUpdate.Request{
		ID:   p.ID,
		Name: "X",
		Rules: []*policyUpdate.RequestRulesItem{
			{
				ID:     "r1",
				Name:   "N",
				Effect: "allow",
				Conditions: []*policyUpdate.RequestRulesItemConditionsItem{
					{Source: "bad-source", Key: "k", Operator: "equals", Values: []string{"v"}},
				},
			},
		},
		ExpectedVersion: int64(p.Version),
	})
	require.NoError(t, err)
	_, ok := resp.(policyUpdate.Update422ErrorResponse)
	assert.True(t, ok, "update invalid source must yield 422, got %T", resp)
}

// TestConverter_UpdateInvalidRowScope exercises the update converter's invalid
// rowScope → KindInvalid → 422 path.
func TestConverter_UpdateInvalidRowScope_ViaAdapter(t *testing.T) {
	svc := newConverterTestService(t)
	ctx := testConvAdminCtx()

	p, err := svc.Create(ctx, CreateInput{Name: "P", Rules: minimalRules()})
	require.NoError(t, err)

	ad := UpdateAdapter{s: svc}
	resp, err := ad.Update(ctx, &policyUpdate.Request{
		ID:   p.ID,
		Name: "X",
		Rules: []*policyUpdate.RequestRulesItem{
			{
				ID:     "r1",
				Name:   "N",
				Effect: "allow",
				Obligations: &policyUpdate.RequestRulesItemObligations{
					RowScope: "invalid-scope",
				},
			},
		},
		ExpectedVersion: int64(p.Version),
	})
	require.NoError(t, err)
	_, ok := resp.(policyUpdate.Update422ErrorResponse)
	assert.True(t, ok, "update invalid rowScope must yield 422, got %T", resp)
}

// TestConverter_ListWire_ZeroConditionsNil verifies that a rule with no conditions
// yields nil in all list-wire converters (branch: len(conds)==0 → return nil).
func TestConverter_ListWire_ZeroConditionsNil(t *testing.T) {
	result := polCondsToListWire(nil)
	assert.Nil(t, result, "nil conditions must produce nil list wire")

	result2 := polCondsToListWire([]abac.Condition{})
	assert.Nil(t, result2, "empty conditions must produce nil list wire")
}

// ─── cross-attribute eq_attr converter tests (Batch C, #1977) ────────────────

// TestConverter_CrossAttr_Create_RoundTrip asserts that a create request
// carrying operator "eq_attr" + rhsSource "resource" + rhsKey "id" (and no
// values) parses to the correct domain abac.Condition and the response
// re-emits rhsSource/rhsKey.
func TestConverter_CrossAttr_Create_RoundTrip(t *testing.T) {
	svc := newConverterTestService(t)
	ctx := testConvAdminCtx()
	ad := CreateAdapter{s: svc}

	resp, err := ad.Create(ctx, &policyCreate.Request{
		Name: "CrossAttrPolicy",
		Rules: []*policyCreate.RequestRulesItem{
			{
				ID:     "r1",
				Name:   "Self ownership",
				Effect: "allow",
				Conditions: []*policyCreate.RequestRulesItemConditionsItem{
					{
						Source:    "subject",
						Key:       "sub",
						Operator:  "eq_attr",
						RHSSource: "resource",
						RHSKey:    "id",
						// Values intentionally absent/nil for cross-attr
					},
				},
			},
		},
	})
	require.NoError(t, err)
	created, ok := resp.(policyCreate.Create201JSONResponse)
	require.True(t, ok, "expected Create201JSONResponse, got %T", resp)

	data := created.Data
	require.Len(t, data.Rules, 1)
	require.Len(t, data.Rules[0].Conditions, 1)
	cond := data.Rules[0].Conditions[0]

	assert.Equal(t, "subject", cond.Source)
	assert.Equal(t, "sub", cond.Key)
	assert.Equal(t, "eq_attr", cond.Operator)
	assert.Equal(t, "resource", cond.RHSSource, "response must re-emit rhsSource")
	assert.Equal(t, "id", cond.RHSKey, "response must re-emit rhsKey")
	assert.Empty(t, cond.Values, "cross-attr condition must have no values in response")
}

// TestConverter_CrossAttr_Update_RoundTrip verifies the update path with eq_attr.
func TestConverter_CrossAttr_Update_RoundTrip(t *testing.T) {
	svc := newConverterTestService(t)
	ctx := testConvAdminCtx()

	// Create a minimal policy first.
	p, err := svc.Create(ctx, CreateInput{Name: "ToUpdateCrossAttr", Rules: minimalRules()})
	require.NoError(t, err)

	ad := UpdateAdapter{s: svc}
	resp, err := ad.Update(ctx, &policyUpdate.Request{
		ID:   p.ID,
		Name: "Updated CrossAttr",
		Rules: []*policyUpdate.RequestRulesItem{
			{
				ID:     "r1",
				Name:   "Self ownership update",
				Effect: "allow",
				Conditions: []*policyUpdate.RequestRulesItemConditionsItem{
					{
						Source:    "subject",
						Key:       "sub",
						Operator:  "eq_attr",
						RHSSource: "resource",
						RHSKey:    "id",
					},
				},
			},
		},
		ExpectedVersion: int64(p.Version),
	})
	require.NoError(t, err)
	updated, ok := resp.(policyUpdate.Update200JSONResponse)
	require.True(t, ok, "expected Update200JSONResponse, got %T", resp)

	data := updated.Data
	require.Len(t, data.Rules, 1)
	require.Len(t, data.Rules[0].Conditions, 1)
	cond := data.Rules[0].Conditions[0]

	assert.Equal(t, "eq_attr", cond.Operator)
	assert.Equal(t, "resource", cond.RHSSource, "response must re-emit rhsSource")
	assert.Equal(t, "id", cond.RHSKey, "response must re-emit rhsKey")
	assert.Empty(t, cond.Values)
}

// TestConverter_CrossAttr_StrayValues_Rejected asserts that a request with
// operator "eq_attr" but non-empty Values is rejected with 422 (domain
// Condition.Validate fires — stray Values on a cross-attr condition).
func TestConverter_CrossAttr_StrayValues_Rejected(t *testing.T) {
	svc := newConverterTestService(t)
	ctx := testConvAdminCtx()
	ad := CreateAdapter{s: svc}

	resp, err := ad.Create(ctx, &policyCreate.Request{
		Name: "CrossAttrStrayValues",
		Rules: []*policyCreate.RequestRulesItem{
			{
				ID:     "r1",
				Name:   "Bad cross-attr",
				Effect: "allow",
				Conditions: []*policyCreate.RequestRulesItemConditionsItem{
					{
						Source:    "subject",
						Key:       "sub",
						Operator:  "eq_attr",
						RHSSource: "resource",
						RHSKey:    "id",
						Values:    []string{"stray-value"},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	_, ok := resp.(policyCreate.Create422ErrorResponse)
	assert.True(t, ok, "eq_attr with stray values must yield 422, got %T", resp)
}

// TestConverter_CrossAttr_InvalidRHSSource_Rejected asserts that an unrecognized
// rhsSource string is rejected with 422 (KindInvalid).
func TestConverter_CrossAttr_InvalidRHSSource_Rejected(t *testing.T) {
	svc := newConverterTestService(t)
	ctx := testConvAdminCtx()
	ad := CreateAdapter{s: svc}

	resp, err := ad.Create(ctx, &policyCreate.Request{
		Name: "BadRHSSource",
		Rules: []*policyCreate.RequestRulesItem{
			{
				ID:     "r1",
				Name:   "Bad rhs source",
				Effect: "allow",
				Conditions: []*policyCreate.RequestRulesItemConditionsItem{
					{
						Source:    "subject",
						Key:       "sub",
						Operator:  "eq_attr",
						RHSSource: "not-a-source",
						RHSKey:    "id",
					},
				},
			},
		},
	})
	require.NoError(t, err)
	_, ok := resp.(policyCreate.Create422ErrorResponse)
	assert.True(t, ok, "invalid rhsSource must yield 422, got %T", resp)
}

// TestConverter_Update_CrossAttr_StrayValues_Rejected mirrors the create-path
// TestConverter_CrossAttr_StrayValues_Rejected for the UPDATE path: an eq_attr
// condition with non-empty Values must be rejected with 422 (KindInvalid).
func TestConverter_Update_CrossAttr_StrayValues_Rejected(t *testing.T) {
	svc := newConverterTestService(t)
	ctx := testConvAdminCtx()

	p, err := svc.Create(ctx, CreateInput{Name: "P", Rules: minimalRules()})
	require.NoError(t, err)

	ad := UpdateAdapter{s: svc}
	resp, err := ad.Update(ctx, &policyUpdate.Request{
		ID:   p.ID,
		Name: "X",
		Rules: []*policyUpdate.RequestRulesItem{
			{
				ID:     "r1",
				Name:   "Bad cross-attr update",
				Effect: "allow",
				Conditions: []*policyUpdate.RequestRulesItemConditionsItem{
					{
						Source:    "subject",
						Key:       "sub",
						Operator:  "eq_attr",
						RHSSource: "resource",
						RHSKey:    "id",
						Values:    []string{"stray-value"}, // stray Values on eq_attr: invalid
					},
				},
			},
		},
		ExpectedVersion: int64(p.Version),
	})
	require.NoError(t, err)
	_, ok := resp.(policyUpdate.Update422ErrorResponse)
	assert.True(t, ok, "update eq_attr with stray values must yield 422, got %T", resp)
}

// TestConverter_Update_CrossAttr_InvalidRHSSource_Rejected mirrors the create-path
// TestConverter_CrossAttr_InvalidRHSSource_Rejected for the UPDATE path: an
// unrecognized rhsSource string must be rejected with 422 (KindInvalid).
func TestConverter_Update_CrossAttr_InvalidRHSSource_Rejected(t *testing.T) {
	svc := newConverterTestService(t)
	ctx := testConvAdminCtx()

	p, err := svc.Create(ctx, CreateInput{Name: "P", Rules: minimalRules()})
	require.NoError(t, err)

	ad := UpdateAdapter{s: svc}
	resp, err := ad.Update(ctx, &policyUpdate.Request{
		ID:   p.ID,
		Name: "X",
		Rules: []*policyUpdate.RequestRulesItem{
			{
				ID:     "r1",
				Name:   "Bad rhs source update",
				Effect: "allow",
				Conditions: []*policyUpdate.RequestRulesItemConditionsItem{
					{
						Source:    "subject",
						Key:       "sub",
						Operator:  "eq_attr",
						RHSSource: "not-a-source", // unrecognized rhsSource
						RHSKey:    "id",
					},
				},
			},
		},
		ExpectedVersion: int64(p.Version),
	})
	require.NoError(t, err)
	_, ok := resp.(policyUpdate.Update422ErrorResponse)
	assert.True(t, ok, "update with invalid rhsSource must yield 422, got %T", resp)
}

// TestConverter_StaticCondition_NoRHSInResponse asserts that a static "eq"
// condition still round-trips with values and emits no rhsSource/rhsKey in the
// response (zero values → omitempty).
func TestConverter_StaticCondition_NoRHSInResponse(t *testing.T) {
	svc := newConverterTestService(t)
	ctx := testConvAdminCtx()
	ad := CreateAdapter{s: svc}

	resp, err := ad.Create(ctx, &policyCreate.Request{
		Name: "StaticOnly",
		Rules: []*policyCreate.RequestRulesItem{
			{
				ID:     "r1",
				Name:   "Static eq",
				Effect: "allow",
				Conditions: []*policyCreate.RequestRulesItemConditionsItem{
					{
						Source:   "subject",
						Key:      "department",
						Operator: "eq",
						Values:   []string{"eng"},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	created, ok := resp.(policyCreate.Create201JSONResponse)
	require.True(t, ok, "expected Create201JSONResponse, got %T", resp)

	data := created.Data
	require.Len(t, data.Rules[0].Conditions, 1)
	cond := data.Rules[0].Conditions[0]
	assert.Equal(t, "eq", cond.Operator)
	assert.Equal(t, []string{"eng"}, cond.Values)
	assert.Empty(t, cond.RHSSource, "static condition must emit no rhsSource")
	assert.Empty(t, cond.RHSKey, "static condition must emit no rhsKey")
}
