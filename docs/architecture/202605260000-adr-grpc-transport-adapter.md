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

## Amendment 2026-06-15 — #2008: per-method ABAC `permission` overlay + interceptor PDP gate (delivers #1675's ABAC deferral)

#1675 deferred the ABAC fields to #2008 (§"范围边界" above). #2008 delivers them:
non-public gRPC RPCs now pass the same ABAC PDP decision as HTTP routes, via a
**transport-level interceptor gate driven by a contract-derived method→permission
map** — replacing the prior **hand-written `s.authorize()` predicate inside the
devicecommandrpc handler** (the "手写谓词" the issue targets). `permission` is
**authorization** (runs after authentication); it is the authorization sibling of
#1675's `public` (authentication bypass) — separate sources, separate interceptor
options.

### Design

The funnel mirrors the #1675 public-method funnel:

```
contract endpoints.grpc.methods[].permission: "device:command"
  → GRPCMethodMeta.Permission (kernel/metadata)
  → FMT-41 vacuous/mutex/closed-set guards + contractgen/cellgen completeness pre-pass
  → cellgen GrpcServiceGenSpec.MethodPermissions → cell_gen.go GRPCServiceSpec.MethodPermissions (map[string]string)
  → registrar.methodPermissions (string→sealed authz.Permission, fail-fast on unknown) → PermissionForMethod
  → interceptor authorize() PDP gate (unary + stream share one core)
  → auth.Authorizer.Authorize(subject, fullMethod, permission.String())
```

The composition root wires the **same** cell-provided Authorizer into the gRPC
interceptor (`interceptor.Deps.Authorizer = dc.Authorizer()`) that
`bootstrap.WithPrimaryAuthorizer` wires into the HTTP primary listener — so gRPC
method authorization is the identical PDP decision.

### Strict fail-closed (supersedes #1675's "sparse, absent⇒authed" for the permission dimension)

Under #2008 a non-public RPC with **no** permission overlay entry is **DENIED** at
the gate (no mapping → deny), not merely authed. The overlay is therefore
**complete** for non-public methods: every authed RPC is either `public:true` or
carries a `permission`. The contractgen/cellgen completeness pre-pass rejects an
uncovered non-public proto method at codegen — turning "forgot the overlay → silently
dead 403 method" into a build failure (the build-time enforcement of the strict
fail-closed model). Resource forwarded to the PDP is the full method name (coarse,
mirrors HTTP `RequirePermission` forwarding `r.URL.Path`).

### Supersedes specific #1675 statements

- Schema item shape: `required:["name","public"]` → **`required:["name"]`**; `public`
  keeps `const:true` (public:false stays meaningless) but is no longer required; a new
  flat `permission` (string, minLength:1) is added; a vacuous-entry `anyOf`
  (public:true OR permission) and a public ⊕ permission `if/then` mutex replace the
  bare const-only lock.
- FMT-41 vacuous guard: "must assert public:true" → **"must assert ≥1 non-default
  (public:true OR permission)"**, plus a public⊕permission mutex and a closed-set
  check (`authz.IsKnownPermissionString`, governance→pkg/authz, layering-legal).
- "Zero production grpc contracts adopt the overlay yet" → **iotdevice
  `grpc.device.command.v1` adopts it** (IssueCommand + WatchCommands, both
  `device:command`); the hand-written `devicecommandrpc.Server.authorize` is removed.

### Enforcement (permission overlay)

