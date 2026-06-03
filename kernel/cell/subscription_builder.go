package cell

import (
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// subscriptionDraft is the first step of the fluent subscription builder
// returned by [Registrar.Subscription]. It exposes exactly one method — CellID —
// so the owning cell id is a COMPILE-TIME red line: the terminal Register lives
// only on *subscriptionBuilder, which is reachable only by first calling CellID.
// A hand-written external cell therefore cannot register a subscription without
// naming its cell id, mirroring the compile-HARD positional cellID that codegen
// (cellgen) injects into the positional [Registrar.Subscribe].
//
// Both subscriptionDraft and subscriptionBuilder are UNEXPORTED (sealed
// construction, same shape as kernel/outbox.Entry / metrics.CellLabel): an
// external package can chain methods on the value returned by Subscription but
// can neither name nor zero-value-construct either type, so there is no path to
// the terminal Register that bypasses CellID. Skipping CellID() yields a compile
// error — the returned draft type has no Register method — making the
// missing-CellID path unexpressible upstream as well as downstream (true
// type-system Hard, not a runtime fallback).
//
// ref: ADR docs/architecture/202605111000-adr-subscription-cellid-mandatory.md
type subscriptionDraft struct {
	reg  *RegistryRecorder
	spec contractspec.ContractSpec
}

// CellID sets the owning cell id (observability owner) and unlocks the rest of
// the builder. It is the only method on subscriptionDraft, so naming the cell is
// mandatory by construction — skipping it leaves no path to Register.
func (d *subscriptionDraft) CellID(cellID string) *subscriptionBuilder {
	return &subscriptionBuilder{
		reg:    d.reg,
		spec:   d.spec,
		cellID: cellID,
	}
}

// subscriptionBuilder accumulates the remaining subscription fields after the
// mandatory CellID. ConsumerGroup and SliceID are optional; Handler is required
// and validated at Register (via the Subscribe funnel). Methods mutate and
// return the receiver for chaining. The type is unexported so it cannot be
// forged outside this package (sealed construction — see subscriptionDraft).
type subscriptionBuilder struct {
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
func (b *subscriptionBuilder) ConsumerGroup(consumerGroup string) *subscriptionBuilder {
	b.consumerGroup = consumerGroup
	return b
}

// Handler sets the event handler.
// Handler is the only required field validated at runtime (Register returns an
// error whose message contains "handler"); CellID is compile-enforced and
// ConsumerGroup defaults to CellID.
func (b *subscriptionBuilder) Handler(handler outbox.EntryHandler) *subscriptionBuilder {
	b.handler = handler
	return b
}

// SliceID declares the owning slice for subscription observability (optional).
// Codegen-generated cells inject SliceID from slice metadata; hand-written
// external cells may omit it (the event router then uses CellID as the
// observability owner at cell granularity).
func (b *subscriptionBuilder) SliceID(sliceID string) *subscriptionBuilder {
	b.sliceID = sliceID
	return b
}

// Register finalizes the subscription. It defaults ConsumerGroup to CellID when
// unset and delegates to [RegistryRecorder.Subscribe] — the single source of
// truth for subscription validation (nil handler / empty consumerGroup / empty
// cellID / non-event spec / empty topic / spec.Validate()). The builder adds no
// validation of its own, so the two registration forms cannot diverge.
func (b *subscriptionBuilder) Register() error {
	consumerGroup := b.consumerGroup
	if consumerGroup == "" {
		consumerGroup = b.cellID
	}
	return b.reg.Subscribe(b.spec, b.handler, consumerGroup, b.cellID, WithSubscriptionSliceID(b.sliceID))
}
