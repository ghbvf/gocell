package cell

import (
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// SubscriptionDraft is the first step of the fluent subscription builder
// returned by [Registrar.Subscription]. It exposes exactly one method — CellID —
// so the owning cell id is a COMPILE-TIME red line: the terminal Register lives
// only on [*SubscriptionBuilder], which is reachable only by first calling
// CellID. A hand-written external cell therefore cannot register a subscription
// without naming its cell id, mirroring the compile-HARD positional cellID that
// codegen (cellgen) injects into the positional [Registrar.Subscribe].
//
// SubscriptionDraft holds the back-reference to the recorder and the contract
// spec; both fields are unexported so a non-CellID construction path is not
// expressible outside this package.
//
// ref: ADR docs/architecture/202605111000-adr-subscription-cellid-mandatory.md
type SubscriptionDraft struct {
	reg  *RegistryRecorder
	spec contractspec.ContractSpec
}

// CellID sets the owning cell id (observability owner) and unlocks the rest of
// the builder. It is the only method on SubscriptionDraft, so naming the cell is
// mandatory by construction — skipping it leaves no path to Register.
func (d *SubscriptionDraft) CellID(cellID string) *SubscriptionBuilder {
	return &SubscriptionBuilder{
		reg:    d.reg,
		spec:   d.spec,
		cellID: cellID,
	}
}

// SubscriptionBuilder accumulates the remaining subscription fields after the
// mandatory CellID. ConsumerGroup and SliceID are optional; Handler is required
// and validated at Register (via the Subscribe funnel). Methods mutate and
// return the receiver for chaining.
type SubscriptionBuilder struct {
	reg           *RegistryRecorder
	spec          contractspec.ContractSpec
	cellID        string
	consumerGroup string
	sliceID       string
	handler       outbox.EntryHandler
}

// ConsumerGroup overrides the broker partition key + idempotency namespace. When
// omitted, Register defaults it to the cell id — the common consumerGroup==cellID
// case; role-suffix fanout consumers (e.g. "accesscore-rbac-session-sync") set
// it explicitly.
func (b *SubscriptionBuilder) ConsumerGroup(consumerGroup string) *SubscriptionBuilder {
	b.consumerGroup = consumerGroup
	return b
}

// Handler sets the event handler. Required — Register rejects a nil handler via
// the Subscribe validation funnel.
func (b *SubscriptionBuilder) Handler(handler outbox.EntryHandler) *SubscriptionBuilder {
	b.handler = handler
	return b
}

// SliceID declares the owning slice for subscription observability (optional).
func (b *SubscriptionBuilder) SliceID(sliceID string) *SubscriptionBuilder {
	b.sliceID = sliceID
	return b
}

// Register finalizes the subscription. It defaults ConsumerGroup to CellID when
// unset and delegates to [RegistryRecorder.Subscribe] — the single source of
// truth for subscription validation (nil handler / empty consumerGroup / empty
// cellID / non-event spec / empty topic / spec.Validate()). The builder adds no
// validation of its own, so the two registration forms cannot diverge.
func (b *SubscriptionBuilder) Register() error {
	consumerGroup := b.consumerGroup
	if consumerGroup == "" {
		consumerGroup = b.cellID
	}
	return b.reg.Subscribe(b.spec, b.handler, consumerGroup, b.cellID, WithSubscriptionSliceID(b.sliceID))
}
