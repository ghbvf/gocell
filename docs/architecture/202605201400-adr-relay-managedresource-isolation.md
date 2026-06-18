# ADR: Relay ↔ ManagedResource 类型隔离

> Status: Accepted
> Date: 2026-05-20
> Implementation: worktree 655-pr593-relay-isolation
> Closes: PR #593 review fix-up §6 (W4 P2#6) + backlog `BOOTSTRAP-RELAY-DOUBLE-MANAGED-UPSTREAM-HARD-01`

## Context

PR #593 (`fix/625-pr589-fix-followups`) 之后 `runtime/bootstrap.WithRelay`
仍存在一个边界 bug：调用顺序 `WithRelay(r1) + WithManagedResource(r1) +
WithRelay(r2)` 时，runtime guard `preflightDoubleManagedRelay` 只看 `b.relay`
（即 `r2`），找不到 r1 的双重注册——r1 在 `managedResources` 里残留两份，
shutdown 路径 LIFO 双 Close。

PR #593 ship 之前 review 留下了 backlog 升级条目
`BOOTSTRAP-RELAY-DOUBLE-MANAGED-UPSTREAM-HARD-01`（`docs/backlog/20260520/
202605191800-pr589-review-fixup-backlog.md`）：双向锁 funnel 下游 Hard / 上游
Medium 的过渡形态需要进一步把 *Relay 隐藏在 sealed wrapper 后，使
`WithManagedResource(*Relay)` 编译期不可表达。本 ADR 落地该升级，同时彻底
关掉边界 bug。

### 根因

- `*runtime/outbox.Relay` 公开实现 `kernel/lifecycle.ManagedResource`
  （`Checkers() / Worker() / Close(ctx) error`），任何接受 `ManagedResource`
  接口的 API 都会接它。
- `WithRelay` 内部 `append(managedResources, relay)` 与 `WithManagedResource`
  本质等价；runtime guard 必须在 phase0 期再验，是字面约定的
  Soft 上游（AI-robust 章程禁止 Soft 立项）。

## Decision

把 `*Relay` 隔离出 `kernel/lifecycle.ManagedResource` 接口。所有
relay-as-managed-resource 的语义统一收口到 `runtime/bootstrap` 包内的
unexported `relayAdapter`——**single sanctioned holder**（ai-robust.md Hard
范本）。

### D1. `*Relay` 不再实现 `ManagedResource`

`runtime/outbox/relay.go` 删除：

- 编译期断言 `_ kernellifecycle.ManagedResource = (*Relay)(nil)`
- 公开方法 `Close(ctx context.Context) error`

`Start` / `Stop` 仍保留（由 `kout.Relay` + `kernel/worker.Worker` 接口要
求），`Checkers` / `Worker` 仍保留（供 bootstrap 适配器消费）。

### D2. `runtime/bootstrap.relayAdapter` 唯一 sanctioned holder

新文件 `runtime/bootstrap/relay_adapter.go`：

```go
type relayAdapter struct{ relay *runtimeoutbox.Relay }

func newRelayAdapter(r *runtimeoutbox.Relay) *relayAdapter { return &relayAdapter{relay: r} }

func (a *relayAdapter) Checkers() map[string]func(context.Context) error { return a.relay.Checkers() }
func (a *relayAdapter) Worker() kworker.Worker                            { return a.relay.Worker() }
func (a *relayAdapter) Close(ctx context.Context) error                   { return a.relay.Stop(ctx) }
```

adapter struct + constructor 均 unexported。bootstrap 包外没有任何方式构
造或访问。`WithRelay` 改为：

```go
func WithRelay(r *runtimeoutbox.Relay) Option {
    return func(b *Bootstrap) {
        if r == nil { return }
        if b.relay != nil {
            panic(panicregister.Approved("bootstrap-relay-rebind",
                errcode.Assertion("bootstrap: WithRelay called more than once; ...")))
        }
        b.relay = r
        b.managedResources = append(b.managedResources, newRelayAdapter(r))
    }
}
```

二次绑定走 panic taxonomy B 类（programmer-error parameter）。

