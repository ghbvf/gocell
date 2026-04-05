id: {{.ID}}
kind: event
ownerCell: {{.OwnerCell}}
consistencyLevel: L2
lifecycle: draft
endpoints:
  publisher: {{.OwnerCell}}
  subscribers: []
replayable: true
idempotencyKey: event_id
deliverySemantics: at-least-once
