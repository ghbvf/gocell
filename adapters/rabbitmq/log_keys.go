package rabbitmq

// Structured log field keys used by rabbitmq adapter code. ConsumerBase used to
// live here and defined these keys; after the move to kernel/outbox they are
// redeclared locally so subscriber/connection log lines stay consistent with
// the kernel-level consumer middleware.
// logKeyEventID is the slog structured-field key (snake_case per observability.md).
// The wire JSON header field declared in event headers.schema.json is "eventId" (camelCase).
const (
	logKeyEventID = "event_id"
	logKeyTopic   = "topic"
)
