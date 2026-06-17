package governance

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// gateTestEpoch is the fixed clock instant for gate tests (deterministic
// registrar timestamps). TEST-TIME-LITERAL-01: a package-level var (time.Time
// cannot be a const; injected via clockmock.New for determinism).
var gateTestEpoch = time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)

// validTenant is a canonical lowercase dashed UUID accepted by tenant.Validate.
const validTenant = tenant.TenantID("11111111-1111-1111-1111-111111111111")

func newGate(t *testing.T) (*RegistrationGate, *registry.ContractRegistrar) {
	t.Helper()
	clk := clockmock.New(gateTestEpoch)
	reg := registry.NewContractRegistrar(clk)
	return NewRegistrationGate(reg, clk), reg
}

// validEventContract is a fully-formed event contract: owner + lifecycle +
// provider (publisher) + a consumer (subscriber). It passes every gate rule.
func validEventContract() *metadata.ContractMeta {
	return &metadata.ContractMeta{
		ID:        "event.registry.thing-happened.v1",
		Kind:      "event",
		OwnerCell: "registrycore",
		Lifecycle: "active",
		Endpoints: metadata.EndpointsMeta{
			Publisher:   "registrycore",
			Subscribers: []string{"othercell"},
		},
	}
}

func hasCode(results []ValidationResult, code RuleCode) bool {
	for i := range results {
		if results[i].Code == code {
			return true
		}
	}
	return false
}

// TestGate_NewRegistrationGate_NilStoreFailFast asserts the strong-dep guard:
// a nil store panics at construction (no degraded/noop mode).
func TestGate_NewRegistrationGate_NilStoreFailFast(t *testing.T) {
	t.Parallel()
	clk := clockmock.New(gateTestEpoch)
	assert.Panics(t, func() { NewRegistrationGate(nil, clk) },
		"nil store must fail-fast at construction")
}

// TestGate_NewRegistrationGate_NilClockFailFast asserts the clock strong-dep
// guard (clock.MustHaveClock) panics at construction.
func TestGate_NewRegistrationGate_NilClockFailFast(t *testing.T) {
	t.Parallel()
	reg := registry.NewContractRegistrar(clockmock.New(gateTestEpoch))
	assert.Panics(t, func() { NewRegistrationGate(reg, nil) },
		"nil clock must fail-fast at construction")
}

// TestGate_NilCandidate_FailClosed: a nil candidate is a caller precondition
// violation → fail-closed with ReasonInvalidInput (distinct from a rule failure),
// never a panic or fail-open, for both Check and Submit.
func TestGate_NilCandidate_FailClosed(t *testing.T) {
	t.Parallel()
	gate, reg := newGate(t)

	checkRes := gate.Check(context.Background(), validTenant, nil)
	assert.False(t, checkRes.Allowed)
	assert.Equal(t, ReasonInvalidInput(), checkRes.Reason)
	assert.Empty(t, checkRes.Result)

	got, submitRes, err := gate.Submit(context.Background(), validTenant, nil, "submitter-cell")
	require.NoError(t, err)
	assert.False(t, submitRes.Allowed)
	assert.Equal(t, ReasonInvalidInput(), submitRes.Reason)
	assert.Equal(t, registry.RegistrationState{}, got.State)
	assert.Equal(t, 0, reg.Count(), "nil candidate must not persist")
}

// TestGate_Submit_EmptySubmitter_FailClosed: a valid contract with an empty
// submitter is rejected at the gate (ReasonInvalidInput), not via the store's
// validate() side-effect, and is not persisted.
func TestGate_Submit_EmptySubmitter_FailClosed(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, submitter string }{
		{"empty", ""},
		{"whitespace", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gate, reg := newGate(t)
			got, res, err := gate.Submit(context.Background(), validTenant, validEventContract(), tc.submitter)
			require.NoError(t, err)
			assert.False(t, res.Allowed)
			assert.Equal(t, ReasonInvalidInput(), res.Reason)
			assert.Equal(t, registry.RegistrationState{}, got.State)
			assert.Equal(t, 0, reg.Count(), "missing submitter must not persist")
		})
	}
}

