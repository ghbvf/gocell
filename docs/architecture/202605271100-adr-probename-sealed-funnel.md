# ADR: ProbeName sealed funnel — Registrar.RegisterReadiness 替代 Healthz()

- Status: Accepted
- Date: 2026-05-27
- Tracks: gh issue #1034
- Builds on: `202605241600-adr-ready-probe-name-typed-funnel.md` (OPS-CONTRACT-STRING-FUNNEL-01 原始落地)；`202605161030-adr-cell-repo-readyz-probe.md`（cellgen repo-readiness probe）；`ai-robust.md` §"Hard 范本目录" string-typed concept funnel
- Implemented by: PR resolving issue #1034 (worktree 521-probename-sealed-funnel)

## Context

PR-500（ADR `202605241600`）将 adapter dependency-availability probe 名升级至
`kernel/healthz.ReadyProbeName` typed-string funnel，并由 `OPS-CONTRACT-STRING-FUNNEL-01`
archtest 双向锁守卫。然而，仅覆盖了一个维度：**adapter 侧**的 probe 声明。
升级后 probe 注册通路存在三条独立路径，形成 funnel 三向碎片：

1. **Adapter 侧**（`OPS-CONTRACT-STRING-FUNNEL-01`）：`ReadyProbeName` typed const +
   `adapterutil.HealthToCheckers(name ReadyProbeName, ...)` 已升 Hard。
2. **Framework probe 侧**（`READYZ-PROBE-NAMING-01`）：`config_watcher` /
   `config_drift` / `outbox_failopen_rate_<cellID>` 仍用 `NewProbe` /
   `WithHealthChecker` 的**裸 string**，仅由 hyphen-check archtest（Soft）守卫。
3. **Cell repo probe 侧**（`HEALTHZ-TYPED-REGISTER-01`）：`cellgen RegisterRepoReady`
   生成 typed funnel 内调 `reg.Healthz().Register(...)`，但 `reg.Healthz()` 整体
   把 `Aggregator` 写面（包含 `Register`）暴露在 `Registrar` 接口上——任何持有
   `Registrar` 的代码都可绕过 funnel 直接调 `agg.Register(p)` 注入任意 probe。

三向碎片的根本原因：`Registrar.Healthz() Aggregator` 返回写面后，下游封堵
(`HEALTHZ-WRITE-01/A2 caller allowlist`) 是 archtest-bound Medium，不是 type-system
Hard——新调用点只需在 A2 allowlist 外出现，archtest 才能捕获，AI co-author 可在同一
PR 内同时增 caller + 改 allowlist 完成绕过。

此外，`ReadyProbeName` 类型只覆盖 adapter probe，framework / cellgen probe 使用的
`kernel/healthz.Probe.Name() string` 返回裸 `string`，使构造点检查无法统一到单一
类型对比。

## Decision

### D1 — 单一 `type ProbeName string` 覆盖全部三类 probe

`kernel/healthz.ReadyProbeName` 整体合并进 `kernel/healthz.ProbeName`；删除
`kernel/healthz/readyprobename.go`。所有 adapter 常量值不变，类型从 `ReadyProbeName`
改为 `ProbeName`。`Probe.Name()` 返回类型从 `string` 升为 `ProbeName`；
`NewProbe(name ProbeName, fn ...)` 参数类型同步升。

### D2 — `Registrar.Healthz()` 整体移除；`RegisterReadiness` 是唯一注册入口

`kernel/cell.Registrar` 接口移除 `Healthz() healthz.Aggregator`，新增：

```go
RegisterReadiness(name healthz.ProbeName, p healthz.Probe) error
```

`RegistryRecorder.RegisterReadiness` 实现体：

```go
func (r *RegistryRecorder) RegisterReadiness(name healthz.ProbeName, p healthz.Probe) error {
    return r.registerProbe(healthz.NewProbe(name, p.Check))
}
```

强制以 `name` 为真值源重包一层 `Probe`，确保 name 与 probe 不错配。
`reg.Healthz()` 变成编译错误——type-system Hard gate，无需 archtest 守卫此方法移除。

### D3 — Framework probe 全部接入 typed 构造器

`config_watcher` / `config_drift` 改 `kernel/healthz` 常量：

