package governance

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// --- JOURNEY-CONTRACT-EXISTENCE-01 (inverse direction of REF-07) ---

func TestJOURNEYCONTRACTEXISTENCE01_HappyPath(t *testing.T) {
	pm := validProject()
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateJOURNEYCONTRACTEXISTENCE01(), codeJOURNEYCONTRACTEXISTENCE01)
	assert.Empty(t, got, "validProject baseline references both active contracts via J-ssologin.contracts")
}

func TestJOURNEYCONTRACTEXISTENCE01_ActiveContractWithoutJourney(t *testing.T) {
	pm := validProject()
	// Drop event.session.created.v1 specifically; keep http + projection
	// referenced so the test isolates the single contract under test.
	pm.Journeys["J-ssologin"].Contracts = []string{
		"http.auth.login.v1",
		"projection.session.active.v1",
	}

	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateJOURNEYCONTRACTEXISTENCE01(), codeJOURNEYCONTRACTEXISTENCE01)
	require.Len(t, got, 1)
	r := got[0]
	assert.Equal(t, SeverityError, r.Severity)
	assert.Equal(t, IssueRequired, r.IssueType)
	assert.Equal(t, "contracts/event/session/created/v1/contract.yaml", r.File)
	assert.Equal(t, "id", r.Field)
	assert.True(t, strings.Contains(r.Message, "; fix:"),
		"SeverityError messages must include `; fix:` anchor (INV-3); got: %s", r.Message)
}

func TestJOURNEYCONTRACTEXISTENCE01_DeprecatedContractExempt(t *testing.T) {
	pm := validProject()
	pm.Contracts["event.session.created.v1"].Lifecycle = "deprecated"
	pm.Journeys["J-ssologin"].Contracts = []string{
		"http.auth.login.v1",
		"projection.session.active.v1",
	} // drop reference to the deprecated contract only

	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateJOURNEYCONTRACTEXISTENCE01(), codeJOURNEYCONTRACTEXISTENCE01)
	assert.Empty(t, got, "deprecated contract must not require journey coverage")
}

func TestJOURNEYCONTRACTEXISTENCE01_ExperimentalContractExempt(t *testing.T) {
	pm := validProject()
	pm.Contracts["event.session.created.v1"].Lifecycle = "experimental"
	pm.Journeys["J-ssologin"].Contracts = []string{
		"http.auth.login.v1",
		"projection.session.active.v1",
	} // drop reference to the experimental contract only

	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateJOURNEYCONTRACTEXISTENCE01(), codeJOURNEYCONTRACTEXISTENCE01)
	assert.Empty(t, got, "experimental contract must not require journey coverage")
}

func TestJOURNEYCONTRACTEXISTENCE01_ExampleContractExempt(t *testing.T) {
	pm := validProject()
	// Synthesize an active contract under examples/ with no journey reference.
	pm.Contracts["event.order.created.v1"] = &metadata.ContractMeta{
		ID:               "event.order.created.v1",
		Kind:             "event",
		OwnerCell:        "ordercreate",
		ConsistencyLevel: "L2",
		Lifecycle:        "active",
		Endpoints:        metadata.EndpointsMeta{Publisher: "ordercreate"},
		Dir:              "examples/todoorder/contracts/event/order/created/v1",
		File:             "examples/todoorder/contracts/event/order/created/v1/contract.yaml",
	}

	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateJOURNEYCONTRACTEXISTENCE01(), codeJOURNEYCONTRACTEXISTENCE01)
	assert.Empty(t, got, "examples/ contracts must not require platform journey coverage")
}

// --- JOURNEY-STATUS-LIFECYCLE-01 (board.state × yaml.lifecycle matrix) ---

func TestJOURNEYSTATUSLIFECYCLE01_ZeroFindingsOnTodoExperimental(t *testing.T) {
	// True happy path: every status-board state is paired with an allowed
	// lifecycle AND the active+doing in-transit warning is not triggered.
	pm := validProject()
	// Demote J-ssologin lifecycle to experimental and board state to "todo" —
	// the canonical "not started yet" combination, fully clean.
	pm.Journeys["J-ssologin"].Lifecycle = "experimental"
	pm.StatusBoard[0].State = "todo"
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateJOURNEYSTATUSLIFECYCLE01(), codeJOURNEYSTATUSLIFECYCLE01)
	assert.Empty(t, got, "todo+experimental must produce no findings")
}

func TestJOURNEYSTATUSLIFECYCLE01_TodoMustBeExperimental(t *testing.T) {
	pm := validProject()
	pm.StatusBoard[0].State = "todo"
	// Keep J-ssologin lifecycle=active → "todo"+active is illegal.
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateJOURNEYSTATUSLIFECYCLE01(), codeJOURNEYSTATUSLIFECYCLE01)
	require.Len(t, got, 1)
	r := got[0]
	assert.Equal(t, SeverityError, r.Severity)
	assert.Equal(t, IssueMismatch, r.IssueType)
	assert.Equal(t, "journeys/status-board.yaml", r.File)
	assert.Equal(t, "[0].state", r.Field)
	assert.True(t, strings.Contains(r.Message, "; fix:"))
	// Error message must list the actual allowed set so authors know what to pick.
	assert.True(t, strings.Contains(r.Message, "experimental"),
		"error message must enumerate allowed lifecycles; got: %s", r.Message)
}