### D3. preflight runtime guard 删除

`runtime/bootstrap/phases_assembly.go::preflightDoubleManagedRelay` 及其在
`bootstrap.go::Run` 的调用点完全删除。双注册在 type system 上已不可表达，
runtime guard 不再有意义；保留即是双源真理。

### D4. archtest `RELAY-NOT-MANAGEDRESOURCE-01`（Hard 下游回归守卫）

`tools/archtest/relay_isolation_test.go` 在统一 type universe 中加载
`runtime/outbox` + `kernel/lifecycle` + `runtime/bootstrap` 三包，断言：

- `types.Implements(*runtime/outbox.Relay, kernel/lifecycle.ManagedResource)`
  必须为 `false`——核心规则。
- `types.Implements(*runtime/bootstrap.relayAdapter, kernel/lifecycle.ManagedResource)`
  必须为 `true`——反向自检（防止 filter 空挂）。

未来任何 PR 让 *Relay 再次实现 ManagedResource（最可能：恢复
`Close(ctx) error` 方法）都会被 archtest 红。

## AI-robust 评级

| 维度 | 评级 | 依据 |
|------|------|------|
| 下游 Hard | ✅ | `*Relay` 不实现 `ManagedResource`，`WithManagedResource(relay)` 在 Go 编译期 type-mismatch，AI 可绕过性 = 0 |
| 上游 Hard | ✅ | `relayAdapter` + `newRelayAdapter` 双 unexported，包外不可表达替代路径；`WithRelay` 二次绑定 `panicregister.Approved` 兜底 |
| 总评 | **Hard funnel（闭环双向锁）** | ai-robust.md §"Funnel 双向锁评级" 通过 |

形态归属：**single sanctioned holder + type isolation**（ai-robust.md Hard
范本目录第 5 项）。

## 影响面

| 文件 | 变更 |
|------|------|
| `runtime/outbox/relay.go` | 删除 `Close` 方法、`ManagedResource` 编译期断言；godoc 同步 |
| `runtime/outbox/relay_test.go` | 删除编译期断言；`TestRelay_ImplementsManagedResource` 重命名为 `TestRelay_ProvidesLifecyclePrimitives`，断言改 `Stop` 而非 `Close` |
| `runtime/bootstrap/relay_adapter.go` | **新增** unexported adapter + constructor |
| `runtime/bootstrap/options_events.go` | `WithRelay` 用 adapter + 二次绑定 panic |
| `runtime/bootstrap/phases_assembly.go` | 删除 `preflightDoubleManagedRelay` |
| `runtime/bootstrap/bootstrap.go` | 删除 preflight 调用点 |
| `runtime/bootstrap/phases_events_test.go` | DoubleManaged 测试改为 `Rebind_Panics` + 反向自检注释；`AutoLifecycle_RelayAddedToManagedResources` 改为断言 adapter 类型 |
| `runtime/bootstrap/managed_resource_test.go` | TM2/TM3/TM4 三处 `WithManagedResource(relay)` 改 `WithRelay(relay)`；comment 同步 |
| `tests/integration/l2atomicity/harness_test.go` | `WithManagedResource(relayWorker)` → `WithRelay(relayWorker)` |
| `examples/ssobff/app.go` | 同上 |
| `tools/archtest/relay_isolation_test.go` | **新增** RELAY-NOT-MANAGEDRESOURCE-01 + 反向自检 |

## 威胁矩阵

PR #593 review 提出的双注册路径与本 ADR 落地后的状态：

| 威胁 | PR #593 现状 | 本 ADR 落地后 |
|------|-------------|--------------|
| `WithRelay(r) + WithManagedResource(r)` 双注册 | runtime guard 拦截（Medium） | type system 拒绝（Hard，编译失败） |
| `WithRelay(r1) + WithManagedResource(r1) + WithRelay(r2)`：r1 漏检 | **未拦截**（bug） | type system 拒绝 `WithManagedResource(r1)` 一项即编译失败 |
| `WithManagedResource(r1) + WithManagedResource(r1)` 同一 relay 双 ManagedResource | 未覆盖 | type system 拒绝（不能把 *Relay 传给接受 ManagedResource 的 API） |
| 二次 `WithRelay(r2)`（不同 relay） | 静默覆盖 b.relay，r1 残留 managedResources | panic via panicregister.Approved（B 类）fail-fast |
| 未来恢复 `Close` 方法绕过 type isolation | n/a | archtest `RELAY-NOT-MANAGEDRESOURCE-01` 红 |