// TestGate_CheckThenSubmit_DryRunHasNoSideEffect closes the Check side-effect-free
// claim end-to-end: a Check followed by a Submit of the same contract persists
// exactly once (the Check did not pre-persist, nor block the later Submit).
func TestGate_CheckThenSubmit_DryRunHasNoSideEffect(t *testing.T) {
	t.Parallel()
	gate, reg := newGate(t)
	c := validEventContract()

	checkRes := gate.Check(context.Background(), validTenant, c)
	require.True(t, checkRes.Allowed)
	require.Equal(t, 0, reg.Count(), "Check must not persist")

	_, submitRes, err := gate.Submit(context.Background(), validTenant, c, "submitter-cell")
	require.NoError(t, err)
	assert.True(t, submitRes.Allowed)
	assert.Equal(t, 1, reg.Count(), "Submit after Check persists exactly once")
}

// TestGate_Check_DeprecatedContract_AllowedWithWarning covers AC1: a wire-compliant
// contract carrying a non-blocking advisory (deprecated lifecycle) returns
// {Allowed:true, Warnings:[…]} and is NOT persisted (dry-run).
func TestGate_Check_DeprecatedContract_AllowedWithWarning(t *testing.T) {
	t.Parallel()
	gate, reg := newGate(t)
	c := validEventContract()
	c.Lifecycle = "deprecated"

	res := gate.Check(context.Background(), validTenant, c)

	assert.True(t, res.Allowed, "deprecated lifecycle is allowed (advisory, not blocking)")
	assert.Equal(t, ReasonAllowed(), res.Reason)
	assert.Empty(t, res.Result, "no blocking errors")
	require.NotEmpty(t, res.Warnings, "deprecated registration must surface a warning")
	assert.True(t, hasCode(res.Warnings, codeREG02), "warning must be REG-02")
	assert.Equal(t, 0, reg.Count(), "Check is dry-run: nothing persisted")
}

// TestGate_Check_ValidContract_AllowedNoFindings: a fully valid active contract
// passes with no findings at all.
func TestGate_Check_ValidContract_AllowedNoFindings(t *testing.T) {
	t.Parallel()
	gate, reg := newGate(t)

	res := gate.Check(context.Background(), validTenant, validEventContract())

	assert.True(t, res.Allowed)
	assert.Equal(t, ReasonAllowed(), res.Reason)
	assert.Empty(t, res.Result)
	assert.Empty(t, res.Warnings)
	assert.Equal(t, 0, reg.Count())
}

// TestGate_Submit_InvalidContract_DeniedNotPersisted covers AC2: a contract that
// violates a governance rule is rejected before persistence — there is no
// "submitted but invalid" intermediate state — and the denial carries a
// machine-readable reason + finding code.
func TestGate_Submit_InvalidContract_DeniedNotPersisted(t *testing.T) {
	t.Parallel()
	gate, reg := newGate(t)
	c := validEventContract()
	c.OwnerCell = "" // CH-01 violation

	got, res, err := gate.Submit(context.Background(), validTenant, c, "submitter-cell")

	require.NoError(t, err, "a governance denial is a verdict, not a Go error")
	assert.False(t, res.Allowed)
	assert.Equal(t, ReasonValidationFailed(), res.Reason)
	assert.True(t, hasCode(res.Result, codeCH01), "machine-readable reason: CH-01 in Result")
	assert.Equal(t, registry.RegistrationState{}, got.State, "denied: no registration returned")
	assert.Equal(t, 0, reg.Count(), "denied contract MUST NOT enter submitted")
}

// TestGate_ValidatorUnavailable_FailClosed covers AC3: when the validation run
// cannot complete (ctx canceled), the gate denies (fail-closed), never fail-open.
func TestGate_ValidatorUnavailable_FailClosed(t *testing.T) {
	t.Parallel()
	gate, reg := newGate(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-canceled

	res := gate.Check(ctx, validTenant, validEventContract())

	assert.False(t, res.Allowed, "interrupted validation must NEVER fail open")
	assert.Equal(t, ReasonValidatorUnavailable(), res.Reason)
	assert.Equal(t, 0, reg.Count())
}

// TestGate_FanoutIncomplete_FailClosed covers AC4: a runtime contract missing its
// publisher, subscriber, or owner is rejected fail-closed (owner→CH-01;
// publisher/subscriber→REG-01) — the runtime compensation for the in-tree
// reverse-coverage archtests being vacuous over runtime contracts.
func TestGate_FanoutIncomplete_FailClosed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		mutate   func(*metadata.ContractMeta)
		wantCode RuleCode
	}{
		{"missing owner", func(c *metadata.ContractMeta) { c.OwnerCell = "" }, codeCH01},
		{"missing publisher", func(c *metadata.ContractMeta) { c.Endpoints.Publisher = "" }, codeREG01},
		{"missing subscriber", func(c *metadata.ContractMeta) { c.Endpoints.Subscribers = nil }, codeREG01},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gate, reg := newGate(t)
			c := validEventContract()
			tc.mutate(c)

			res := gate.Check(context.Background(), validTenant, c)

			assert.False(t, res.Allowed, "incomplete fanout must fail-closed")
			assert.Equal(t, ReasonValidationFailed(), res.Reason)
			assert.True(t, hasCode(res.Result, tc.wantCode), "expected finding %s", tc.wantCode)
			assert.Equal(t, 0, reg.Count())
		})
	}
}