```go
const ConfigWatcherProbeName ProbeName = "config_watcher"
const ConfigDriftProbeName   ProbeName = "config_drift"
```

`outbox_failopen_rate_<cellID>` 改 composed-name 构造器：

```go
func EmitterFailOpenProbeName(cellID string) (ProbeName, error) {
    return NewProbeName("outbox_failopen_rate_" + cellID)
}
```

`EmitterFailOpenProbeName` 放 `kernel/healthz`（不放 `kernel/cell`），
避免 `kernel/outbox` → `kernel/cell` 反向依赖形成 import 环。

### D4 — `NewProbeName` 是唯一 Validate 入口

```go
func NewProbeName(s string) (ProbeName, error)
```

regex：`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`（snake_case，禁连字符 / 大写 / 前导数字），
长度 ≤ 48。`_ready` 后缀不在 regex 层强制，由 archtest A1 在 sanctioned 包级分流。
**不引入 `MustProbeName`**（优雅简洁自检原则）。

### D5 — cellgen 模板重命名 `RegisterReadiness`

`tools/codegen/cellgen/templates/healthz_gen.tmpl` 生成：

```go
const ProbeRepoReady healthz.ProbeName = "{{.CellID}}_repo_ready"

func RegisterReadiness(reg cell.Registrar, p healthz.RepoProber) error {
    return reg.RegisterReadiness(ProbeRepoReady,
        healthz.NewProbe(ProbeRepoReady, p.RepoReady))
}
```

helper 由 `RegisterRepoReady` 重命名为 `RegisterReadiness`，与
`Registrar.RegisterReadiness` 对齐。5 个生成产物（`cells/{configcore,auditcore,
accesscore}/healthz_gen.go`、`examples/{iotdevice,todoorder}` 对应文件）通过
`gocell generate cell` 重生成，禁止手工编辑。

### D6 — 单 archtest `PROBENAME-SEALED-FUNNEL-01` 替代三条旧规则

`OPS-CONTRACT-STRING-FUNNEL-01`、`READYZ-PROBE-NAMING-01`、`HEALTHZ-TYPED-REGISTER-01`
三条 archtest 随 `reg.Healthz()` 消失变为多余（前两条 registration 路径在 type system
层已无法表达，后一条 `file-identity` 检查的绕过对象已不存在），整体删除，由新文件
`tools/archtest/probename_sealed_funnel_test.go` 接替：

| 层 | 内容 | 评级 |
|----|------|------|
| A1 上游声明 sanction + value 形状 | sanctioned 包集合覆盖 `kernel/healthz` + 8 adapter + `runtime/{websocket,saga}` + cells/<cell>（`healthz_gen.go` + cellgen marker）；每个 `*types.Const of type ProbeName` 值过 regex；adapter-suffix overlay 要求 adapter 包 ProbeName 以 `_ready` 结尾 | Hard 上游（archtest-bound，Go 无 const 可见性 seal） |
| A2 下游 callsite resolves to 声明集 | scan `RegisterReadiness(<expr>, ...)` / `NewProbe(<expr>, ...)` / `HealthToCheckers(<expr>, ...)`；`<expr>` 经 `info.Uses` 解析到 sanctioned `*types.Const`；BasicLit / BinaryExpr / Var / CallExpr fail-closed | Hard 下游（type system）— `NewProbe(name ProbeName, ...)` 让裸 string 编译错；archtest 额外封 `ProbeName("foo")` cast |
| A3 上游 RegisterReadiness 是唯一 write 入口 | scan 全 repo `Aggregator.Register(...)` callsite；allowlist 限于实现文件 + `*_test.go` | Hard 下游（type system）— `reg.Healthz()` 已删，`reg.Healthz()` 是编译错；archtest 兜残余 Aggregator 直 Register |
| A4 NewProbeName const-literal 入参 | `NewProbeName(<expr>)` 必须是 BasicLit 或 string-literal + ident（composed-name 唯一允许形态）；caller allowlist：`kernel/healthz/probename.go` + `*_test.go` | Medium 上游 archtest |
| A5 framework composed-name helper allowlist | golden inventory `EmitterFailOpenProbeName=outbox_failopen_rate_`；scan `"outbox_failopen_rate_" + <x>` BinaryExpr 出现在 `kernel/healthz/probename.go` 外即 fail | Medium 上游 archtest |

