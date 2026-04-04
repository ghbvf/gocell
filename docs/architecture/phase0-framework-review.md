# Phase 0 对标 Review

> 对标框架：Uber fx（Cell 运行时）、Kubernetes（声明模型）、Kratos（错误模型）
> 审查范围：pkg/errcode, pkg/ctxkeys, kernel/cell, kernel/assembly, gocell.go

---

## 已对齐的设计决策

| # | 决策点 | 对标框架 | GoCell 现状 | 评估 |
|---|--------|---------|------------|------|
| 1 | FIFO Start / LIFO Stop | fx lifecycle.go | assembly.Start 按注册序，Stop 按反序 | 已对齐 |
| 2 | Stop 尽力而为 | fx app.go | Stop 遇错继续，返回第一个错误 | 已对齐 |
| 3 | Spec/Status 分离 | K8s types.go | cell.yaml = Spec, status-board = Status | 已对齐 |
| 4 | 错误链 Unwrap | Kratos errors.go | errcode.Error 实现 Unwrap()，支持 errors.Is/As | 已对齐 |
| 5 | 不可变错误 + 包装 | Kratos WithCause | WithDetails 返回新实例 | 已对齐 |
| 6 | struct 字段未导出 | fx 内部模式 | BaseCell/BaseSlice/BaseContract 全部小写字段 | 已对齐 |
| 7 | 接口极简 | fx Lifecycle (1 method) | Contract 接口仅 5 方法，Slice 8 方法 | 已对齐 |

---

## 需要修复的问题

### P0-FIX-1: Start 失败未 rollback 已启动的 Cell

**对标**：fx app.go — Start 出错后立即调用 Stop 回滚已成功启动的 hooks。
**GoCell 现状**：assembly.Start() 某个 Cell Init/Start 失败后直接返回错误，不回滚已启动的 Cell。
**风险**：部分 Cell 已 Start（占用端口/连接），但 Assembly 未标记为 started，后续 Stop 不会被调用。
**修复**：Start 失败时，对已成功 Start 的 Cell 按反序调用 Stop。

### P0-FIX-2: 缺少 Lifecycle 状态机

**对标**：fx lifecycle.go — 有状态枚举（stopped → starting → started → stopping → stopped），防止重入。
**GoCell 现状**：仅用 `started bool`，无法区分 starting/stopping 过渡态。
**风险**：并发调用 Start/Stop 可能导致数据竞争。
**修复**：引入状态枚举 + 原子操作或互斥锁。

---

## Phase 1 采纳项（不阻塞 Phase 0）

| # | 决策点 | 对标框架 | 建议 |
|---|--------|---------|------|
| 3 | Start/Stop 超时 | fx 全局 15s 超时 | Phase 1 加 context timeout，偏离 fx 的全局超时，改为 per-Cell 超时 + 全局上限 |
| 4 | 函数式选项模式 | fx New(...Option) | Phase 1 为 Assembly/Cell 构造加 WithXxx Option |
| 5 | Error Code+Reason 双字段 | Kratos Code+Reason | Phase 1 errcode 加 Reason 字段，便于 Is() 匹配 |

---

## 后续 Phase 采纳项（记录不做）

| # | 决策点 | 对标框架 | 何时做 |
|---|--------|---------|--------|
| 6 | 统一元数据信封（TypeMeta/ObjectMeta） | K8s types.go | Phase 1 metadata parser 时考虑 |
| 7 | Probe = Action + Timing 分离 | K8s Container.Probe | Phase 2 verify 框架时 |
| 8 | Handler 注册表模式 | K8s lifecycle/interfaces.go | Phase 2 Cell 运行时扩展时 |
| 9 | gRPC 状态映射 | Kratos GRPCStatus() | Phase 3 gRPC adapter 时 |
| 10 | Module = DI Scope 隔离 | fx module.go | Phase 2 Cell 间依赖注入时 |

---

## 修复记录

### P0-FIX-1 修复

```
文件：src/kernel/assembly/assembly.go
变更：Start() 失败时反序 Stop 已启动的 Cell
ref: uber-go/fx app.go — Start 出错自动 rollback
```

### P0-FIX-2 修复

```
文件：src/kernel/assembly/assembly.go
变更：引入 state 枚举 + sync.Mutex，防止重入
ref: uber-go/fx lifecycle.go — stopped/starting/started/stopping 状态机
```
