package metadata

import (
	"errors"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// actor_subscribers_test.go tests the Option T atomic flip:
//   - contract.yaml with a literal "subscribers:" key must be rejected by KnownFields
//   - contract.yaml with "actorSubscribers:" is parsed into EndpointsMeta.ActorSubscribers
//   - deriveEventSubscribers merges ActorSubscribers ∪ cell-subscribers into Subscribers
//
// These tests are written FIRST (TDD RED phase) before the implementation.
// They will fail until the types.go + parser.go changes land in the GREEN commit.

// minimalEventContractYAML is the smallest valid event contract.yaml for tests.
// Note: after the Option T flip, "subscribers:" is derived (yaml:"-") and
// must NOT appear in hand-written YAML — its presence causes a KnownFields error.
const minimalEventContractYAML = `id: event.test.created.v1
kind: event
ownerCell: testcell
consistencyLevel: L2
lifecycle: active
endpoints:
  publisher: testcell
replayable: true
idempotencyKey: eventId
deliverySemantics: at-least-once
`

// TestParseContract_LiteralSubscribersKey_RejectsKnownFields asserts that a
// contract.yaml with a hand-written "subscribers:" key is rejected by the
// KnownFields strict decoder. This is the Hard guard: after Subscribers gets
// yaml:"-", any yaml with "subscribers:" in the endpoints mapping is an unknown field.
func TestParseContract_LiteralSubscribersKey_RejectsKnownFields(t *testing.T) {
	contractYAML := `id: event.test.created.v1
kind: event
ownerCell: testcell
consistencyLevel: L2
lifecycle: active
endpoints:
  publisher: testcell
  subscribers: [testcell]
replayable: true
idempotencyKey: eventId
deliverySemantics: at-least-once
`
	fsys := fstest.MapFS{
		"contracts/event/test/created/v1/contract.yaml": &fstest.MapFile{Data: []byte(contractYAML)},
	}
	p := NewParser("")
	_, err := p.ParseFS(fsys)
	require.Error(t, err, "contract.yaml with literal 'subscribers:' must be rejected by KnownFields")
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "expected *errcode.Error, got %T: %v", err, err)
	assert.Equal(t, errcode.ErrMetadataInvalid, ecErr.Code)
	assert.Contains(t, err.Error(), "subscribers",
		"error must name the rejected field 'subscribers'")
}

// TestParseContract_ActorSubscribers_UnmarshalsCorrectly asserts that
// actorSubscribers unmarshal into EndpointsMeta.ActorSubscribers and that
// Subscribers is populated as the derived union after ParseFS.
func TestParseContract_ActorSubscribers_UnmarshalsCorrectly(t *testing.T) {
	contractYAML := `id: event.test.created.v1
kind: event
ownerCell: testcell
consistencyLevel: L2
lifecycle: active
endpoints:
  publisher: testcell
  actorSubscribers: [external-audit-sink]
replayable: true
idempotencyKey: eventId
deliverySemantics: at-least-once
`
	fsys := fstest.MapFS{
		"contracts/event/test/created/v1/contract.yaml": &fstest.MapFile{Data: []byte(contractYAML)},
	}
	p := NewParser("")
	pm, err := p.ParseFS(fsys)
	require.NoError(t, err, "contract.yaml with actorSubscribers must be accepted")

	c := pm.Contracts["event.test.created.v1"]
	require.NotNil(t, c)
	assert.Equal(t, []string{"external-audit-sink"}, c.Endpoints.ActorSubscribers,
		"ActorSubscribers must be populated from actorSubscribers YAML key")
	// Subscribers is derived: since no cell slice subscribes, it equals ActorSubscribers
	assert.Equal(t, []string{"external-audit-sink"}, c.Endpoints.Subscribers,
		"Subscribers must be derived as union (actors only case)")
}

