package outbox

import "time"

// EntryScan is the sanctioned reconstruction funnel for storage adapters that
// rebuild an Entry from persisted columns (the relay claim path scans DB rows
// into it, then calls ToEntry). It exists because Entry fields are unexported
// (sealed construction, issue #1229): an adapter in another package cannot scan
// `rows.Scan(&entry.id, ...)` nor build a composite literal, so the kernel
// exposes this typed, validated rebuild path instead.
//
// EntryScan deliberately carries exported fields — it is a scan target, not a
// producer surface. It has NO wire role (wireMessage remains the only
// json.Unmarshal funnel) and NO clock/ctx injection (the persisted row already
// carries createdAt/occurredAt/observability/principal). ToEntry runs the full
// Entry.Validate, so a corrupt row cannot reconstruct an invalid Entry.
//
// EntryScan is asserted to be the only exported Entry-reconstruction mirror by
// archtest OUTBOX-ENTRY-SEALED-CONSTRUCTION-01; any second exported struct that
// reconstitutes an Entry would defeat the seal.
type EntryScan struct {
	ID            string
	AggregateID   string
	AggregateType string
	EventType     string
	Topic         string
	Payload       []byte
	CreatedAt     time.Time
	OccurredAt    time.Time
	Metadata      map[string]string
	Observability ObservabilityMetadata
	Principal     PrincipalMetadata
	FailurePolicy FailurePolicy
}

// ToEntry assembles a sealed Entry from the scanned columns and validates it.
// Storage adapters MUST route persisted-row reconstruction through this method
// rather than attempting to construct an Entry directly.
func (s EntryScan) ToEntry() (Entry, error) {
	e := Entry{
		id:            s.ID,
		aggregateID:   s.AggregateID,
		aggregateType: s.AggregateType,
		eventType:     s.EventType,
		topic:         s.Topic,
		payload:       s.Payload,
		createdAt:     s.CreatedAt,
		occurredAt:    s.OccurredAt,
		metadata:      s.Metadata,
		observability: s.Observability,
		principal:     s.Principal,
		failurePolicy: s.FailurePolicy,
	}
	if err := e.Validate(); err != nil {
		return Entry{}, err
	}
	return e, nil
}