| 载体 | 强度 | 守卫 |
|---|---|---|
| schema item shape (`required:[name]`, `permission` minLength:1, vacuous anyOf, public⊕permission mutex) | **Hard** | `contract_schema_test.go` cases (permission-only valid; mutex/empty-permission/missing-both invalid) |
| referential + **completeness** (every non-public proto RPC covered by public OR permission) | **Hard** (codegen funnel) | cellgen `EnrichGrpcServicesWithProtoInfo` → `validateGrpcMethodOverlayAgainstProto` |
| permission ∈ closed authz registry | **Hard + Medium** | governance **FMT-41** (`authz.IsKnownPermissionString`, static) **+** registrar `authz.PermissionByName` fail-fast at registration (runtime) |
| cellgen `MethodPermissions` emission | **Hard** (byte golden) | iotdevice `cell_gen.go` (verified by `gocell verify codegen-cell`) |
| public ⊕ permission mutex + vacuous-entry | **Hard + Medium** | schema (Hard) **+** governance **FMT-41** (Medium) |
| runtime single-source (registrar `PermissionForMethod` + composition-root Authorizer = sole production gate source) | **Medium** | archtest **GRPC-PERMISSION-GATE-WIRING-FUNNEL-01**, two dimensions: (1) production `WithPermissionResolver`/`WithPDPAuthorizer` refs ⊆ {chain.go}; (2) `authConfig.permissionFor`/`authConfig.authorizer` field writes ⊆ {auth.go} |
| fail-closed (no mapping / no Authorizer / deny / non-zero obligation / PDP error → deny) | structural + tested | interceptor `authorizePermission` decision table (unary + stream table tests) |

### Deferred (registered follow-up issues / documented boundary)

