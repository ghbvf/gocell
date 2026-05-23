# ADR: SafeID Wire-Boundary Funnel

- **Date**: 2026-05-19
- **Status**: Accepted (amended 2026-05-22 — upstream upgraded to Hard via type-system seal)
- **Refs**: PR #582, gh issue #713, backlog `G-08(c)`, `SAFEID-UPSTREAM-FUNNEL-HARD-01` (closed)

## §0 Amendment (2026-05-22) — Upstream Hard via Type-System Seal

Issue #713 (`SAFEID-UPSTREAM-FUNNEL-HARD-01`) tracked the upstream funnel
upgrade from Medium-by-necessity to Hard. The original backlog entry
proposed an archtest caller-allowlist; that approach was rejected on
review because charter §"Funnel 双向锁评级" rates archtest caller-allowlists
as Medium, not Hard. The industry precedent for an unexported codec
gating wire decode is Kratos `transport/grpc/codec.go` (zero-size
unexported codec struct with single registration); etcd `server/wal/wal.go`
provides an adjacent "sealed handle via unexported fields + factory-only
construction" pattern at the `WAL` handle level (distinct from etcd's
wire-level `wal.Record`). Watermill `message.Message` is NOT a direct
precedent — its UUID/Metadata/Payload are exported; only the ack
lifecycle is sealed via unexported channels.

**Adopted change**: rename `kernel/outbox.WireMessage` → `wireMessage`
(lowercase, package-private). The public envelope I/O surface is unchanged
— `MarshalEnvelope(entry Entry) ([]byte, error)` and `UnmarshalEnvelope(
topic, raw) (Entry, error)` neither expose nor accept the envelope type
in their signatures, so the rename is API-transparent to external callers.

After the seal:
- Cross-package `outbox.WireMessage{...}` / `var msg outbox.wireMessage` /
  `json.Unmarshal(b, &outbox.WireMessage{})` are compile-time errors.
- Go package-level visibility is the upstream Hard mechanism; the
  `SAFEID-UPSTREAM-FUNNEL-HARD-01` archtest is a regression guard against
  future re-exports (struct or alias).
- The closed Hard funnel is now: Hard upstream (Go visibility +
  archtest naming check) + Hard downstream (SafeID field types + reflective
  deny-by-default archtest).

**Threat-matrix re-evaluation** (per charter §"ADR amendment 落地必查"):