## 回滚约束

- 不接受恢复 `*Relay.Close(ctx) error` 为公开方法——archtest 红。
- 不接受让 `relayAdapter` / `newRelayAdapter` 导出——破坏 single sanctioned
  holder 形态，外部即可重新构造直接持有 `*Relay` 的 ManagedResource。
- 不接受新增 `bootstrap.WithRelayManagedResource(ManagedResource)` 这类
  门户——等价于回到 PR #593 之前。

## Implementation matrix

```
Contract: kernel/lifecycle.ManagedResource interface satisfaction by *runtime/outbox.Relay
Change: *Relay no longer implements ManagedResource; runtime/bootstrap.relayAdapter is the sole sanctioned holder
Implementations: [x] runtime/bootstrap.relayAdapter (only sanctioned)
Conformance test: tools/archtest.TestRELAY_NOT_MANAGEDRESOURCE_01
Repro: go test ./tools/archtest -run 'TestRELAY_NOT_MANAGEDRESOURCE_01' -count=1
Dependent contracts (governance scan): none — ManagedResource interface signature unchanged
```

## Amendment 2026-06-18 — #2152 PR-1：relay 运行期 fan-out（keyed-by-instance）

> 本 amendment 重写上文 §Decision D2 与 §威胁矩阵中「单 relay per Bootstrap」的隐含前提。
> 原文针对 PR #593 单 relay 边界 bug；#2152 把 relay 通道提升为按去重基建实例 keyed 的集合。

### 背景

#1964（PR #2151，MERGED）落地 per-cell DB 凭据/连接 seam（`cellmodules/percellpg`），但运行期
relay 仍是单例：`WithRelay` 第二次调用 panic（单 relay 不变式），故 distinct per-cell 基建连接
无法真正运行（percellpg 对 >1 distinct DSN fail-closed）。#2152 PR-1 建立 **bootstrap relay
fan-out seam**：relay 通道一体重构为「按去重基建实例 keyed」集合。

### 变更

- **keying 维度 = sealed `InfraInstanceKey`**（`runtime/bootstrap/infra_instance_key.go`）：
  unexported `id` 字段 → 包外不可 struct-literal 伪造非默认值（Hard sealed construction）；
  `DefaultInstanceKey()` = 零值（colocated 单实例哨兵），`NewInfraInstanceKey(id)` 在 mint 期
  校验 id 为 snake_case 标识符（≤32）。**该键在 PR-1 冻结，PR-2 复用给 pub/sub，不改键、不重写
  relay 第二遍。**
- **`WithRelay(key, r)`**：`b.relay *Relay` 单字段 → `b.relaysByInstance map[InfraInstanceKey]*Relay`。
  同 key 重绑 panic（语义从「per Bootstrap 唯一」→「per instance key 唯一」，仍防 double-managed
  危害）；不同 key 累加（fan-out），各 append 一个 `relayAdapter`。
- **`relayAdapter` 结构不变**——仍包单个 relay；现在有 N 个实例。LIFO teardown（slice）天然支持 N。
  唯一新增：非默认实例的 relay 操作 probe 经 sanctioned `healthz.RelayInstanceProbeName` 按 instance
  id 命名空间化（`outbox_relay_poll_<id>`），避免 N relay 在全局 probe 名冲突；colocated 默认实例
  probe 名不变（运维契约保持）。relay_adapter.go 因此进 `PROBENAME-SEALED-FUNNEL-01/A2` 内部
  allowlist（与 emitter/projection/tailer 同族 composed-name 构造）。
- **`pub/sub` 通道不动**（仍单例）：relay 各自在构造期已持自己的 publisher，故 relay 通道独立可 key；
  broker（pub/sub）fan-out 归 PR-2。