- **transport-neutral `authz.MethodPolicyResolver`** (the issue's "重构"): would
  require migrating HTTP's per-route hand-written gates to contract-derived metadata —
  **#2205**. **Delivered (base wave) in PR #2350** — `authz.MethodPolicyResolver`
  interface (`pkg/authz`), `auth.NewStaticMethodPolicyResolver` + `RequirePermissionForContract`
  (`runtime/auth`), `endpoints.http.permission` overlay + cellgen-derived cell resolver,
  governance `FMT-42`, archtest `HTTP-PERMISSION-GATE-WIRING-FUNNEL-01`, and the configcore
  pilot (all 5 HTTP slices). The gRPC `ServiceRegistrar` satisfies the same interface via a
  one-line assertion (zero wiring change). Remaining HTTP cells (accesscore owner-scoped /
  auditcore / examples) + the PERMISSION-BASED-AUTHZ-01 Hard-ification stay deferred to the
  PR-13 follow-ups tracked off #2205.
- **`internalOnly`**: PR-11 internal cell-to-cell gRPC boundary.

> **Delivered in #2207** (see "Amendment 2026-06-18 — #2207" below), no longer
> deferred: **owner-scoped per-message resource extraction** (the gRPC analog of HTTP
> `RequirePermissionForResource`) and **gRPC `WatchCommands` device:consume** (a device
> watching its own command queue). The earlier "not feasible generically in an
> interceptor" boundary is superseded: a stream wrapper that gates on the first
> `RecvMsg` makes it both feasible and fail-closed. The threat-matrix rows are
> re-evaluated in that amendment.

> **Delivered in the 2026-06-16 #2204 review hardening** (see amendment below), no
> longer deferred: **gRPC startup fail-fast for a nil Authorizer** (was a tracked DX
> parity follow-up) and **gRPC PDP-decision metrics parity** (was **#2206**, now
> closed). The threat-matrix rows below are re-evaluated accordingly.

### 威胁矩阵 re-eval (#2008)

| Concern | Re-eval (#2008) |
|---|---|
| Wire / schema break | **Safe (pre-GA window).** `permission` is additive; `required` narrows to `[name]` (a relaxation); the iotdevice contract + cell_gen + handler update atomically in one PR. |
| PII / redaction | **Unchanged.** The gate logs nothing new to wire; gRPC status messages are const literals (subject/action only in server-side slog, same as the prior handler gate). The #2204 review adds a machine-readable `google.rpc.ErrorInfo` detail to deny statuses, but its `Metadata` carries only non-PII routing keys (method, and permission when resolved) — never subject or token (see the 2026-06-16 amendment, F6). |
| Layering (`kernel/` ↛ grpc, `kernel/governance` → pkg/authz) | **Safe.** `GRPCServiceSpec.MethodPermissions` is `map[string]string` (no authz import in kernel); string→Permission resolution happens in runtime/grpc. governance→pkg/authz is layering-legal (authz imports only errcode/tenant/stdlib; covered by the existing kernel `framework/pkg` depguard allow). |
| Auth granularity / security | **Improves (fail-closed).** Authorization moves from a per-handler hand-written predicate to a declarative, contract-derived, transport-level gate enforced before the handler (403-before-validation preserved). Strict fail-closed: a non-public method with no permission is denied (and rejected at codegen). HTTP F5 obligation-fail-closed is mirrored. The gate runs inside the shared `authorize` core, so unary + stream are at parity and a panicking PDP collapses to codes.Internal (existing stage guard). |
| AI-robustness | **Improves.** The method→permission map is codegen-derived + golden-locked (Hard); a typo'd permission fails statically (FMT-41 closed-set) and at registration (fail-fast); the runtime gate source is funnel-locked (GRPC-PERMISSION-GATE-WIRING-FUNNEL-01); the completeness pre-pass makes "forgot a permission" a build failure rather than a silent dead method. The #2204 review adds two registration-time fail-fast guards (F1 nil-Authorizer-with-gated-methods; F2 method-key referential integrity) — both Medium runtime guards surfaced at the phase7b drain, before serve (see the 2026-06-16 amendment). |

## Amendment 2026-06-16 — #2204 review hardening: startup parity, error model, metrics parity, referential integrity

The six-dimension review of #2204 surfaced gaps between the #2008 gRPC PDP gate and
its HTTP sibling. They are delivered in the same PR (not deferred) — the gate now
reaches availability + operability parity with HTTP. Each item below states its
mechanism + AI-robust rating; the #2008 threat matrix rows above are re-evaluated.

### F1 — startup fail-fast for a permission-gated spec with no Authorizer (Medium)

HTTP fails fast at router build (`bootstrap.ResolveAuthorizer`) when a permission-gated
listener has no Authorizer. gRPC previously fail-closed only at request time (boot +
`grpc_ready` pass, then every protected RPC 403s at first call). Now `NewServerInterceptors`
threads `!IsNilInterface(deps.Authorizer)` into the minted registrar
(`runtimegrpc.WithPermissionGate`); `ServiceRegistrar.Register` fail-fasts (panicregister
`grpc-registrar-permission-gate-unwired`) when a spec carries non-empty `MethodPermissions`
but no Authorizer is wired. The drain runs in phase7b (after Init, before Serve), so the
wiring bug surfaces at startup — true HTTP parity. Guard rating Medium (registration-time
runtime fail-fast); covered by `registrar_test.go` (unwired→panic / wired→ok / no-gated→ok).

### F2 — method-key referential integrity at registration (Medium)

`MethodPermissions` / `PublicMethods` overlay KEYS are now cross-checked at `Register`
against the methods the spec actually registered (collected per-callback in
`cellScopedRegistrar.localMethods`); an unknown full-method key fail-fasts
(`grpc-registrar-unknown-method-key`) instead of silently producing a dead 403 at request
time. This is defense-in-depth alongside the build-time contractgen + FMT-41 guards (it
catches a hand-written spec bypassing codegen). Covered by `registrar_test.go`.

### F6 — machine-readable deny reasons via google.rpc.ErrorInfo (Hard enum)

Every gRPC auth/authz denial now carries a `google.rpc.ErrorInfo` detail (canonical
code + human message for humans; `Reason` + `Domain="gocell.authz.grpc"` + non-PII
`Metadata` for clients), so a client distinguishes no-mapping / not-wired / denied /
obligation / unavailable / invalid-token / password-reset without parsing English text.
`Reason` is drawn from a **sealed closed set** (`denyReason`, unexported field +
package-level values + `allDenyReasons` registry) — `deniedStatus` only accepts a
`denyReason`, so a raw string can never reach the slot (Hard). ALL interceptor denial
branches route through the one helper (authz gate AND pre-PDP authn — no half-migrated
bare `status.Error`). `Metadata` carries only method (+ permission when resolved), never
subject/token (PII row re-eval above). Covered by `auth_reason_test.go`.

### F8 — gRPC PDP-decision metrics parity (centralized)

`interceptor.Deps` gains `MetricsProvider`; `NewServerInterceptors` wraps the Authorizer
in `auth.NewObservableAuthorizer` when a real provider is configured (`kernelmetrics.IsReal`
— the single source now shared with bootstrap `hasRealMetricsProvider`), so gRPC PDP
decisions reach the same `auth_pdp_decision_*` series as HTTP. Centralized in the chain
(every gRPC cell gets it, not per-composition-root). The metric family is shared with the
HTTP path via the provider's `registerOrReuse` (same name + labels → no double-registration,
no transport label, no cardinality blowup). A Nop/nil provider leaves the Authorizer bare
(metrics best-effort, never gate the verdict). This **closes #2206**. Covered by
`chain_pdp_metrics_test.go` + `isreal_test.go`.

### Test + governance closure

- iotdevice assembly-level e2e (`cells/devicecell/grpc_pdp_gate_test.go`): real
  `deviceAuthorizer` + real overlay + real interceptor over a socket — asserts the gate's
  allow/deny/unauthenticated verdicts for unary (IssueCommand) and stream (WatchCommands).
- cellgen completeness negative control extended to a multi-RPC fixture (full / partial-missing
  / unknown-key); the unknown-permission-VALUE check is FMT-41 + registrar, documented as a
  cellgen contract-of-absence test.
- `GRPC-PERMISSION-GATE-WIRING-FUNNEL-01` negative control strengthened to per-symbol
  anti-vacuity (each protected option func / field individually asserted).
- Stale "ABAC fields deferred to #2008" godoc in `kernel/metadata/schema_types.go` corrected
  (`Public` + `Permission` are both live exported overlay fields).

## Amendment 2026-06-18 — #2207: owner-scoped per-message resource extraction (gRPC RequirePermissionForResource parity)

#2008 left `resource = fullMethod` (coarse) and documented per-message resource extraction
as deferred ("not feasible generically in an interceptor; a stream has no message at open").
#2207 delivers it: a device can now gate gRPC `WatchCommands` on its OWN id via the owner-scoped
`device:consume` permission, the gRPC analog of HTTP `RequirePermissionForResource`.
`WatchCommands` is the real-time NOTIFICATION arm of the device:consume lifecycle — a read-only
doorbell over the device's active-command queue; the device still claims + executes commands via
the HTTP dequeue/ack path (which leases and returns the payload), so the stream does not itself
consume. The reusable contribution is the **generic per-message resource-extraction capability**,
not a stream-side consume protocol. The earlier infeasibility boundary is superseded — wrapping
the stream so the gate runs on the FIRST `RecvMsg` (when the request message IS available) makes
it feasible and fail-closed. The threat matrix below is unchanged by this framing: the gate,
fail-closed behavior, and PII handling are identical whether the stream is called a notification
or a consume doorbell.

### Mechanism

- **Contract**: `endpoints.grpc.methods[].resource` (optional) names the REQUEST-MESSAGE field
  (proto snake_case, e.g. `device_id`) whose value becomes the PDP `resource`. `WatchCommands`
  becomes `permission: device:consume` + `resource: device_id`; `IssueCommand` stays the coarse
  `device:command`. Mutually exclusive with `public`; valid only with a `permission`.
- **Permission scope (the key AI-robustness primitive)**: `authz.Permission` gains a
  machine-readable `ownerScoped` bit (sealed minter `newPermission(s, scope)`, accessor
  `IsOwnerScoped()`). The pre-existing prose invariant "coarse vs ownership is NEVER folded into
  one Permission" is now TYPED, not documented. The authoritative owner-scoped set is the machine
  source `authz.Permission.IsOwnerScoped()`, frozen (count + membership) by
  `permission_test.go::TestPermissions_OwnerScopedPinnedSet` — at this writing `access:decide,
  device:consume, device:read, user:read, user:write, role:read, order:read, order:update`.
- **Generate-time cross-check (Hard)**: the cellgen completeness pre-pass
  (`validateGrpcMethodOverlayAgainstProto` → `validateOwnerScopedResourceSymmetry`) fails the
  build if an owner-scoped permission lacks a `resource` selector (else the device owner is
  silently locked out — `fullMethod` never equals the device id, so `subject == resource` never
  fires) OR a coarse permission carries one (inert/misleading). This is the sibling of #2008's
  "dead 403" completeness gate.
- **Derivation + startup re-check (Medium)**: cellgen derives `GRPCServiceSpec.MethodResources`
  (golden-locked); the registrar validates each resource method-key against the registered method
  set (stale-key fail-fast) and exposes `ResourceFieldForMethod`.
- **Interceptor**: a method with a resource selector → the field is read via protoreflect and
  canonicalized with the SAME `httputil.ParseCanonicalUUID` HTTP uses (UUID → canonical, else
  forwarded raw — exact parity). Unary extracts from `req` at interceptor entry; server-streaming
  DEFERS the WHOLE permission gate to the first `RecvMsg` via `resourceGatedStream` (the open-time
  coarse gate MUST NOT run, or it would deny the owner before the per-message check). The gate is
  the single `authorizePermission` decision function with a `resource` parameter.
- **F3 fail-closed**: only a STRUCTURAL extraction failure (not a proto.Message / declared field
  absent / wrong kind) denies (`RESOURCE_UNRESOLVED`, never falls back to fullMethod). A
  value-level case (empty / non-UUID) is FORWARDED to the PDP — denying on value would wrongly
  block admin/operator, who pass coarsely and never consult the resource.
- **Wiring funnel**: `WithResourceResolver(reg.ResourceFieldForMethod)` is installed only in
  `chain.go` (archtest **GRPC-METHOD-RESOURCE-FIELD-FUNNEL-01**, the third auth dimension beside
  the #1675 public-method bypass and the #2008 permission gate).

### 威胁矩阵 re-eval (#2207)

| Concern | Re-eval (#2207) |
|---|---|
| Wire / schema break | **Safe (pre-GA window).** `resource` is additive; flipping `WatchCommands` to `device:consume` changes the auth requirement (a breaking wire change) but the contract + cell_gen + e2e update atomically in one PR (no external consumer). |
| PII / redaction | **Unchanged.** The extracted resource (a device id) is used ONLY for the decision; it NEVER enters `denyMeta` / `google.rpc.ErrorInfo.Metadata` (still method + permission only). Verified by a PII assertion in the resource tests. |
| Layering | **Safe.** `MethodResources` is `map[string]string` in kernel (no authz/proto import). The interceptor adds `google.golang.org/protobuf` — curated into the `runtime-isolation` depguard allow-list (the canonical companion to the already-allowed `google.golang.org/grpc`, scoped to per-message field reflection). cellgen→pkg/authz (for `IsOwnerScoped`) is tooling, layering-legal. |
| Auth granularity / security | **Improves (fail-closed, owner-scoped).** A device authorizes against its OWN id (`subject == resource`), not a coarse role gate; cross-device access is denied by the tenant/device-agnostic ownership rule (e2e `cross-device` case). Owner-scoped streaming defers the gate to first-RecvMsg but still BEFORE the user handler runs (the generated server-stream handler Recvs the single request first). Structural extraction failure fails closed. |
| AI-robustness | **Improves.** The coarse-vs-owner taxonomy is now a typed `Permission` bit (Hard sealed marker) instead of prose; the owner-scoped⟺resource symmetry is a Hard generate-time build failure (prevents the silent owner lock-out — the most security-relevant regression); derivation is golden-locked; the resolver wiring is funnel-locked. The owner-scoped permission set is frozen by a value-golden test (anti-vacuity: count + membership). |
