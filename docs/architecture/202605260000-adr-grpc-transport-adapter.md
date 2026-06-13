# ADR — gRPC Transport Adapter (kind=grpc foundation)

| | |
|---|---|
| **Status** | Accepted (PR 1 of 12) |
| **Date** | 2026-05-26 |
| **Epic** | #1099 gRPC transport adapter |
| **Spec / Plan** | `docs/plans/specs/202605262300-048-grpc-adapter/{spec,plan,data-model}.md` |
| **PR series** | PR 1 (this ADR) … PR 12 (errcode mapping ADR `202605260100`) |
| **Amended** | 2026-06-06 (#1655 — service-level granularity) |

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

The RPC wire details (**service / proto**) are modeled by
`metadata.GRPCTransportMeta` under `EndpointsMeta.GRPC`, mirroring how
`HTTPTransportMeta` lives under `EndpointsMeta.HTTP`. This is the established
codebase convention. (The plan's `data-model.md` illustrative example placed a
top-level `grpc:` key; that form was rejected for breaking symmetry with
`endpoints.http` and colliding with the schema's top-level `additionalProperties:
false`.)

**Amendment (#1655)**: The fields `method` and `streamingType` were present in the
original `GRPCTransportMeta` but were **removed** in PR #1655 (service-level
granularity). A contract now owns a whole proto service; the method set and
streaming kind derive from the `.proto` file (single source of truth). See
§"Amendment 2026-06-06 — D5" below.

### D4 — field validation via schema if/then + contractspec.validateGRPC (no new archtest)

"grpc requires **service+proto**" is enforced by the same mechanisms the other
four kinds use: a `contract.schema.json` if/then block (CI-tested by
`contract_schema_test.go`) plus the runtime guard `contractspec.ContractSpec.
validateGRPC` (mirrors `validateEvent`). The `method` field is **no longer
required** — it was removed in #1655 (service-level granularity per D5). A bespoke
`GRPC-KIND-PARSE-01` AST-scan archtest (proposed in the plan) was **not** added —
an AST scan of decode logic is a fragile string-anchor mechanism (≈ Soft per the
AI-robust charter) and value-presence is inherently a runtime guard, not a
type-system invariant. The issue's declared archtest scope is the closed-set gate
only.

## 威胁矩阵 / 影响

| Concern | Assessment |
|---|---|
| Wire / schema break | None — transport-only; no persistence, no existing-contract change. |
| PII / redaction | None — no new error/log surface in PR 1. |
| Layering (`kernel/` ↛ grpc) | Holds — PR 1 introduces no `google.golang.org/grpc` dependency (that lands in PR 6 behind an `any`-typed field per plan Complexity Tracking). |
| AI-robustness | Closed-set membership = Hard (typed const + archtest); kind validity at the CLI = Soft→Hard upgrade (scaffold now derives from `cellvocab.ParseContractKind`, deleting a duplicate kind map); field presence = Hard schema literal + Medium runtime guard. |

(威胁矩阵逐行 re-eval 见 §"Amendment 2026-06-06 — D5" below.)

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

---

## Amendment 2026-06-06 — D5: grpc contract granularity is service-level (#1655)

### 决策

A `kind: grpc` contract owns a whole **proto service**. The `.proto` file is the
single source of truth for the method set and streaming kind. The `GRPCTransportMeta`
fields `method` and `streamingType` (present in the original PR 1 schema) are
**deleted**; `GRPCTransportMeta` now models **service + proto** only.

The per-RPC `auth` overlay (`GRPCAuthMeta{ Public bool }`) is **also deleted** (PR
#1672 follow-up): a service-level `public` bool cannot express per-method auth once
a contract owns multiple RPCs — the identical premise-loss that justified deleting
`method`/`streamingType` — and nothing consumed it (the runtime `WithPublicMethod`
predicate is wired independently, never read from the contract). It is removed
fail-closed rather than left as a dead, misleading declaration. The per-method auth
model (likely a `endpoints.grpc.methods[]` overlay, a different shape than the old
service-level bool) is deferred to **#1675**; re-adding a field there starts from a
clean slate, not from this vestigial one.

`contractgen.ReadProtoServiceInfo` (replacing the deleted `ReadProtoTypeInfo`)
enumerates all RPCs from the proto service at codegen time. The generated `Server`
interface declares every RPC; the generated registrar wires the whole-service map
(`FullMethod → cellID`) needed for cell attribution.

### 否决的替代方案

- **"minimal" (1 contract = 1 method)**: Conflicts with PR-10 streaming
  `WatchCommands` — a single proto service exposes both `IssueCommand` (unary) and
  `WatchCommands` (server-stream). Requiring one contract per RPC would force
  artificial fragmentation and duplicate `proto` + `service` declarations.
- **"thorough" (custom ServiceDesc)**: Fights grpc-go's whole-service registration
  model (`grpc.ServiceDesc` always covers a complete service). Implementing custom
  per-method `ServiceDesc` would require maintaining a parallel struct that mirrors
  the protoc-generated descriptor — high maintenance, high drift risk.

### Enforcement

- Archtest renamed **`GRPC-SERVICE-IN-CONTRACT-01`** (previously
  `GRPC-METHOD-IN-CONTRACT-01`):
  - **上游 Hard**: generated `reg.GRPCService(...)` call is golden-byte-locked by
    cellgen — any drift from contract.yaml is a codegen regeneration diff.
  - **下游 Medium**: caller-allowlist — `reg.GRPCService` restricted to generated
    `cell_gen.go` + `_test.go`; external-cell path is a permanent Go visibility
    ceiling (won't-do, tracked gh #1631, same family as #851/#893/#1282/#1582).
- The multi-method `Server` interface is **codegen-derived from the proto** (Hard
  funnel) — there is no YAML method list that can drift.

### 威胁矩阵 re-eval

| Concern | Original assessment | Re-eval (#1655) |
|---|---|---|
| Wire / schema break | None — no existing grpc contracts. | **Unchanged. Improves.** There are ZERO production grpc contracts (pre-v1.0 direct evolution per ADR `202605211200`). Deleting `method`/`streamingType` from the schema breaks no consumer. |
| PII / redaction | None. | **Unchanged.** Removing YAML fields introduces no new log or error surface. |
| Layering (`kernel/` ↛ grpc) | Holds. | **Unchanged.** `GRPCTransportMeta` field reduction does not affect the `any`-typed `Register` field boundary. |
| AI-robustness | Closed-set = Hard; field presence = Hard schema + Medium runtime. | **Improves.** `method`-level is now **unexpressible** — the YAML field is gone. The method set is exclusively derived from the `.proto` (Hard codegen funnel). Single-method `method:` declaration was an implicit Soft: an AI co-author could declare the wrong method name with no compile-time check. That footgun is eliminated. |
| Auth granularity / security (#1672) | `auth.public` modeled service-level (PR 1). | **Improves (fail-closed).** The service-level `auth.public` field was **deleted**. It was dead (zero readers — the runtime `WithPublicMethod` predicate never reads the contract) and, under service-level granularity, a single bool could silently mark *every* RPC of a multi-method service JWT-exempt — a latent mixed-auth bypass if a future PR naively wired it. Removal makes the dangerous declaration **unexpressible** (schema `additionalProperties:false` rejects `endpoints.grpc.auth`, locked by `contract_schema_test.go`); per-method auth is **delivered in the #1675 amendment below** as a per-method overlay (a structurally safer shape than the deleted service-level bool). Before #1675 the runtime default was nil predicate = fail-closed (all RPCs authed). |

## Amendment 2026-06-07 — contractgen emits zero artifacts for kind=grpc (#1688)

### 决策

**The single source of a grpc service's Go server contract is buf's generated
`pb.<Svc>Server` interface. contractgen emits NO Go artifact for kind=grpc** — no
`types_gen.go`, no `iface_gen.go`, nothing. The cell author's handler struct
embeds `pb.Unimplemented<Svc>Server` (by value, forward-compat) and implements
the RPC methods directly with the proto-generated request/response types; cellgen
registers it via the already-golden `pb.Register<Svc>Server(r, c.<field>)` call.

This resolves the §D5 phrase "the generated `Server` interface declares every RPC
… (Hard funnel)": **that interface is buf's `pb.<Svc>Server`** (it declares every
RPC, is codegen-derived from the .proto, regenerates on proto change, and carries
`mustEmbedUnimplemented<Svc>Server()` for forward compatibility). A *second*,
contractgen-emitted GoCell `Server` interface was redundant with it and **doubly
broken**: (1) it lacked `mustEmbedUnimplemented<Svc>Server()`, so
`pb.Register<Svc>Server(r, handler)` could not accept a handler that implemented
only it; and (2) it was rendered as `package command` into the *same directory*
as buf's `package commandv1` pb.go (`generated/contracts/grpc/<domain>/vN/`) — a
package-name collision that would not compile. Both detonate at the first real
grpc contract (PR-8 #1151), which is exactly the trigger #1688 records.

### 否决的替代方案

- **Alias** (`type Server = pb.<Svc>Server`): a pure redundant re-export of the pb
  interface; still requires the handler to embed `pb.Unimplemented`, and adds a
  duplicate source for zero value. Rejected per the no-duplicate-source principle.
- **Generated adapter shim** (a clean GoCell business interface + a generated
  `serverShim` embedding `pb.Unimplemented` and forwarding each method): a parallel
  mirror of the protoc-generated descriptor — the same maintenance/drift cost §D5
  already rejected for "custom ServiceDesc". It also double-books error mapping,
  which this design places at the interceptor layer (Kratos `GRPCStatus()` model,
  PR-12), not at a per-handler adapter. No canonical grpc framework (grpc-go,
  Kratos) generates a parallel business interface; go-zero generates a concrete
  embed scaffold, not an interface. Deferred (re-addable cheaply if a real driver
  for framework-mediated DX appears — no backward-compat cost, no external
  consumers).

### Enforcement changes

- `contractgen.contractArtifacts` drops `grpc` from the `types.tmpl` / `iface.tmpl`
  kind sets — kind=grpc joins `webhook` as a zero-artifact kind. `buildGRPCSpec` +
  the contractgen-IR `GRPCEndpointSpec` / `GRPCMethodSpec` are deleted. Proto
  validity is gated solely by the `checkGRPCProtoCollisions` codegen pre-pass
  (validateGRPCProtoPath + ReadProtoServiceInfo, fail-closed per codegen:true grpc
  contract) and governance **FMT-37** (`gocell validate`) — both unchanged.
- **`GRPC-PROTO-REGISTRY-SINGLE-SOURCE-01`** collapses from {C1 constructor-seal,
  C2 golden byte-lock, C3 rendered-import == oracle, C4 collision} to **{C4
  collision-uniqueness}** only. C1/C2/C3 protected the proto import + message types
  emitted into the (now non-existent) grpc iface_gen.go; their protection target
  was removed, not weakened (AI-robust: delete when the premise is gone). The
  tracked single-contract Go ceiling (gh #1525) for the rendered literal is moot.
- **`CODEGEN-CONTRACT-USER-OVERLAP-01`** exempts buf `.pb.go` output (symmetric with
  its existing `_gen.go` exemption): protoc-gen-go output is deterministic codegen,
  not hand-written code, and legitimately lands under `generated/contracts/grpc/`.

### 威胁矩阵 re-eval

| Concern | Re-eval (#1688) |
|---|---|
| Wire / schema break | **Unchanged.** No grpc contract YAML field changes; only the deletion of a never-shipped, never-compiled GoCell-side generated interface. Zero production grpc contracts existed before #1151. |
| PII / redaction | **Unchanged.** No new log/error surface; error redaction stays at the interceptor (Recovery → codes.Internal; errcode→codes table PR-12). |
| Layering (`kernel/` ↛ grpc) | **Unchanged.** The `any`-typed `GRPCServiceSpec.Register` boundary is untouched; cellgen still emits the pb register call. |
| AI-robustness | **Improves.** A register-incompatible, package-colliding duplicate interface is now **unexpressible** (contractgen emits nothing for grpc). The proto-derived Hard funnel (buf `pb.<Svc>Server`) and the collision-uniqueness guard (C4) remain. The `.pb.go` overlap exemption keeps generated/contracts/ hand-written-code-free for plain `.go` while admitting deterministic buf output. |

## Amendment 2026-06-13 — #1675: per-method `public` auth overlay (delivers D5's deferral)

### 决策

D5 deferred per-method auth to #1675 from a clean slate. #1675 adds the optional,
**sparse** `endpoints.grpc.methods[]` overlay (`GRPCMethodMeta{ Name; Public }`):
only RPCs needing a non-default flag appear; an absent method is authed
(fail-closed). The overlay carries **only `public`** in #1675 — the original gap
(mixed public/authed RPC in one service) — fully live-wired end to end:

- contract `endpoints.grpc.methods[].public:true`
- → cellgen derives `GRPCServiceSpec.PublicMethods []string` (full method names
  `/{Service}/{Method}`, byte-locked by the cellgen golden)
- → the runtime `ServiceRegistrar` aggregates them (`IsPublicMethod`)
- → the auth interceptor installs `WithPublicMethod(reg.IsPublicMethod)`
  (`authOptionsWithPublicMethods` in chain.go, the sole sanctioned installer for
  both the unary and stream chains).

The `.proto` remains the **single source of the method set** (D5 unchanged): the
overlay only **annotates** existing proto methods, it never declares them.

### 范围边界 — ABAC fields deferred to #2008 (no dead config)

ABAC `permission`/`resource`/`action` and `internalOnly` are **excluded** from
#1675 and deferred to **#2008** (gRPC→ABAC PDP parity) / PR-11 (per-method internal
boundary). Rationale — the same no-dead-config principle that justified #1672's
deletion of the vestigial service-level `auth.public`: those fields have no live
consumer until #2008 wires the gRPC PDP. Adding them now would recreate the exact
dead-config anti-pattern. #2008 extends `GRPCMethodMeta` with those fields
**additively** (reusing the `methods[]` array, the proto-referential pre-pass, the
codegen merge, and the registry deep-copy this amendment establishes) and widens
FMT-41's vacuous-entry guard from "must assert public:true" to "must assert ≥1
non-default" when the new fields land.

### Enforcement (overlay)

| 载体 | 强度 | 守卫 |
|---|---|---|
| schema item shape (`additionalProperties:false`, `required:[name,public]`, `name minLength:1`, `public const:true`) | **Hard** | `contract_schema_test.go` negative cases (unknown-property; missing/false `public` — the const:true vacuous-entry lock, #2078 F2; forward-protects #2008) |
| referential integrity (each overlay name ∈ proto method set) | **Hard** (codegen funnel, BOTH entry points) | contractgen `checkGRPCProtoCollisions` → `validateGRPCMethodOverlay` (gocell generate contract) **+** cellgen `EnrichGrpcServicesWithProtoInfo` → `validateGrpcPublicMethodsAgainstProto` (gocell generate cell, #2078 F3 — closes the generate-cell-only hole; kernel⊥tools keeps it in the tools layer) |
| cellgen `PublicMethods` emission | **Hard** (byte golden) | `synth_grpc_cell_gen.go.golden` |
| vacuous-entry guard (public:true required) | **Hard + Medium** | schema `public const:true` (Hard, #2078 F2) **+** governance **FMT-41** (Medium) — defense-in-depth, mirrors HTTP schema-if/then + FMT-27 |
| other metadata-pure guards (non-empty name, no dups, **methods⇒codegen:true**) | **Medium** | governance **FMT-41** (`gocell validate`) |
| runtime single-source (registrar = sole production public-method source) | **Medium** | archtest **GRPC-PUBLIC-METHOD-WIRING-FUNNEL-01**, two dimensions: (1) production `WithPublicMethod` refs ⊆ {chain.go}; (2) `authConfig.publicMethod` field writes ⊆ {auth.go} (#2078 F1 — locks the state slot, not just the API ref) |
| fail-closed default | structural | nil overlay → empty `PublicMethods` → empty registrar set → `callPredicate` nil→false → authed |

The **methods⇒codegen:true** guard (FMT-41) closes the one gap the kernel⊥tools
split would otherwise leave: referential integrity lives only in the codegen
pre-pass, which skips `codegen:false` contracts — so an overlay on a `codegen:false`
contract would be unvalidated AND never emit a `PublicMethods` entry (silently
inert dead config). Requiring `codegen:true` makes "any overlay ⟹ passes the
referential pre-pass" provably hold.

### 威胁矩阵 re-eval

| Concern | Re-eval (#1675) |
|---|---|
| Wire / schema break | **Improves / safe.** Additive optional `methods[]`; absent ⇒ prior behavior (all RPC authed). Zero production grpc contracts adopt the overlay yet. |
| PII / redaction | **Unchanged.** No new log/error surface; the overlay is build-time metadata. |
| Layering (`kernel/` ↛ grpc) | **Unchanged.** `GRPCServiceSpec.PublicMethods` is a `[]string` data field (no grpc import); referential integrity stays in the tools layer (kernel⊥tools preserved). |
| Auth granularity / security | **Improves (fail-closed), strictly safer than the deleted service-level bool.** Two load-bearing properties: (1) the overlay is a per-method **annotation**, NOT a method-set re-declaration — D5's "proto is the single source of the method set" is untouched; (2) the #1672-deleted threat ("a single service-level bool silently marks *every* RPC of a multi-method service public") is **structurally absent** in this shape — public is opt-in **per named method**, each name is proto-validated (Hard pre-pass) and golden-locked, the default is fail-closed (authed), and the runtime source is the registrar alone (funnel archtest). You cannot mark a whole service public with one flag; you must enumerate each method, and each must survive referential + golden + governance gates. |
| AI-robustness | **Improves.** Every overlay field has a live reader (no dead config); errors are largely unexpressible (schema Hard) or CI-caught (codegen funnel Hard + FMT-41/funnel Medium). The ABAC deferral to #2008 avoids reintroducing dead config. |