// TestDeriveEventSubscribers_ActorOnly covers a contract with no cell subscribers
// but with actorSubscribers pre-set. Subscribers must equal ActorSubscribers.
func TestDeriveEventSubscribers_ActorOnly(t *testing.T) {
	pm := buildSubscribeProject(
		map[string]*SliceMeta{},
		map[string]*ContractMeta{
			"event.audit.appended.v1": {
				ID:   "event.audit.appended.v1",
				Kind: "event",
				Endpoints: EndpointsMeta{
					Publisher:        "auditcore",
					ActorSubscribers: []string{"external-audit-sink"},
				},
			},
		},
	)
	deriveEventSubscribers(pm)
	subs := pm.Contracts["event.audit.appended.v1"].Endpoints.Subscribers
	assert.Equal(t, []string{"external-audit-sink"}, subs,
		"actor-only contract: Subscribers must equal ActorSubscribers")
}

// TestDeriveEventSubscribers_ActorPlusCells covers a contract with both actor
// subscribers and cell subscribers (derived from slices). The result must be the
// union, deduped and sorted alphabetically.
func TestDeriveEventSubscribers_ActorPlusCells(t *testing.T) {
	pm := buildSubscribeProject(
		map[string]*SliceMeta{
			"ordercell/orderingest": {
				ID:            "orderingest",
				BelongsToCell: "ordercell",
				ContractUsages: []ContractUsage{
					{Contract: "event.order-created.v1", Role: "subscribe", Handler: "HandleEvent"},
				},
			},
		},
		map[string]*ContractMeta{
			"event.order-created.v1": {
				ID:   "event.order-created.v1",
				Kind: "event",
				Endpoints: EndpointsMeta{
					Publisher:        "ordercell",
					ActorSubscribers: []string{"example-order-platform"},
				},
			},
		},
	)
	deriveEventSubscribers(pm)
	subs := pm.Contracts["event.order-created.v1"].Endpoints.Subscribers
	assert.Equal(t, []string{"example-order-platform", "ordercell"}, subs,
		"actor+cell contract: Subscribers must be sorted union of both")
}

// TestDeriveEventSubscribers_ActorContractNoSliceSubscribers covers a contract
// that has only ActorSubscribers and zero slice subscriptions. The contract still
// enumerates in the ALL-event-contracts loop (not just slice-driven contracts),
// so Subscribers is correctly populated from ActorSubscribers alone.
func TestDeriveEventSubscribers_ActorContractEnumeratedAlways(t *testing.T) {
	// There are two event contracts; only one has actor subscribers.
	// The other has a cell slice subscriber. Both must be populated correctly.
	pm := buildSubscribeProject(
		map[string]*SliceMeta{
			"auditcore/auditingest": {
				ID:            "auditingest",
				BelongsToCell: "auditcore",
				ContractUsages: []ContractUsage{
					{Contract: "event.session.created.v1", Role: "subscribe", Handler: "Handle"},
				},
			},
		},
		map[string]*ContractMeta{
			"event.audit.appended.v1": {
				ID:   "event.audit.appended.v1",
				Kind: "event",
				Endpoints: EndpointsMeta{
					Publisher:        "auditcore",
					ActorSubscribers: []string{"external-audit-sink"},
				},
			},
			"event.session.created.v1": {
				ID:   "event.session.created.v1",
				Kind: "event",
				Endpoints: EndpointsMeta{Publisher: "accesscore"},
			},
		},
	)
	deriveEventSubscribers(pm)

	auditSubs := pm.Contracts["event.audit.appended.v1"].Endpoints.Subscribers
	assert.Equal(t, []string{"external-audit-sink"}, auditSubs,
		"audit contract with only actor subscriber must have Subscribers populated")

	sessionSubs := pm.Contracts["event.session.created.v1"].Endpoints.Subscribers
	assert.Equal(t, []string{"auditcore"}, sessionSubs,
		"session contract with only cell subscriber must have Subscribers derived from slices")
}
