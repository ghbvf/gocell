package outbox

// test_helpers_test.go — small Subscription/Entry constructors shared across
// the kernel/outbox test suite. After N8 (c) made
// Subscription.Validate the single source of truth for the contract triple,
// every test that reaches SubscribeEntry needs a fully-populated Subscription;
// these helpers keep the call sites uncluttered.

// testFullSub returns a Subscription whose Topic, ConsumerGroup, CellID, and
// full Contract triple (ContractID/ContractKind/ContractTransport) are
// populated, so it satisfies Subscription.Validate. ContractID is derived
// from topic using the canonical "event.<topic>.v1" pattern; CellID defaults
// to the ConsumerGroup so test assertions on ObservabilityID see a non-empty
// owner (K#07 collapsed CellID's fallback to ConsumerGroup, so the helper now
// makes the owner explicit). Tests that need a specific CellID/contract id
// construct the Subscription literal directly.
func testFullSub(topic, cg string) Subscription {
	return Subscription{
		Topic:             topic,
		ConsumerGroup:     cg,
		CellID:            cg,
		ContractID:        "event." + topic + ".v1",
		ContractKind:      "event",
		ContractTransport: "memory",
	}
}
