# ADR: SafeID Wire-Boundary Funnel

- **Date**: 2026-05-19
- **Status**: Accepted
- **Refs**: PR #582, backlog `G-08(c)`, `SAFEID-UPSTREAM-FUNNEL-HARD-01`

## Context

`kernel/outbox.UnmarshalEnvelope` reads wire bytes that originate from
brokers (RabbitMQ, in-memory event bus) controlled by upstream producers.
Pre-PR #582 the wire envelope decoded `msg.ID` and other ID-shaped fields
as plain `string` with only emptiness checks. Downstream code subsequently
fed those strings into `slog.String("entry_id", ...)`, `slog.String(
"event_type", ...)`, prometheus metric labels, and tracing spans. A
compromised producer (or intermediate hop) that supplied
`"evt-1\nlevel=error msg=injected"` could trigger CWE-117 log injection
across every transport touching the envelope. The fault was registered in
backlog `G-08(c)` (`docs/backlog.md:180`).

## Decision

Adopt the **string-typed concept funnel** (charter §"Hard 范本" 第 3 条).

1. Introduce a typed wrapper `pkg/idutil.SafeID` whose `UnmarshalJSON`
   enforces `idutil.IsSafeID` + `MaxMetadataIDLen` (256 B) at the JSON
   decode boundary. Empty payload (`""` or `null`) decodes to the zero
   value — required-field rejection lives at the caller.
2. Type the wire envelope's ID-shaped fields as `SafeID`. This makes
   downstream Hard automatic: the Go runtime dispatches `SafeID.
   UnmarshalJSON` whenever `json.Unmarshal` produces a value for one of
   those fields, regardless of the caller. There is no parser-level
   shape that bypasses the validator while keeping the field typed.
3. Mirror the type change in `ObservabilityMetadata` (TraceID,
   RequestID, CorrelationID). `TraceParent` keeps `string` because it is
   a 55-byte W3C traceparent with its own format validator
   (`validTraceParent`), not an `IsSafeID` member set.
4. Add producer-side fail-fast: `MarshalEnvelope` calls
   `idutil.ParseSafeID` on every Entry ID-shaped field before emitting
   wire bytes. Combined with `Entry.Validate` calling the same routine,
   programmer-constructed unsafe entries fail at write time rather than
   poisoning downstream consumers.
5. Lock the type declarations via the `SAFEID-WIREMESSAGE-USAGE-01`
   archtest: any reversion to `string` or alias substitution fails CI.
   A companion `BlindSpot/NewWireStruct` rule detects parallel wire
   structs that re-introduce the unsafe shape.

## Two-layer trust model

- **WireMessage** (wire boundary): all ID-shaped fields are `SafeID`.
  This is the CWE-117 closure layer.
- **Entry** (in-memory): keeps `string`. Entry is constructed by trusted
  paths (`MustNewEntryID` is `IsSafeID` by construction;
  `UnmarshalEnvelope` casts SafeID → string only after wire-boundary
  validation) and consumed by downstream code that expects plain
  `string` (slog, PG columns, prometheus labels). Defense in depth:
  `Entry.Validate` runs `IsSafeID` over the same five fields; programmer
  errors that fabricate unsafe Entries reject at write time.

## Funnel rating (charter §"Funnel 双向锁评级")

| Direction | Rating | Mechanism |
|-----------|--------|-----------|
| Downstream | **Hard** | Field type `SafeID` makes "decode without validation" unrepresentable. `SAFEID-WIREMESSAGE-USAGE-01` archtest locks the field types. |
| Upstream | **Medium-by-necessity** | `UnmarshalEnvelope` is the only caller invoking `json.Unmarshal` against the envelope across transports today (RabbitMQ subscriber + in-memory event bus). Validated by inspection, not archtest. Upgrade path: `SAFEID-UPSTREAM-FUNNEL-HARD-01` registered in backlog — archtest caller-allowlist for direct `json.Unmarshal(bytes, &WireMessage{})`. |

The downstream Hard alone closes the CWE-117 vector even without
upstream Hard: any caller that decodes a `WireMessage` from JSON will
trigger `SafeID.UnmarshalJSON`. The remaining upstream gap is whether a
direct decode would skip non-SafeID validations (`schemaVersion`,
required-field). That gap is documented in the upgrade backlog.

## Character set choice

`idutil.IsSafeID` allows `[a-zA-Z0-9._:/-]`. The set is the intersection
of:

- UUID v4 canonical form (`xxxxxxxx-xxxx-...`)
- W3C TraceID (32 lowercase hex)
- Outbox event type taxonomy (`order.created.v1`)
- Topic routing keys (`ns/topic.v1`)
- Service/cell ID convention (`accesscore:role`)

Excluded characters (space, control bytes, angle brackets, equals, plus,
unicode) are not reachable from any production ID generator. Existing
production fixtures and tests pass without modification (verified by
zero regression in `make verify` and `hack/verify-archtest.sh`).

## Carve-outs

- `ObservabilityMetadata.TraceParent` stays `string` (W3C 55-byte
  validator, distinct character set).
- `kernel/outbox.Entry` stays `string` (in-memory, post-validation;
  consumed by `slog.String` and PG columns that expect plain string).
  Documented in `SAFEID-WIREMESSAGE-USAGE-01/BlindSpot` allowlist with
  reviewer-judgment requirement on additions.

## Alternatives considered

1. **Inline `IsSafeID` validators inside `UnmarshalEnvelope`**. Rejected
   for AI-rebust Soft rating: any new field addition could forget to
   wire the validator. Charter §"立项硬门槛 ≥ Medium" rejects Soft.
2. **Archtest scanning `UnmarshalEnvelope` body for `IsSafeID` calls
   per field**. Reaches Medium. Still relies on string-anchored
   archtest; type-system enforcement is strictly stronger.
3. **Move `IsSafeID` into a struct-tag validator (e.g., go-playground/
   validator)**. Rejected: pulls a runtime reflection-based validator
   into `kernel/`, expands dependency surface, and gives Medium rating
   (reflection visibility is weaker than Go type system).

## Consequences

- New typed wrapper `SafeID` in `pkg/idutil`. All call sites that
  serialize/deserialize an envelope go through it implicitly.
- Test fixtures that construct `WireMessage` directly need
  `idutil.SafeID(literal)` casts. Documented in `SafeID` godoc as a
  trusted-path bypass; tests that construct attack vectors must assert
  downstream rejection (`makeDeliveryBody` precedent).
- Future ID-shaped wire field additions to `WireMessage` /
  `ObservabilityMetadata` automatically inherit the funnel.
- `validateObservabilityID` removed; `SafeID.Validate` is the single
  source of truth.

## References

- charter `.claude/rules/gocell/ai-collab.md` §"Hard 范本" 第 3 条
  string-typed concept funnel
- charter `.claude/rules/gocell/ai-collab.md` §"Funnel 双向锁评级"
- OpenTelemetry `go.opentelemetry.io/otel/trace.TraceID.IsValid` —
  comparable typed wrapper with embedded validation
- `k8s.io/apimachinery/pkg/util/validation` — exported length-constant
  pattern (`idutil.MaxMetadataIDLen` mirrors)
- Backlog `SAFEID-UPSTREAM-FUNNEL-HARD-01` (upstream Hard upgrade
  path) — see `docs/backlog/cap-13-observability.md`
