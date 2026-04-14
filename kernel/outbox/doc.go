// Package outbox defines interfaces for the transactional outbox pattern:
// Writer (insert within a transaction), Relay (poll-and-publish), Publisher
// (fire-and-forget), and Subscriber (consume).
//
// The package also provides the L4 (Device Latent) command queue state machine:
// CommandEntry, CommandStatus (Pending→Sent→Delivered→Succeeded/Failed/Expired/Canceled),
// three-tier timeouts (ScheduleToSend, SendToComplete, OverallDeadline),
// lifecycle functions (NewCommandEntry, AdvanceCommand, ResetForRetry), and
// adapter injection interfaces (CommandWriter, CommandReader, CommandStateAdvancer).
//
// Implementations live in adapters/ (e.g., adapters/postgres, adapters/rabbitmq).
package outbox
