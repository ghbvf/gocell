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

## 参考

- 上游 backlog: `docs/backlog/20260520/202605191800-pr589-review-fixup-backlog.md`
  §`BOOTSTRAP-RELAY-DOUBLE-MANAGED-UPSTREAM-HARD-01`
- AI-robust 治理章程: `.claude/rules/gocell/ai-robust.md` §"Hard 范本目录"·single
  sanctioned holder + §"Funnel 双向锁评级"
- Panic taxonomy: `.claude/rules/gocell/error-handling.md` §"Panic"
  B 类（programmer-error parameter）
