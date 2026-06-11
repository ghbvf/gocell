package policymanage

import (
	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"

	policyCreate "github.com/ghbvf/gocell/generated/contracts/http/policy/create/v1"
	policyGet "github.com/ghbvf/gocell/generated/contracts/http/policy/get/v1"
	policyList "github.com/ghbvf/gocell/generated/contracts/http/policy/list/v1"
	policyUpdate "github.com/ghbvf/gocell/generated/contracts/http/policy/update/v1"
)

// --- Request → domain converters ---

// createRulesFromRequest converts a create request's rules to domain []abac.Rule.
// Returns KindInvalid/ErrValidationFailed on any unrecognized enum code.
func createRulesFromRequest(req *policyCreate.Request) ([]abac.Rule, error) {
	rules := make([]abac.Rule, 0, len(req.Rules))
	for _, item := range req.Rules {
		r, err := createRuleItemToDomain(item)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, nil
}

func createRuleItemToDomain(item *policyCreate.RequestRulesItem) (abac.Rule, error) {
	effect, err := authz.ParseEffect(item.Effect)
	if err != nil {
		return abac.Rule{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"policymanage: invalid rule effect code",
			errcode.WithDetails(errcode.PublicString("ruleId", item.ID), errcode.PublicString("effect", item.Effect)))
	}
	conds, err := createConditionsToDomain(item.Conditions)
	if err != nil {
		return abac.Rule{}, err
	}
	obs, err := createObligationsToDomain(item.Obligations)
	if err != nil {
		return abac.Rule{}, err
	}
	return abac.Rule{
		ID:          item.ID,
		Name:        item.Name,
		Effect:      effect,
		Conditions:  conds,
		Obligations: obs,
	}, nil
}

// conditionWire is the minimal condition wire shape shared by create and update
// request schemas. Both generated types carry Source/Key/Operator/Values fields
// with identical semantics; this adapter allows parseConditionWire to operate on
// both without duplicating the parsing logic (avoids dupl lint).
type conditionWire interface {
	getSource() string
	getKey() string
	getOperator() string
	getValues() []string
}

func createConditionsToDomain(items []*policyCreate.RequestRulesItemConditionsItem) ([]abac.Condition, error) {
	wires := make([]conditionWire, len(items))
	for i, c := range items {
		wires[i] = (*createCondWire)(c)
	}
	return parseConditionWires(wires)
}

// createCondWire adapts the create-contract condition type to conditionWire.
type createCondWire policyCreate.RequestRulesItemConditionsItem

func (w *createCondWire) getSource() string   { return w.Source }
func (w *createCondWire) getKey() string      { return w.Key }
func (w *createCondWire) getOperator() string { return w.Operator }
func (w *createCondWire) getValues() []string { return w.Values }

// parseConditionWires converts a slice of conditionWire to domain Conditions.
// Factored out to avoid code duplication between create and update converters.
func parseConditionWires(items []conditionWire) ([]abac.Condition, error) {
	if len(items) == 0 {
		return nil, nil
	}
	conds := make([]abac.Condition, 0, len(items))
	for _, c := range items {
		src, err := abac.ParseAttributeSource(c.getSource())
		if err != nil {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"policymanage: invalid condition source",
				errcode.WithDetails(errcode.PublicString("source", c.getSource())))
		}
		op, err := abac.ParseOperator(c.getOperator())
		if err != nil {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"policymanage: invalid condition operator",
				errcode.WithDetails(errcode.PublicString("operator", c.getOperator())))
		}
		conds = append(conds, abac.Condition{Source: src, Key: c.getKey(), Operator: op, Values: c.getValues()})
	}
	return conds, nil
}

func createObligationsToDomain(obs *policyCreate.RequestRulesItemObligations) (authz.Obligations, error) {
	if obs == nil {
		return authz.Obligations{}, nil
	}
	return parseObligationsFromWire(obs.RowScope, obs.FieldMask)
}