| Threat | Pre-amendment | Post-amendment |
|--------|---------------|---------------|
| Wire-decode bypass via direct `json.Unmarshal(b, &WireMessage{})` outside `UnmarshalEnvelope` | ⚠️ Medium-by-necessity (would skip `schemaVersion` / required-field checks; SafeID still fires) | ✅ Compile-time impossible (cross-package reference to `wireMessage` is forbidden) |
| Future re-export under ANY exported name: alias (`type Envelope = wireMessage`), defined-type sharing underlying (`type Envelope wireMessage`), or fresh struct copy with the canonical wire-shape fields | ❌ Not detected | ✅ Caught by `SAFEID-UPSTREAM-FUNNEL-HARD-01` checks 5+6: types.Unalias-normalized identity equality (alias / materialized `*types.Alias`); underlying-struct identity equality (defined-type); SchemaVersion + ≥7/10 canonical field overlap (fresh struct). AST `NoReExport` reverse self-test additionally guards the exact-name `type WireMessage` token. |
| In-memory `SafeID(rawUnsafe)` cast within a trusted package | ⚠️ Permitted (Go's max grade for typed strings) | ⚠️ Unchanged — `MarshalEnvelope`'s `ParseSafeID` producer-side fail-fast still rejects at marshal time |
| Test helper that builds attack-vector wire bytes (negative testing) | ⚠️ Used `WireMessage{}` literal cast bypass | ✅ Tests build raw `[]byte` JSON templates; the seal forbids in-Go construction, aligning with the principle that wire-format faults are byte-level |

Industry references (commit message `ref:` slugs):

- ref: go-kratos/kratos `transport/grpc/codec.go` — zero-size unexported
  codec struct with single registration. **Primary equivalent**: the
  unexported codec type gating decode is the same form-class as GoCell's
  unexported `wireMessage` gating `outbox.UnmarshalEnvelope`.
- ref: etcd-io/etcd `server/wal/wal.go` — `WAL` handle sealed via
  unexported fields + factory-only constructors (`Create` / `Open` /
  `OpenForRead`). **Cited at the handle level**, not at the wire-level
  `wal.Record` which has different framing semantics.
- Watermill `message.Message` is intentionally NOT cited as a precedent —
  its UUID/Metadata/Payload fields are exported; only the ack channels
  are sealed. Envelope construction is reachable cross-package; that
  shape is a partial seal, not the Hard upstream guarantee this funnel
  requires.

The rest of this ADR retains its original decision and trust model;
sections that originally read "Medium-by-necessity" have been rewritten
below in §"Funnel rating" per charter §"ADR amendment 落地必查" (no
two-truths history retention).

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

- **wireMessage** (wire boundary, package-private after §0 amendment): all ID-shaped fields are `SafeID`.
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
| Downstream | **Hard** | Field type `idutil.SafeID` makes "decode without validation" unrepresentable. `SAFEID-WIREMESSAGE-USAGE-01` archtest reflectively locks the field types (deny-by-default with explicit carve-outs). |
| Upstream | **Hard** (since §0 amendment 2026-05-22) | Wire envelope struct `wireMessage` is package-private. Cross-package `outbox.WireMessage{...}` / `outbox.wireMessage{...}` / `json.Unmarshal(b, &outbox.WireMessage{})` are compile-time errors — Go package-level visibility is the upstream type-system seal. `SAFEID-UPSTREAM-FUNNEL-HARD-01` archtest is the regression guard (verifies unexported `wireMessage` exists, no exported `WireMessage` re-export, canonical field set intact; reverse self-test AST-scans for `type WireMessage` declarations). |

Closed Hard funnel: the only paths from `[]byte` ↔ envelope semantics
across package boundaries are `outbox.MarshalEnvelope(Entry) ([]byte,
error)` and `outbox.UnmarshalEnvelope(topic, raw) (Entry, error)`.
Neither signature exposes the envelope struct; both call SafeID-aware
producer / consumer paths internally (`MarshalEnvelope` invokes
`idutil.ParseSafeID` per ID-shaped field; `UnmarshalEnvelope` relies on
the Go runtime's `json.Unmarshal` → `SafeID.UnmarshalJSON` dispatch +
required-field and `schemaVersion` post-checks).

CWE-117 closure: both upstream and downstream layers independently
close the log-injection vector. The amendment removes the prior
"Medium-by-necessity" carve-out — direct `json.Unmarshal` against the
envelope is now syntactically inexpressible from outside `kernel/outbox`.

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
   for AI-robust Soft rating: any new field addition could forget to
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
- Cross-package test fixtures that previously built `outbox.WireMessage{
  ID: idutil.SafeID(...), ...}` directly migrate to
  `outbox.MarshalEnvelope(entry)` (safe inputs) or raw `[]byte` JSON
  templates (attack-vector negative tests). The §0 amendment makes
  `outbox.WireMessage` cross-package reference a compile error, so
  attack-vector tests cannot accidentally rely on in-Go construction.
- Future ID-shaped wire field additions to the unexported `wireMessage`
  / `ObservabilityMetadata` automatically inherit the funnel.
- `validateObservabilityID` removed; `SafeID.Validate` is the single
  source of truth.

## References

- charter `.claude/rules/gocell/ai-robust.md` §"Hard 范本" 第 3 条
  string-typed concept funnel
- charter `.claude/rules/gocell/ai-robust.md` §"Funnel 双向锁评级"
- OpenTelemetry `go.opentelemetry.io/otel/trace.TraceID.IsValid` —
  comparable typed wrapper with embedded validation
- `k8s.io/apimachinery/pkg/util/validation` — exported length-constant
  pattern (`idutil.MaxMetadataIDLen` mirrors)
- Backlog `SAFEID-UPSTREAM-FUNNEL-HARD-01` — closed by issue #713
  amendment §0 (upstream sealed via Go visibility; archtest
  `SAFEID-UPSTREAM-FUNNEL-HARD-01` is the regression guard)
- ref: go-kratos/kratos `transport/grpc/codec.go` — primary equivalent
  (zero-size unexported codec struct, single registration)
- ref: etcd-io/etcd `server/wal/wal.go` — sealed handle pattern
  (unexported fields + factory-only construction; cited at `WAL` handle
  level, distinct from wire-level `wal.Record`)
- Watermill `message.Message` is NOT cited as a precedent — exported
  UUID/Metadata/Payload fields make envelope construction reachable
  cross-package; only ack channels are sealed (partial seal)
