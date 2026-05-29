# ADR — gRPC Transport Adapter (kind=grpc foundation)

| | |
|---|---|
| **Status** | Accepted (PR 1 of 12) |
| **Date** | 2026-05-26 |
| **Epic** | #1099 gRPC transport adapter |
| **Spec / Plan** | `docs/plans/specs/202605262300-048-grpc-adapter/{spec,plan,data-model}.md` |
| **PR series** | PR 1 (this ADR) … PR 12 (errcode mapping ADR `202605260100`) |

## 背景

GoCell ships two transports today: HTTP (`runtime/http`) and AMQP
(`kernel/outbox` + `ConsumerBase`). The gRPC feature (epic #1099) adds a third,
sliced into 12 independently-shippable PRs. **PR 1 is the foundation**: it makes
`kind: grpc` a first-class member of the contract-kind closed set and teaches the
metadata + contractspec layers to parse and validate a grpc contract. No
transport runtime, adapter, interceptor, codegen, or example cell is introduced
here (PR 2–12). The first real grpc `contract.yaml` lands in PR 8; PR 1's grpc
paths are exercised by unit tests with synthetic fixtures only.

The contract-kind set was a closed enumeration `{http, event, command,
projection}` enforced at four layers: the `cellvocab.ContractKind` typed const +
parser (canonical), governance FMT-09 (`validKinds`), the JSON schema enum, and
the archtest `CONTRACT-KINDS-CLOSED-SET-01` filesystem gate. `command` (PR #937
analog) and `projection` are the precedents for adding a kind.

## 决策记录

### D1 — kind=grpc extends the closed set (no constitution-process amendment)

`grpc` is added to the canonical `cellvocab.ContractKind` enum + `ParseContractKind`,
and to every downstream layer that enumerates or resolves kinds, in this PR:
governance `validKinds` (FMT-09) + endpoint-resolution helpers, the JSON schema
`kind` enum + id pattern, `kernel/registry` Provider/Consumers, `runtime/devtools`
catalog, and the `CONTRACT-KINDS-CLOSED-SET-01` archtest map. `command`/`projection`
are the precedent — a kind extension, not a model change. The constitution §III
table is updated (四种→五种 + a `grpc` row) to keep the supreme single source of
truth factually accurate; no heavyweight amendment process is required because the
enforced gate is the archtest + typed const, not the prose table.

### D2 — gRPC mirrors HTTP for provider/consumer/roles

Provider field `server`, consumer field `clients`, roles `serve`/`call` — identical
to `http`. This reuses `EndpointsMeta.Server`/`Clients` (no new endpoint fields)
and lets every kind switch resolve grpc with a one-line case. `ProviderEndpoint()`,
registry, governance helpers, FMT-07, and devtools all return server/clients for
grpc.

### D3 — grpc transport subtree lives at `endpoints.grpc` (parallel to `endpoints.http`)

The RPC wire details (service / method / streamingType / proto / auth.public) are
modeled by `metadata.GRPCTransportMeta` under `EndpointsMeta.GRPC`, mirroring how
`HTTPTransportMeta` lives under `EndpointsMeta.HTTP`. This is the established
codebase convention. (The plan's `data-model.md` illustrative example placed a
top-level `grpc:` key; that form was rejected for breaking symmetry with
`endpoints.http` and colliding with the schema's top-level `additionalProperties:
false`.)

### D4 — field validation via schema if/then + contractspec.validateGRPC (no new archtest)

"grpc requires service+method+proto" is enforced by the same mechanisms the other
four kinds use: a `contract.schema.json` if/then block (CI-tested by
`contract_schema_test.go`) plus the runtime guard `contractspec.ContractSpec.
validateGRPC` (mirrors `validateEvent`). A bespoke `GRPC-KIND-PARSE-01` AST-scan
archtest (proposed in the plan) was **not** added — an AST scan of decode logic is
a fragile string-anchor mechanism (≈ Soft per the AI-robust charter) and value-
presence is inherently a runtime guard, not a type-system invariant. The issue's
declared archtest scope is the closed-set gate only.

## 威胁矩阵 / 影响

| Concern | Assessment |
|---|---|
| Wire / schema break | None — transport-only; no persistence, no existing-contract change. |
| PII / redaction | None — no new error/log surface in PR 1. |
| Layering (`kernel/` ↛ grpc) | Holds — PR 1 introduces no `google.golang.org/grpc` dependency (that lands in PR 6 behind an `any`-typed field per plan Complexity Tracking). |
| AI-robustness | Closed-set membership = Hard (typed const + archtest); kind validity at the CLI = Soft→Hard upgrade (scaffold now derives from `cellvocab.ParseContractKind`, deleting a duplicate kind map); field presence = Hard schema literal + Medium runtime guard. |

## Enforcement (this PR)

- `CONTRACT-KINDS-CLOSED-SET-01` archtest: `grpc` added to the closed set.
- `cellvocab.ContractGRPC` typed const + `ParseContractKind`/`ValidRolesForKind` cases.
- `contract.schema.json`: kind enum + id pattern + `endpoints.grpc` if/then block.
- `contractspec.validateGRPC` + governance FMT-07/FMT-09 grpc cases.
- Scaffold CLI validity now derives from `cellvocab.ParseContractKind` (single source).

## 不在本 ADR 决议（later PRs）

- contractgen/cellgen grpc codegen + bundled `scaffold cell --with-grpc` — PR 2 / PR 6 (real proto + codegen template).
- `adapters/grpc` server, `runtime/grpc/interceptor` chain, bootstrap `WithGRPCListener`, `Cell.GRPCService` registrar — PR 3–7.
- example/platform cells, streaming, observability parity — PR 8–11.
- errcode.Kind → codes.Code mapping — PR 12 (ADR `202605260100`).