func TestJOURNEYSTATUSLIFECYCLE01_DoneRequiresActive(t *testing.T) {
	pm := validProject()
	pm.StatusBoard[0].State = "done"
	pm.Journeys["J-ssologin"].Lifecycle = "experimental"

	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateJOURNEYSTATUSLIFECYCLE01(), codeJOURNEYSTATUSLIFECYCLE01)
	require.Len(t, got, 1)
	r := got[0]
	assert.Equal(t, SeverityError, r.Severity)
	// Allowed set for "done" is just {active}; schema/FMT does not accept stable.
	assert.True(t, strings.Contains(r.Message, "allowed lifecycles for state \"done\": active"),
		"error message must enumerate {active} as the allowed set; got: %s", r.Message)
}

func TestJOURNEYSTATUSLIFECYCLE01_ActiveDoingWarning(t *testing.T) {
	pm := validProject()
	// Baseline already has active+doing — assert it surfaces as Warning, not Error.
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateJOURNEYSTATUSLIFECYCLE01(), codeJOURNEYSTATUSLIFECYCLE01)
	require.Len(t, got, 1)
	assert.Equal(t, SeverityWarning, got[0].Severity)
	assert.Equal(t, IssueMismatch, got[0].IssueType)
	assert.True(t, strings.Contains(got[0].Message, "active journeys should progress toward done"))
}

func TestJOURNEYSTATUSLIFECYCLE01_OrphanEntrySkipped(t *testing.T) {
	pm := validProject()
	// Append a status-board entry that references a non-existent journey.
	pm.StatusBoard = append(pm.StatusBoard, metadata.StatusBoardEntry{
		JourneyID: "J-ghost", State: "todo", Risk: "low",
	})
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateJOURNEYSTATUSLIFECYCLE01(), codeJOURNEYSTATUSLIFECYCLE01)
	// Only the baseline active+doing Warning should be emitted; the orphan
	// entry is left to ADV-04. The new rule must not double-emit.
	require.Len(t, got, 1)
	assert.Equal(t, "J-ssologin", findJourneyIDFromMessage(t, got[0].Message))
}

// TestValidateStrict_JourneyRulesIntegrated guards against silent
// de-registration: a future refactor that moves either JOURNEY rule
// out of rules() would let the per-method unit tests still pass while
// the rule no longer fires in any real validate pipeline. This end-to-end
// assertion catches that drift even before INV-1 (which only checks the
// reflection ↔ registration sets match) would surface it.
func TestValidateStrict_JourneyRulesIntegrated(t *testing.T) {
	pm := validProject()
	// Set up one violation per JOURNEY rule:
	//   - JOURNEY-CONTRACT-EXISTENCE-01: drop projection.session.active.v1
	//     from J-ssologin.contracts so it goes unreferenced.
	//   - JOURNEY-STATUS-LIFECYCLE-01: set board state="todo" while lifecycle
	//     stays "active" → illegal pair.
	pm.Journeys["J-ssologin"].Contracts = []string{
		"http.auth.login.v1",
		"event.session.created.v1",
	}
	pm.StatusBoard[0].State = "todo"

	val := NewValidator(pm, ".", clock.Real())
	results, err := val.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)

	gotContract := findByCode(results, codeJOURNEYCONTRACTEXISTENCE01)
	require.Len(t, gotContract, 1, "JOURNEY-CONTRACT-EXISTENCE-01 must fire via ValidateStrict")
	assert.Equal(t, SeverityError, gotContract[0].Severity)

	gotLifecycle := findByCode(results, codeJOURNEYSTATUSLIFECYCLE01)
	require.Len(t, gotLifecycle, 1, "JOURNEY-STATUS-LIFECYCLE-01 must fire via ValidateStrict")
	assert.Equal(t, SeverityError, gotLifecycle[0].Severity)
}

// findJourneyIDFromMessage extracts the quoted journey ID from a JOURNEY-
// STATUS-LIFECYCLE-01 message body — used only by the orphan-skip test to
// assert which entry produced the surviving finding.
func findJourneyIDFromMessage(t *testing.T, msg string) string {
	t.Helper()
	const open = "journey \""
	i := strings.Index(msg, open)
	if i < 0 {
		t.Fatalf("message did not contain quoted journey ID: %s", msg)
	}
	rest := msg[i+len(open):]
	j := strings.Index(rest, "\"")
	if j < 0 {
		t.Fatalf("message did not have closing quote: %s", msg)
	}
	return rest[:j]
}