// TestGate_TenantInvalid_FailClosed asserts the FR-002 "缺租户 → deny": an empty,
// nil-UUID, or non-canonical tenant is denied without running validation.
func TestGate_TenantInvalid_FailClosed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		tnt  tenant.TenantID
	}{
		{"empty", ""},
		{"nil uuid", "00000000-0000-0000-0000-000000000000"},
		{"non-canonical", "not-a-uuid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gate, reg := newGate(t)

			res := gate.Check(context.Background(), tc.tnt, validEventContract())

			assert.False(t, res.Allowed)
			assert.Equal(t, ReasonTenantInvalid(), res.Reason)
			assert.Empty(t, res.Result, "tenant denial short-circuits before validation")
			assert.Equal(t, 0, reg.Count())
		})
	}
}

// TestGate_Submit_HappyPath: a valid contract under a valid tenant is persisted
// at the submitted state and the gate returns Allowed.
func TestGate_Submit_HappyPath(t *testing.T) {
	t.Parallel()
	gate, reg := newGate(t)
	c := validEventContract()

	got, res, err := gate.Submit(context.Background(), validTenant, c, "submitter-cell")

	require.NoError(t, err)
	assert.True(t, res.Allowed)
	assert.Equal(t, ReasonAllowed(), res.Reason)
	assert.Equal(t, registry.StateSubmitted(), got.State)
	assert.Equal(t, c.ID, got.ID)
	assert.Equal(t, "submitter-cell", got.Submitter)
	assert.Equal(t, 1, reg.Count(), "valid contract enters submitted exactly once")
}

// TestGate_Submit_Duplicate_FailClosed: re-submitting an id already registered is
// a fail-closed conflict (ReasonDuplicate) and does not double-persist.
func TestGate_Submit_Duplicate_FailClosed(t *testing.T) {
	t.Parallel()
	gate, reg := newGate(t)
	c := validEventContract()

	_, first, err := gate.Submit(context.Background(), validTenant, c, "submitter-cell")
	require.NoError(t, err)
	require.True(t, first.Allowed)

	_, second, err := gate.Submit(context.Background(), validTenant, c, "submitter-cell")

	require.Error(t, err, "store surfaces the duplicate conflict")
	assert.False(t, second.Allowed)
	assert.Equal(t, ReasonDuplicate(), second.Reason)
	assert.Equal(t, 1, reg.Count(), "duplicate must not double-persist")
}

// TestGateReason_FrozenRegistry pins the closed GateReason value set and proves it
// is non-vacuous (anti-vacuity for the sealed-type closure). Mirrors
// registry.TestRegistrationState_FrozenRegistry / transport TransportOutcome.
func TestGateReason_FrozenRegistry(t *testing.T) {
	t.Parallel()
	got := make(map[string]int, len(allGateReasons))
	for _, r := range allGateReasons {
		got[r.String()]++
	}
	want := map[string]int{
		"allowed":               1,
		"validation-failed":     1,
		"validator-unavailable": 1,
		"tenant-invalid":        1,
		"duplicate":             1,
		"invalid-input":         1,
	}
	assert.Equal(t, want, got, "GateReason value set drifted")
	for _, r := range allGateReasons {
		assert.False(t, r.IsZero(), "%q reported IsZero", r)
		assert.True(t, r.isRegistered(), "%q not registered", r)
	}
}

