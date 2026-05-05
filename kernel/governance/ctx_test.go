package governance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// projectWithEarlyAndLateError constructs a fixture that violates both REF-01
// (slice references a non-existent cell) and ADV-05 (active event contract
// without subscribers). REF-01 sits at the head of the rule pipeline; ADV-05
// sits much later (see Validator.rules() in validate.go: REF-01 is index 0,
// ADV-05 is among the trailing ADV/OUTGUARD/SliceConsistency block). Validate
// finds both; ValidateFailFast must surface REF-01 and skip ADV-05 entirely.
//
// If a future change to rules() reorders ADV-05 before REF-01, the
// ShortCircuit test below would still typecheck but stop covering the
// short-circuit contract — TestValidate_RespectsCtxCancel and
// TestValidateFailFast_RespectsCtxCancel act as the structural backstop.
func projectWithEarlyAndLateError(t *testing.T) *metadata.ProjectMeta {
	t.Helper()
	pm := validProject()
	// REF-01: BelongsToCell points at a cell id that does not exist.
	pm.Slices["accesscore/session-login"].BelongsToCell = "ghost-cell"
	// ADV-05: active event contract with zero subscribers.
	replayable := true
	pm.Contracts["event.dead.v1"] = &metadata.ContractMeta{
		ID:                "event.dead.v1",
		Kind:              "event",
		OwnerCell:         "accesscore",
		ConsistencyLevel:  "L2",
		Lifecycle:         "active",
		Replayable:        &replayable,
		IdempotencyKey:    "id",
		DeliverySemantics: "at-least-once",
		Endpoints: metadata.EndpointsMeta{
			Publisher:   "accesscore",
			Subscribers: nil,
		},
		Dir:  "contracts/event/dead/v1",
		File: "contracts/event/dead/v1/contract.yaml",
	}
	return pm
}

// TestValidateFailFast_ShortCircuitsOnFirstError closes the "ValidateFailFast
// has zero coverage" gap (030 G-03 finding c). It pins the contract that once
// any rule emits a SeverityError, no later rule runs — so a CI fail-fast pass
// cannot accidentally collect findings from rules executed after the bailout.
func TestValidateFailFast_ShortCircuitsOnFirstError(t *testing.T) {
	pm := projectWithEarlyAndLateError(t)
	val := NewValidator(pm, "", clock.Real())

	// Sanity: the full Validate pass surfaces both findings.
	full := val.Validate(t.Context())
	require.NotEmpty(t, findByCode(full, "REF-01"), "REF-01 must fire under full Validate")
	require.NotEmpty(t, findByCode(full, "ADV-05"), "ADV-05 must fire under full Validate")

	// Fail-fast bails after the first SeverityError-producing rule.
	failFast := val.ValidateFailFast(t.Context())
	require.NotEmpty(t, findByCode(failFast, "REF-01"), "REF-01 must fire under fail-fast")
	assert.Empty(t, findByCode(failFast, "ADV-05"),
		"ADV-05 must NOT fire under fail-fast: short-circuit broke")
	// Rule-code-agnostic short-circuit signature: fail-fast strictly produces
	// fewer findings than the full pass. Holds regardless of rules() ordering
	// because the only reason fail-fast can match the full count is if no
	// rule ever errored, which the require.NotEmpty above already rules out.
	assert.Less(t, len(failFast), len(full),
		"fail-fast must produce strictly fewer findings than full Validate when an error fires")
}

// TestValidate_RespectsCtxCancel proves that a canceled context unwinds the
// rule pipeline before any rule executes. Without ctx propagation a CI worker
// that aborts the validate command cannot stop the running rule loop.
func TestValidate_RespectsCtxCancel(t *testing.T) {
	pm := projectWithEarlyAndLateError(t)
	val := NewValidator(pm, "", clock.Real())

	// Baseline: live ctx produces findings.
	live := val.Validate(t.Context())
	require.NotEmpty(t, live)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := val.Validate(ctx)
	assert.Empty(t, canceled,
		"pre-canceled ctx must short-circuit before any rule runs")
}

// TestValidateFailFast_RespectsCtxCancel mirrors TestValidate_RespectsCtxCancel
// for the fail-fast path so the cancellation contract is identical regardless
// of which entry point CI invokes.
func TestValidateFailFast_RespectsCtxCancel(t *testing.T) {
	pm := projectWithEarlyAndLateError(t)
	val := NewValidator(pm, "", clock.Real())

	live := val.ValidateFailFast(t.Context())
	require.NotEmpty(t, live)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := val.ValidateFailFast(ctx)
	assert.Empty(t, canceled,
		"pre-canceled ctx must short-circuit fail-fast before any rule runs")
}

// TestRunGit_RespectsCtxCancel confirms the runGit subprocess wrapper honors
// ctx cancellation. With pre-canceled ctx the call must return an error;
// without ctx threading runGit hardcodes context.Background() and could hang
// indefinitely on slow filesystems (NFS / FUSE).
func TestRunGit_RespectsCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runGit(ctx, "--version")
	require.Error(t, err)
	assert.True(t,
		errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "canceled"),
		"expected canceled error, got %v", err)
}