// updateRulesFromRequest converts an update request's rules to domain []abac.Rule.
func updateRulesFromRequest(req *policyUpdate.Request) ([]abac.Rule, error) {
	rules := make([]abac.Rule, 0, len(req.Rules))
	for _, item := range req.Rules {
		r, err := updateRuleItemToDomain(item)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, nil
}

func updateRuleItemToDomain(item *policyUpdate.RequestRulesItem) (abac.Rule, error) {
	effect, err := authz.ParseEffect(item.Effect)
	if err != nil {
		return abac.Rule{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"policymanage: invalid rule effect code",
			errcode.WithDetails(errcode.PublicString("ruleId", item.ID), errcode.PublicString("effect", item.Effect)))
	}
	conds, err := updateConditionsToDomain(item.Conditions)
	if err != nil {
		return abac.Rule{}, err
	}
	obs, err := updateObligationsToDomain(item.Obligations)
	if err != nil {
		return abac.Rule{}, err
	}
	return abac.Rule{
		ID:          item.ID,
		Name:        item.Name,
		Effect:      effect,
		Conditions:  conds,
		Obligations: obs,
	}, nil
}

func updateConditionsToDomain(items []*policyUpdate.RequestRulesItemConditionsItem) ([]abac.Condition, error) {
	wires := make([]conditionWire, len(items))
	for i, c := range items {
		wires[i] = (*updateCondWire)(c)
	}
	return parseConditionWires(wires)
}

// updateCondWire adapts the update-contract condition type to conditionWire.
type updateCondWire policyUpdate.RequestRulesItemConditionsItem

func (w *updateCondWire) getSource() string   { return w.Source }
func (w *updateCondWire) getKey() string      { return w.Key }
func (w *updateCondWire) getOperator() string { return w.Operator }
func (w *updateCondWire) getValues() []string { return w.Values }

func updateObligationsToDomain(obs *policyUpdate.RequestRulesItemObligations) (authz.Obligations, error) {
	if obs == nil {
		return authz.Obligations{}, nil
	}
	return parseObligationsFromWire(obs.RowScope, obs.FieldMask)
}

// parseObligationsFromWire is the shared obligations wire→domain helper.
// Empty rowScopeStr is valid (zero RowScope = no constraint). KindInvalid on
// unrecognized rowScope code.
func parseObligationsFromWire(rowScopeStr string, fieldMaskFields []string) (authz.Obligations, error) {
	obs := authz.Obligations{
		FieldMask: authz.FieldMask{Fields: fieldMaskFields},
	}
	if rowScopeStr != "" {
		rs, err := tenant.ParseRowScope(rowScopeStr)
		if err != nil {
			return authz.Obligations{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"policymanage: invalid rowScope value",
				errcode.WithDetails(errcode.PublicString("rowScope", rowScopeStr)))
		}
		obs.RowScope = rs
	}
	return obs, nil
}

// --- domain → response converters ---

// policyToCreateResponseData converts *abac.Policy to create-contract ResponseData.
func policyToCreateResponseData(p *abac.Policy) *policyCreate.ResponseData {
	return &policyCreate.ResponseData{
		ID:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		Version:     int64(p.Version),
		Rules:       polRulesToCreateWire(p.Rules),
	}
}

func polRulesToCreateWire(rules []abac.Rule) []*policyCreate.ResponseDataRulesItem {
	items := make([]*policyCreate.ResponseDataRulesItem, 0, len(rules))
	for _, r := range rules {
		item := &policyCreate.ResponseDataRulesItem{
			ID:          r.ID,
			Name:        r.Name,
			Effect:      r.Effect.String(),
			Conditions:  polCondsToCreateWire(r.Conditions),
			Obligations: polObsToCreateWire(r.Obligations),
		}
		items = append(items, item)
	}
	return items
}

func polCondsToCreateWire(conds []abac.Condition) []*policyCreate.ResponseDataRulesItemConditionsItem {
	if len(conds) == 0 {
		return nil
	}
	out := make([]*policyCreate.ResponseDataRulesItemConditionsItem, 0, len(conds))
	for _, c := range conds {
		out = append(out, &policyCreate.ResponseDataRulesItemConditionsItem{
			Source: c.Source.String(), Key: c.Key, Operator: c.Operator.String(), Values: c.Values,
		})
	}
	return out
}

func polObsToCreateWire(obs authz.Obligations) *policyCreate.ResponseDataRulesItemObligations {
	if obs.RowScope == 0 && obs.FieldMask.IsZero() {
		return nil
	}
	w := &policyCreate.ResponseDataRulesItemObligations{FieldMask: obs.FieldMask.Fields}
	if obs.RowScope != 0 {
		w.RowScope = obs.RowScope.String()
	}
	return w
}

// policyToGetResponseData converts *abac.Policy to get-contract ResponseData.
func policyToGetResponseData(p *abac.Policy) *policyGet.ResponseData {
	return &policyGet.ResponseData{
		ID:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		Version:     int64(p.Version),
		Rules:       polRulesToGetWire(p.Rules),
	}
}

func polRulesToGetWire(rules []abac.Rule) []*policyGet.ResponseDataRulesItem {
	items := make([]*policyGet.ResponseDataRulesItem, 0, len(rules))
	for _, r := range rules {
		items = append(items, &policyGet.ResponseDataRulesItem{
			ID: r.ID, Name: r.Name, Effect: r.Effect.String(),
			Conditions:  polCondsToGetWire(r.Conditions),
			Obligations: polObsToGetWire(r.Obligations),
		})
	}
	return items
}

func polCondsToGetWire(conds []abac.Condition) []*policyGet.ResponseDataRulesItemConditionsItem {
	if len(conds) == 0 {
		return nil
	}
	out := make([]*policyGet.ResponseDataRulesItemConditionsItem, 0, len(conds))
	for _, c := range conds {
		out = append(out, &policyGet.ResponseDataRulesItemConditionsItem{
			Source: c.Source.String(), Key: c.Key, Operator: c.Operator.String(), Values: c.Values,
		})
	}
	return out
}

func polObsToGetWire(obs authz.Obligations) *policyGet.ResponseDataRulesItemObligations {
	if obs.RowScope == 0 && obs.FieldMask.IsZero() {
		return nil
	}
	w := &policyGet.ResponseDataRulesItemObligations{FieldMask: obs.FieldMask.Fields}
	if obs.RowScope != 0 {
		w.RowScope = obs.RowScope.String()
	}
	return w
}

// policyToUpdateResponseData converts *abac.Policy to update-contract ResponseData.
func policyToUpdateResponseData(p *abac.Policy) *policyUpdate.ResponseData {
	return &policyUpdate.ResponseData{
		ID:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		Version:     int64(p.Version),
		Rules:       polRulesToUpdateWire(p.Rules),
	}
}

func polRulesToUpdateWire(rules []abac.Rule) []*policyUpdate.ResponseDataRulesItem {
	items := make([]*policyUpdate.ResponseDataRulesItem, 0, len(rules))
	for _, r := range rules {
		items = append(items, &policyUpdate.ResponseDataRulesItem{
			ID: r.ID, Name: r.Name, Effect: r.Effect.String(),
			Conditions:  polCondsToUpdateWire(r.Conditions),
			Obligations: polObsToUpdateWire(r.Obligations),
		})
	}
	return items
}

func polCondsToUpdateWire(conds []abac.Condition) []*policyUpdate.ResponseDataRulesItemConditionsItem {
	if len(conds) == 0 {
		return nil
	}
	out := make([]*policyUpdate.ResponseDataRulesItemConditionsItem, 0, len(conds))
	for _, c := range conds {
		out = append(out, &policyUpdate.ResponseDataRulesItemConditionsItem{
			Source: c.Source.String(), Key: c.Key, Operator: c.Operator.String(), Values: c.Values,
		})
	}
	return out
}

func polObsToUpdateWire(obs authz.Obligations) *policyUpdate.ResponseDataRulesItemObligations {
	if obs.RowScope == 0 && obs.FieldMask.IsZero() {
		return nil
	}
	w := &policyUpdate.ResponseDataRulesItemObligations{FieldMask: obs.FieldMask.Fields}
	if obs.RowScope != 0 {
		w.RowScope = obs.RowScope.String()
	}
	return w
}

// policyToListResponseDataItem converts *abac.Policy to list-contract ResponseDataItem.
func policyToListResponseDataItem(p *abac.Policy) *policyList.ResponseDataItem {
	return &policyList.ResponseDataItem{
		ID:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		Version:     int64(p.Version),
		Rules:       polRulesToListWire(p.Rules),
	}
}

func polRulesToListWire(rules []abac.Rule) []*policyList.ResponseDataItemRulesItem {
	items := make([]*policyList.ResponseDataItemRulesItem, 0, len(rules))
	for _, r := range rules {
		items = append(items, &policyList.ResponseDataItemRulesItem{
			ID: r.ID, Name: r.Name, Effect: r.Effect.String(),
			Conditions:  polCondsToListWire(r.Conditions),
			Obligations: polObsToListWire(r.Obligations),
		})
	}
	return items
}

func polCondsToListWire(conds []abac.Condition) []*policyList.ResponseDataItemRulesItemConditionsItem {
	if len(conds) == 0 {
		return nil
	}
	out := make([]*policyList.ResponseDataItemRulesItemConditionsItem, 0, len(conds))
	for _, c := range conds {
		out = append(out, &policyList.ResponseDataItemRulesItemConditionsItem{
			Source: c.Source.String(), Key: c.Key, Operator: c.Operator.String(), Values: c.Values,
		})
	}
	return out
}

func polObsToListWire(obs authz.Obligations) *policyList.ResponseDataItemRulesItemObligations {
	if obs.RowScope == 0 && obs.FieldMask.IsZero() {
		return nil
	}
	w := &policyList.ResponseDataItemRulesItemObligations{FieldMask: obs.FieldMask.Fields}
	if obs.RowScope != 0 {
		w.RowScope = obs.RowScope.String()
	}
	return w
}