// TestGateReason_ZeroValueFailClosed: a forged zero reason renders fail-closed.
func TestGateReason_ZeroValueFailClosed(t *testing.T) {
	t.Parallel()
	var zero GateReason
	assert.Equal(t, GateReasonUnknown, zero.String())
	assert.True(t, zero.IsZero())
	assert.False(t, zero.isRegistered())
}

// TestRuntimeFanoutCompleteness_PerKind directly exercises the REG-01 rule across
// kinds: provider completeness applies to all; consumer completeness applies only
// to event/command/projection/webhook (http/grpc/saga exempt).
func TestRuntimeFanoutCompleteness_PerKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		contract  *metadata.ContractMeta
		wantFlags bool
	}{
		{
			name: "event complete",
			contract: &metadata.ContractMeta{
				ID: "event.a.v1", Kind: "event",
				Endpoints: metadata.EndpointsMeta{Publisher: "c", Subscribers: []string{"d"}},
			},
			wantFlags: false,
		},
		{
			name: "event missing consumer flags",
			contract: &metadata.ContractMeta{
				ID: "event.b.v1", Kind: "event",
				Endpoints: metadata.EndpointsMeta{Publisher: "c"},
			},
			wantFlags: true,
		},
		{
			name: "http no clients is fine (external consumers)",
			contract: &metadata.ContractMeta{
				ID: "http.c.v1", Kind: "http",
				Endpoints: metadata.EndpointsMeta{Server: "c"},
			},
			wantFlags: false,
		},
		{
			name: "http missing server flags",
			contract: &metadata.ContractMeta{
				ID: "http.d.v1", Kind: "http",
			},
			wantFlags: true,
		},
		{
			name: "saga no consumers exempt",
			contract: &metadata.ContractMeta{
				ID: "saga.e.v1", Kind: "saga",
				Endpoints: metadata.EndpointsMeta{Server: "c"},
			},
			wantFlags: false,
		},
		{
			name: "command complete",
			contract: &metadata.ContractMeta{
				ID: "command.f.v1", Kind: "command",
				Endpoints: metadata.EndpointsMeta{Handler: "c", Invokers: []string{"d"}},
			},
			wantFlags: false,
		},
		{
			name: "command missing invoker flags",
			contract: &metadata.ContractMeta{
				ID: "command.g.v1", Kind: "command",
				Endpoints: metadata.EndpointsMeta{Handler: "c"},
			},
			wantFlags: true,
		},
		{
			name: "projection complete",
			contract: &metadata.ContractMeta{
				ID: "projection.h.v1", Kind: "projection",
				Endpoints: metadata.EndpointsMeta{Provider: "c", Readers: []string{"d"}},
			},
			wantFlags: false,
		},
		{
			name: "projection missing reader flags",
			contract: &metadata.ContractMeta{
				ID: "projection.i.v1", Kind: "projection",
				Endpoints: metadata.EndpointsMeta{Provider: "c"},
			},
			wantFlags: true,
		},
		{
			name: "webhook complete (provider=ownerCell via CH-01, consumer=receivers)",
			contract: &metadata.ContractMeta{
				ID: "webhook.j.v1", Kind: "webhook", OwnerCell: "c",
				Endpoints: metadata.EndpointsMeta{Receivers: []string{"d"}},
			},
			wantFlags: false,
		},
		{
			name: "webhook missing receiver flags",
			contract: &metadata.ContractMeta{
				ID: "webhook.k.v1", Kind: "webhook", OwnerCell: "c",
			},
			wantFlags: true,
		},
		{
			name: "grpc complete (consumer exempt, server present)",
			contract: &metadata.ContractMeta{
				ID: "grpc.l.v1", Kind: "grpc",
				Endpoints: metadata.EndpointsMeta{Server: "c"},
			},
			wantFlags: false,
		},
		{
			name: "grpc missing server flags",
			contract: &metadata.ContractMeta{
				ID: "grpc.m.v1", Kind: "grpc",
			},
			wantFlags: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := NewValidator(singleContractProject(tc.contract), "", clockmock.New(gateTestEpoch))
			findings := v.runtimeFanoutCompleteness()
			if tc.wantFlags {
				assert.True(t, hasCode(findings, codeREG01), "expected REG-01 finding")
			} else {
				assert.Empty(t, findings, "complete fanout should produce no REG-01 findings")
			}
		})
	}
}
