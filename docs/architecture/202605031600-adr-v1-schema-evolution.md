# ADR: v1 schema evolution — lenient response/event, strict request (G5 V1-RESPONSE-EVOLVE)

> Status: Accepted
> Date: 2026-05-03
> ref: `docs/plans/202605011500-029-master-roadmap.md` Track G #G5
> ref: `docs/plans/202604272358-2-2-ci-batch2-k8s-verify.md` PR-CI-3 V1-RESPONSE-EVOLVE
> ref: `docs/backlog2.md` §B2-T-03 / §B2-C-08

## Context

GoCell governance rule **FMT-20** (`kernel/governance/rules_strict_extra.go::validateFMTResponseStrict01`)
treats request and response schemas symmetrically: every `type:object` node in
both must declare `additionalProperties: false`, otherwise `gocell validate
--strict` fails with an error.

This is in direct conflict with the API versioning policy in
`.claude/rules/gocell/api-versioning.md`:

> 新增端点/字段/可选参数 → 不需要[升 v2]，直接在 v1 下添加
> 1. v1 响应只增不删

In practice, any new optional field added to a v1 response is rejected by
clients that validate against the strict schema, forcing every additive change
to bump the major version. The same problem extends to event payloads:
`cells/configcore/internal/events/config_events.go:82` and
`cells/accesscore/internal/dto/config_event_decoder.go:94` call
`json.Decoder.DisallowUnknownFields()`, so the moment a producer adds a new
field, every downstream consumer breaks.

The static schema declarations and the runtime decoder behavior are wired the
same way: 30 `contracts/http/*/v1/response.schema.json`, 17 pairs of
`contracts/event/*/v1/{payload,headers}.schema.json`, and 18 `examples/`
schema files all encode `additionalProperties: false` at every level. A v1
that cannot accept new fields without a v2 bump is not really versioned ──
it is frozen.

The Kubernetes apiserver solves this exact problem by separating the two
sides of the wire: requests run through a `StrictSerializer`
(`staging/src/k8s.io/apiserver/pkg/endpoints/handlers/create.go` selects it
on `?fieldValidation=Strict`), responses bypass field validation entirely
(`transformResponseObject` does not consume the directive). The encoding
serializer in `apimachinery/pkg/runtime/serializer/json/json.go` only honors
`Strict` in `Unmarshal`; `Encode` is always lenient.

## Decision

Adopt the K8s "strict request / lenient response" split across both static
schema declarations and runtime decoder behavior.

### 1. FMT-20 governance — request-only

`strictSchemaRefField` reduces to:

```go
func strictSchemaRefField(field string) bool {
    return field == "schemaRefs.request"
}
```

The implementation functions are renamed to reflect the new semantics:

| Before                              | After                              |
|-------------------------------------|------------------------------------|
| `validateFMTResponseStrict01`       | `validateFMTRequestStrict01`       |
| `validateFMTResponseStrictContract` | `validateFMTRequestStrictContract` |
| `validateFMTResponseStrictRef`      | `validateFMTRequestStrictRef`      |

The rule ID `FMT-20` is preserved (no double-ID, no alias). Schema-tree
walkers (`scanSchemaForStrictMissing`, `walkSchemaObject`,
`walkSchemaTreeDepth`) are reused unchanged because they are
direction-agnostic.

### 2. Schema files — strip `additionalProperties: false`

All v1 response and event schemas are normalized via the new
`hack/scripts/normalize-schema.sh` (jq one-liner, replayable for future
schemas):

| Subtree | File pattern | Files |
|---------|-------------|-------|
| `contracts/http` | `response.schema.json` | 30 |
| `contracts/event` | `payload.schema.json` | 17 |
| `contracts/event` | `headers.schema.json` | 17 |
| `examples/iotdevice/contracts/http` | `response.schema.json` | 10 |
| `examples/iotdevice/contracts/command` | `response.schema.json` | 5 |
| `examples/todoorder/contracts/http` | `response.schema.json` | 3 |

Every level (top + nested) is stripped — no half-strict schemas. All
`request.schema.json` files retain `additionalProperties: false` (FMT-20
still enforces this).

### 3. Event decoder — lenient by default

`cells/configcore/internal/events/config_events.go` and
`cells/accesscore/internal/dto/config_event_decoder.go` drop the
`dec.DisallowUnknownFields()` call. Consumers tolerate new producer fields
without code change.

`pkg/httputil/decode.go` keeps its caller-controlled
`DisallowUnknownFields()` — it serves HTTP request decoding, which is the
strict side of the split.

### 4. Error envelope — exception, stays strict

`contracts/shared/errors/error-response-v1.schema.json` (and its 2
`examples/*/contracts/shared/errors/` mirrors) keep
`additionalProperties: false` at the top level and on the nested error
object. The error envelope is emitted by the framework, has a stable shape,
and is not in scope for the v1-evolution policy. The `details` sub-object
already declares `additionalProperties: true` for free-form context — that
remains.

The schema files retain `additionalProperties: false` at the file level;
they are no longer enforced by FMT-20 (it only scans request schemas), but
the JSON Schema validator at runtime continues to honor the in-file
declaration.

## Consequences

### Positive

- v1 responses and event payloads can grow optional fields indefinitely
  without forcing a v2 bump or breaking clients/consumers.
- Static schema and runtime decoder are now consistent: a field that
  contracttest accepts will also be accepted at runtime, and vice versa.
- Unblocks K#06 CONTRACT-DTO-CODEGEN — the codegen can add fields to
  generated DTOs without a parallel schema-strict update.
- Aligns the framework with the dominant industry pattern (K8s, gRPC,
  Protobuf — all are "lenient on the wire, strict on the contract").

### Negative

- Custom per-endpoint error responses (those not using the shared envelope)
  are no longer guarded for `additionalProperties: false` by FMT-20. The
  convention is to use the shared envelope; new endpoints that diverge will
  not be statically caught. Accepted trade-off.
- Bespoke clients that depended on strict response validation (none known
  inside the repo) would silently accept new fields. This is the intended
  behavior of v1 evolution.

### Neutral

- Rule ID `FMT-20` is unchanged; the semantics are versioned to this ADR.
- Schema files remove `additionalProperties` declarations entirely (rather
  than setting them to `true`). The JSON Schema spec defaults to lenient
  when the key is absent, so the explicit `true` would only be noise.

## References

- ref: kubernetes/kubernetes `staging/src/k8s.io/apiserver/pkg/endpoints/handlers/create.go@master`
- ref: kubernetes/apimachinery `pkg/runtime/serializer/json/json.go@master`
- KEP-2885: server-side unknown field validation
- `.claude/rules/gocell/api-versioning.md`
- `docs/plans/202604272358-2-2-ci-batch2-k8s-verify.md` PR-CI-3
- `docs/plans/202605011500-029-master-roadmap.md` Track G #G5
