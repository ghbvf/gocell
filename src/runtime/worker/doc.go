// Package worker provides background worker lifecycle management for the
// GoCell runtime.
//
// Workers are long-running goroutines that perform periodic or event-driven
// tasks such as outbox polling, event consumption, and scheduled jobs.
//
// # Worker Interface
//
// Each worker implements Start(ctx) and Stop(ctx). The worker manager
// coordinates startup and shutdown, ensuring graceful drain of in-flight
// work during application stop.
//
// # Built-in Workers
//
//   - OutboxPoller: polls the outbox table and publishes pending events
//   - EventConsumer: wraps ConsumerBase for event-driven processing
//   - ScheduledTask: runs a function on a configurable interval
//
// # Error Handling
//
// Worker errors are logged at Error level with full context. Transient
// failures trigger automatic restart with exponential backoff. Permanent
// failures are reported to the health check endpoint.
package worker