B 类盲区反向自检（B1 reflect bypass / B2 helper wrapper indirection / B3 string-cast
bypass / B4 NewProbeName 动态参 / B5 cellgen marker bypass）+ 7 个 RED fixture 配套
自证守卫生效，详见 `tools/archtest/probename_sealed_funnel_test.go` package godoc。

`HEALTHZ-WRITE-01/A1`（HTTP path ban）和 `A3`（Aggregator struct-holder allowlist）
保留，`HEALTHZ-HOLDER-SEAL-01`（gh #893）won't-do 状态不变。

## Consequences

### Positive

- `reg.Healthz()` 是编译错误。新的 probe 注册路径：写 `reg.RegisterReadiness(name, p)`
  — type system 拒绝裸 `string` 入参，编译期 Hard gate，无需 archtest 守此出口。
- 三条散落 archtest 合一（archtest 总量 3 → 1 for this domain），覆盖面更完整。
- Framework probe（`config_watcher` / `config_drift` / `outbox_failopen_rate_<cell>`）
  接入 typed funnel，消除最后的 bare-string probe 注册路径。
- `HEALTHZ-HOLDER-SEAL-01`（gh #893）won't-do 状态维持不变——holder 轴在 Go 类型
  系统无法表达（A3 Medium archtest 是该方向永久天花板），不重开。

### Negative

- 同 PR 内破坏性变更涉及 9 个 adapter 包 + 5 个 cellgen 生成产物 + 3 个手写 cell
  init 文件 + 1 个 outbox emitter 文件 + `bootstrap.WithHealthChecker` option +
  `healthztest.FakeAggregator`；无向后兼容别名（per CLAUDE.md"不考虑向后兼容"）。
- `Probe.Name() ProbeName` 改型流到 slog / fmt.Errorf；`ProbeName` 底层是 string，
  `%q` / `%s` / JSON marshal 表现与 string 一致（无 `MarshalJSON` override），
  `/readyz` verbose wire shape 无破坏性变更（`HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01`
  所保护的 `verboseDependencyEntry` 字段集不变）。
- cellgen golden 必须同 PR 提交（`go test ./tools/codegen/cellgen/...` byte-equal 锁）。

### Neutral

- `CELL-REPO-READYZ-PROBE-01` N2（test-existence backstop `RunRepoReadinessConformance`
  enrollment）独立于 typed funnel，保留为 Medium archtest backstop。
- 5 个 cellgen `healthz_gen.go` 生成产物在本 PR 内通过 `gocell generate cell` 重生，
  禁止手工编辑（per codegen funnel 原则）。

## Alternatives Considered

### A1 — 保留 `ReadyProbeName`，另加 framework-probe 独立 typed（拒绝）

保留 adapter 侧 `ReadyProbeName` + 新增 framework 侧专属 typed，形成并行 funnel。
拒绝理由：并行 funnel 比单一 funnel 更难推理；adapter / framework probe 最终都流入
同一 `Aggregator`，共享 `Probe` 接口的 `Name()` 返回类型不统一导致比较点不可
类型化。合并进单一 `ProbeName` 覆盖更干净。

### A2 — 保留 `reg.Healthz()` 作 test-only 旁路（拒绝）

测试侧直接用 `healthztest.FakeAggregator`，不需要 `reg.Healthz()` 旁路。test-only
豁免在 `*_test.go` 文件内允许直接持有 Aggregator，无需保留 Healthz() 方法。保留
Healthz() 会使 test 与 production 双 allowlist 的边界模糊化，且 A3 archtest 的
file-identity 守卫可直接在 `*_test.go` 范围内豁免，不需要 API 方法来绕。

### A3 — `Registrar.Healthz()` 窄化为只读（拒绝）

只返回 `HealthzReader`（只读 Evaluate / Snapshot），阻止 Register 的暴露。拒绝理由：
任何 Healthz() 形式的返回都意味着 Aggregator 的部分接口泄出到 Registrar，使 holder
耦合仍存在；彻底移除比窄化更干净，且 PR-500 已证明写面封堵可独立由 `RegisterReadiness`
承担。

## Domain collaboration

