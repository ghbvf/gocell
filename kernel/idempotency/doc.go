// Package idempotency defines the consumer-side idempotency interface
// (Claimer with two-phase Claim / Commit / Release semantics) used by
// kernel/outbox.ConsumerBase to deduplicate event delivery.
//
// Concrete implementations live in adapters/ (adapters/redis for the
// production Redis-backed claimer; kernel/idempotency/inmem.go for tests
// that need an in-process fake).
//
// Boundary (kernel-internal DAG, see KERNEL-INTERNAL-DAG-01 archtest):
//
// kernel/idempotency imports only kernel/clock (for TTL bookkeeping in
// inmem.go). It is otherwise a leaf — no other kernel sub-module is
// pulled in. kernel/outbox imports kernel/idempotency; nothing in the
// reverse direction.
//
// Default fail-closed: a Claimer fault returns DispositionRequeue (not
// Ack). The retry budget governs how many failures before transition to
// DispositionReject; idempotency loss is treated as an availability
// degradation, not a silent dedup-bypass.
package idempotency