- **不在本 PR**：`percellpg` fail-closed 保留（生产 per-cell-provider 馈送未接前放行 distinct DSN 会
  不一致）；composition `SharedDeps.PG` 单例→per-cell 线程化、`cap_wiring` 开 N 池 = follow-up issue。

### `RELAY-SOLE-HOLDER-01` 重写（D4 扩展，Hard 不变）

`RELAY-NOT-MANAGEDRESOURCE-01` 不变。`RELAY-SOLE-HOLDER-01`（原 PR-593 后续新增）**直接重写**为
keyed-by-instance 不变式（不走「先保留后推翻」）：

- 保留**类型级 sole-holder**：ManagedResource 实现者中只有 `relayAdapter` 类型可持 relay。N 个
  relayAdapter **实例**合法（即 fan-out）；替代 holder **类型**仍拒。
- **强化**：`fieldHoldsRelay` 递归 slice/array/map element——任何 MR 类型持 `[]*Relay`/`map[K]*Relay`
  relay 集合都被拒（堵 keyed 世界出现的「替代 collection-holder MR」绕过路径）。fan-out 集合本身
  （`Bootstrap.relaysByInstance`）合法且不被扫描：`*Bootstrap` 不实现 ManagedResource。
- 新增 synthetic-type 单测 `TestFieldHoldsRelay_DetectsCollections` 证明 collection 递归非空挂。

### 威胁矩阵（重评，supersede 上文相关行）

| 威胁 | 本 amendment 后状态 |
|------|--------------------|
| 同一 instance key 二次 `WithRelay` | panic via panicregister.Approved（B 类）fail-fast（上文行的 keyed 化等价物） |
| 不同 instance key `WithRelay`（fan-out） | **合法累加**（上文「静默覆盖」行在 keyed 模型下被此取代：不同实例本就该各有 relay） |
| 替代 ManagedResource 类型持 `*Relay` 或 `[]*Relay`/`map[K]*Relay` relay 集合 | archtest `RELAY-SOLE-HOLDER-01` 红（强化后含 collection） |
| N relay 全局 probe 名冲突 | `expandManagedResources` fail-fast；非默认实例经 `RelayInstanceProbeName` 命名空间化避免 |
| 未来恢复 `Close` 方法绕过 type isolation | `RELAY-NOT-MANAGEDRESOURCE-01` 红（不变） |

### 影响面（增量）

| 文件 | 变更 |
|------|------|
| `runtime/bootstrap/infra_instance_key.go` | **新增** sealed `InfraInstanceKey` + 2 minter |
| `runtime/bootstrap/bootstrap.go` | `relay` 单字段 → `relaysByInstance` keyed map |
| `runtime/bootstrap/options_events.go` | `WithRelay(key, r)`；同-key panic；godoc |
| `runtime/bootstrap/relay_adapter.go` | `newRelayAdapter(key, r)`；非默认实例 probe 命名空间化 |
| `kernel/healthz/probename.go` | **新增** `RelayInstanceProbeName` composed-name 构造器 |
| `tools/archtest/relay_isolation_test.go` | `RELAY-SOLE-HOLDER-01` keyed 重写 + collection 强化 + synthetic 单测 |
| `tools/archtest/probename_sealed_funnel_test.go` | A2 内部 allowlist 加 relay_adapter.go |
| `cellmodules/configcore/storage.go`、`examples/{iotdevice,ssobff}` | `WithRelay(DefaultInstanceKey(), r)` |

## 参考

- 上游 backlog: `docs/backlog/20260520/202605191800-pr589-review-fixup-backlog.md`
  §`BOOTSTRAP-RELAY-DOUBLE-MANAGED-UPSTREAM-HARD-01`
- AI-robust 治理章程: `.claude/rules/gocell/ai-robust.md` §"Hard 范本目录"·single
  sanctioned holder + §"Funnel 双向锁评级"
- Panic taxonomy: `.claude/rules/gocell/error-handling.md` §"Panic"
  B 类（programmer-error parameter）