Sibling healthz funnel issues and their relationship to this ADR (status as of PR #1034):

| Issue | Status this PR |
|-------|---------------|
| #893 HEALTHZ-HOLDER-SEAL-01 | won't-do — Go can't express holder axis; Medium archtest remains the ceiling |
| #891 HEALTHZ-SINK-INTERFACE-NARROW-01 | independent ship — funnel downstream; not subsumed |
| #892 HEALTHZ-PANIC-RECOVERY-LAYER-01 | independent ship — funnel internal |
| #894 CELLGEN-MULTI-PROBE-01 | independent ship — funnel upstream派生 path |
| #633 READYZ-PROBE-FAILURE-METRIC-01 | unblocked — typed ProbeName now flows to metric labels |
| #712 USER-REPO-READYZ-PROBE-01 | unblocked — RegisterReadiness funnel covers user repo |

### D4 amendment — MustProbeName exists; zero production callers enforced

The original §D4 stated "不引入 MustProbeName". In practice, `MustProbeName` was
introduced for use in conformance test fixtures and kernel-internal composers where
an invalid name is a programmer error. The spirit of D4 is preserved as follows:

- `MustProbeName` is defined in `kernel/healthz/probename.go` for use in any
  `*_test.go` file (any package) and the healthztest conformance infrastructure.
- Production callers outside the allowlist are rejected by archtest
  `PROBENAME-SEALED-FUNNEL-01/A4b` (scanner `scanA4bMustProbeNameCallerAllowlist`).
- After fixing F1A (managed_resource.go) and F1B (bootstrap.go), the production
  caller count is exactly zero — only the declaration site and conformance.go remain.
- The B4 blind-spot is documented in the archtest package godoc.

#### Considered alternative — remove MustProbeName entirely

Removing MustProbeName would require every test fixture to call `NewProbeName`
with error checks or use `ProbeName` cast directly (bypassing validation).
Both alternatives are worse for test ergonomics and less AI-robust than keeping
MustProbeName restricted to `*_test.go` with A4b enforcement.

## Open-source benchmarking

| Framework | File | Form | Validation |
|-----------|------|------|------------|
| Kubernetes apiserver healthz | `staging/src/k8s.io/apiserver/pkg/server/healthz/healthz.go::HealthChecker.Name()` | bare `string` | 无 |
| K8s apimachinery validation | `pkg/util/validation/validation.go::IsDNS1123Label` | runtime validator on bare string | 调用方显式调，返回 error slice |
| Uber fx | `fx/lifecycle.go::Hook.{onStartName,onStopName}` | bare string 内部字段 | 无（observability label） |
| Kratos validate | `middleware/validate/validate.go` | bare string error code | 无 |
| Watermill | `message/router.go:328::AddHandler(handlerName string, ...)` | bare string | runtime dedup |
| etcd healthz | `server/etcdserver/api/etcdhttp/health.go::CheckRegistry.Register(name string, ...)` | bare string + runtime panic on unknown | runtime |

gocell 的 `ProbeName` typed-string + sealed `NewProbeName` 构造 + archtest 双锁是**开源
生态内的新形态**——最近对标是 K8s apimachinery 的 `IsDNS1123Label` 形态（runtime
validator on bare string），gocell 把同一约束抬到 type system + CI-time 双锁，严
一档。方向正确性来源于 AI-robust 三档（archtest-bound 上游 = Hard 上限；type system
下游 = Hard）。

ref: kubernetes/apimachinery `pkg/util/validation/validation.go`
ref: kubernetes/kubernetes apiserver `staging/src/k8s.io/apiserver/pkg/server/healthz/healthz.go`

## Implementation matrix（per `.claude/rules/gocell/contract-fanout.md` 5 loci）

```
Contract:     kernel/cell.Registrar 接口 + kernel/healthz.ProbeName typed concept
Change:       Healthz() 移除 + RegisterReadiness 新增 + ReadyProbeName 合并入 ProbeName
Implementations: kernel/cell.RegistryRecorder（唯一实现）
Conformance test: tools/archtest/probename_sealed_funnel_test.go::TestProbenameSealedFunnel
Repro:        go test ./kernel/healthz/... ./kernel/cell/... ./kernel/outbox/...
              ./tools/archtest/ -run TestProbenameSealedFunnel
Dependent contracts:
  - adapters/{postgres,redis,rabbitmq,s3,vault,oidc}.ProbeReady* — 9 consts（类型 rename）
  - cells/{configcore,auditcore,accesscore}/healthz_gen.go — 5 生成产物（重生成）
  - examples/{iotdevice,todoorder} healthz_gen.go — 2 生成产物
  - runtime/{websocket,saga} ProbeCoordinatorReady / ProbeReady — 类型 rename
  - runtime/bootstrap.WithHealthChecker — option 签名升 ProbeName
  - runtime/observability/healthz/healthztest.FakeAggregator — test helper 更新
```

### AI-robust 双向锁评级

| 轴 | 形态 | 评级 |
|----|------|------|
| A1 下游 type system | `NewProbe(name ProbeName, ...)` 让裸 string 编译错；`reg.Healthz()` 编译错 | **Hard 下游** |
| A2 下游 archtest | A2/A3 scan `RegisterReadiness` / `Aggregator.Register` callsite identity | **Hard 下游**（archtest-bound 补充） |
| A1 上游 archtest | sanctioned 包 + value regex + adapter-suffix overlay | **Hard 上游**（archtest-bound，Go const-seal 上限） |
| A4/A5 上游 archtest | `NewProbeName` const-literal 入参 + composed-name helper allowlist | **Medium 上游**（同 PR 开 backlog 跟踪 Hard 升级方向，判断多半 won't-do） |

backlog issue：`PROBENAME-NEWPROBENAME-CALLER-SEAL-01`（A4 Medium 上游 Hard 升级评估）
— 若后续评估确认 NewProbeName 入参的 Go const-seal 可行路径，按 `ai-robust.md`
§"Funnel 双向锁评级" 要求升级。

## Amendment 2026-05-27 — Composed-name length budget (round-2 review F3)

Round-2 review (gh issue #1187 review F3) 指出 `EmitterFailOpenProbeName(cellID)`
存在长 cellID 触发 `NewDirectEmitter` 构造失败的理论回归：
`probeNameMaxLen=48` 与 `pkg/scaffoldid.IdentifierPattern=^[a-z][a-z0-9]+$`
（旧版无上限）的组合下，cellID > 27 chars 时 `outbox_failopen_rate_<cellID>` 超 48
char 上限，emitter 构造直接 fail。生产 cell 名 ≤18 char 实际零触发，但治理面缺
失"两个 single-source 必须互相约束"的硬契约。

修复（同 PR）：

1. `pkg/scaffoldid.IdentifierPattern` 加 32 char 上限：
   `^[a-z][a-z0-9]{1,31}$`（2-32 chars）。理由：对齐 POSIX hostname / K8s
   container name 上限族，且为 healthz composed-name 预留 budget。
2. `kernel/healthz.probeNameMaxLen` 从 48 调至 64：对齐 K8s DNS-1123 label cap
   (63) + 1-char margin。Prometheus 1024 / OTel 256 backend budget 都不受影响。
3. 契约：`prefix(21="outbox_failopen_rate_") + cellID(≤32) = ≤53 < 64`，
   `EmitterFailOpenProbeName` 对任意合法 cellID 都不再 fail。
4. 同步扇出 3 个 JSON schema（assembly / cell / slice）+ `MatchCellID`
   godoc + `scaffoldid.Parse` hint。

测试加固：

- `pkg/scaffoldid.TestParse_Accept` 加 `strings.Repeat("a", 32)` 边界 accept。
- `pkg/scaffoldid.TestParse_Reject` 加 `strings.Repeat("a", 33)` 边界 reject。
- `kernel/healthz.TestNewProbeName_AtBudget` 加 64-char accept + 65-char reject
  双向边界。
- `kernel/healthz.TestEmitterFailOpenProbeName_LongCellID` 验证 32-char
  cellID 不再 fail（"两个 single-source 互相约束" 显式回归测试）。

威胁矩阵 reassessment（amendment 必查项）：原 ADR §"Threat" 段未列长 cellID 回归，
amendment 后无 ✅→⚠️ 项；新增 invariant "EmitterFailOpenProbeName 对任意 32-char
合法 cellID 必成功" 由上面 long-cellID 测试守。

## Amendment 2026-05-27 — ManagedResource.Probes() typed signature (round-2 review F1)

Round-2 review (gh issue #1187 review F1) 指出 PR 落地后仍存在一条 bare-string ingress：
`kernel/lifecycle.ManagedResource.Checkers() map[string]func(context.Context) error`
返回裸字符串 key，由 `runtime/bootstrap.expandManagedResources` 在 composition root
调 `healthz.NewProbeName(name)` 做 runtime 转 typed。这层"边界 validator"虽 fail-fast
拒不合规字符串，但与 ADR 原文 §"sealed funnel" 声称的"不存在 bare-string ingress"
有 wording 差距：

1. `runtime/outbox.Relay` / `runtime/websocket.Hub` / `runtime/saga.Coordinator` 等
   ManagedResource 实现的 probe 名以裸 string literal 维护（`"outbox_relay_poll"` 等），
   不在 `goldenProbeNames` inventory，新增 probe 名无 archtest 强制登记。
2. AI co-author 在 `Checkers()` map 里加新 key 不触发 archtest 红——只要
   `expandManagedResources` 边界 validator 接受即可，缺一致的"声明站点 typed const"
   纪律。

修复（同 PR）：

1. `kernel/lifecycle.ManagedResource.Checkers() map[string]func` →
   `Probes() []healthz.Probe`（typed slice 替换 map）。`kernel/lifecycle` 可 import
   `kernel/healthz`（同 kernel/ 内单向依赖，无循环）；裸 string ingress 在 type
   system 层消除。
2. `adapters/adapterutil.HealthToCheckers(name, fn, timeout) map[string]func` →
   `HealthToProbe(name, fn, timeout) healthz.Probe`。
3. `runtime/outbox` 新增 typed const `ProbePoll / ProbeReclaim / ProbeCleanup`
   （values 不变：`outbox_relay_poll` 等，运维 dashboard / alert 零迁移）。
4. `runtime/bootstrap/managed_resource.go::expandManagedResources` 删
   `healthz.NewProbeName` 转换分支，直接迭代 `r.Probes()` 并 register。
5. archtest `PROBENAME-SEALED-FUNNEL-01`：
   - 删 `newProbeNameAllowlist["runtime/bootstrap/managed_resource.go"]`（NewProbeName
     production callers 从 2 收敛到 1，仅剩 kernel/healthz/probename.go 自身）。
   - `probeNameSanctionedPkgs` 增 `runtime/outbox`。
   - `goldenProbeNames()` 增 3 个 relay typed const inventory 条目。
   - `a2FunnelInternalAllowlist` 增 `adapters/adapterutil/health.go`（实现
     `HealthToProbe` funnel）。
6. `tools/archtest/health_aggregation_test.go` 全部 `Checkers/exposesHealthCheckerMethod`
   重命名为 `Probes/exposesHealthProbeMethod`（archtest 自身的接口形状识别更新）。

威胁矩阵 reassessment：

- 原 §"sealed funnel" 段 ✅ 项"adapter probe 名在 type system 层不可绕过" 现在**真正**满足：
  composition root NewProbeName production caller 计数从 2 → 1，从 "validator boundary"
  升级为 "type-system gate"。
- ⚠️ "bare-string ingress at composition root" 项已**消除**（不再需要补偿措施，gate
  本身上移到 ManagedResource interface）。
- 新增 invariant "每个 ManagedResource 派生 probe 必须在 goldenProbeNames inventory"
  由 archtest A1 sub-rule + golden lock 双向锁；runtime/outbox 3 个 typed const 全部入表。

AI-robust 评级（升级）：

| 轴 | 形态（amendment 后） | 评级 |
|----|---------------------|------|
| ManagedResource 上游 type system | `Probes() []healthz.Probe` 让 `map[string]func` 编译错 | **Hard 上游** |
| 下游 const 声明 + golden inventory | `runtime/outbox` typed const + sanctionedPkgs + golden | **Hard 下游** |

PR 调用接口形态变更：`bootstrap.WithManagedResource(r)` caller 一个 option 不变；只是
resource 实现侧把 `Checkers() map` 改为 `Probes() []healthz.Probe`。生产 cellID 都不含
hyphen，已知场景零行为变更。`adapters/prometheus/exposition_test.go` 的 `"test-cell"`
（hyphen）原 PR 漏 fixup 由本 amendment 修复（改为 `"testcell"`）。
