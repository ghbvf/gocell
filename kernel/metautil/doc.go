// Package metautil holds shared metadata limits and validation primitives
// used by kernel-level transports (kernel/outbox, kernel/command). Both
// transports historically duplicated identical Max* constants and a near
// identical validateMetadata loop; this package is the single source of
// truth so future churn cannot reintroduce drift.
//
// Domain-specific extensions (e.g. outbox's reserved-key check on the
// observability bridge) layer on top of metautil.ValidateLimits in the
// owning package.
//
// ref: OTel sdk/trace/span_limits.go -- 128 attributes/span (GoCell uses
// 64 as a tighter balance between overhead prevention and practical use).
// ref: NATS server/const.go -- MAX_CONTROL_LINE_SIZE = 4096 bytes.
// ref: Apache Kafka message.max.bytes default 1 MiB (used by callers as
// MaxPayloadBytes upper bound — payload caps live with the owning
// transport, not with metautil).
//
// # Boundary (KERNEL-INTERNAL-DAG-01)
//
// kernel/metautil is a leaf — it has zero kernel→kernel dependencies.
// kernel/outbox and kernel/command import kernel/metautil for the
// shared Max* constants; nothing in the reverse direction.
package metautil
