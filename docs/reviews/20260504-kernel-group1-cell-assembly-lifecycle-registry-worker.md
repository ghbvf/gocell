# Kernel 层第一组审查报告

**审查日期**：2026-05-04  
**审查范围**：kernel 层第一组 — Cell/Slice 运行时基础  
**模块**：`kernel/assembly`、`kernel/cell`、`kernel/lifecycle`、`kernel/registry`、`kernel/worker`

---

## Preflight

- repo: ghbvf/gocell
- reviewTargetType: manual-diff (kernel 层代码审查，分组1)
- pr: N/A
- base...head: 当前 HEAD (kernel/assembly, kernel/cell, kernel/lifecycle, kernel/registry, kernel/worker)
- changedFiles: ~50 文件（含测试）
- evidenceSource: local-workspace-code-read
- consistencyCheck: PASS（直接读取本地源码）

---

## 1. 审查范围与总体风险

### 范围

| 模块 | 主要责任 | 关键文件 |
|------|---------|---------|
| `kernel/assembly` | Cell 生命周期编排（FIFO Start / LIFO Stop / rollback） | assembly.go, hook_dispatcher.go, canonical.go |
| `kernel/cell` | Cell/Slice 核心抽象（接口、BaseCell、一致性等级、注册面） | interfaces.go, base.go, registry.go, auth_plan.go, mode_resolver.go |
| `kernel/lifecycle` | 资源关闭接口（ContextCloser） | closer.go, managed_resource.go |
| `kernel/registry` | YAML 元数据索引（CellRegistry、ContractRegistry） | cell.go, contract.go |
| `kernel/worker` | 后台任务接口（Worker） | worker.go |

### 总体风险评估

**中-高风险**：整体架构分层清晰（kernel 无向上依赖），FIFO/LIFO 生命周期设计完备，hookDispatcher 异步观察者设计优秀。但存在：
- **1 个 P0 级数据竞争**（可触发生产 fatal）
- **1 个 P1 级 goroutine 泄漏**（上限无界）
- **2 个 P1 级运维配置陷阱**（SIGTERM 期间 rollback context 失效，shutdownTimeout 预算对齐）
- **多处 P2 代码质量和架构边界问题**

**不存在阻塞合并的 API 不兼容变更**，但 P0 数据竞争问题应在下一个 Sprint 内修复。

---

## 2. 合并问题表

| ID | 严重级别 | 席位 | 文件:行号 | 问题 | 根因 | 修复方向 |
|----|---------|------|---------|------|------|---------|
| G1-01 | **P0** | 安全+架构 | [assembly.go](../../kernel/assembly/assembly.go) `startInternal Phase1` | `a.snapshots[c.ID()]` 在 Init 循环中无锁写入，与 `Snapshots()` 的持锁读形成 fatal map race | Init 阶段 `a.mu` 已释放，裸写 `a.snapshots` | 使用局部 map 收集，Init 全部完成后一次性持锁赋值 |
| G1-02 | **P1** | 架构+安全 | [hook_dispatcher.go](../../kernel/assembly/hook_dispatcher.go) `dispatchOne` | `dispatchOne` 超时后遗弃 goroutine，`d.wg` 不追踪 `dispatchOne` 内部子 goroutine，`stop()` 返回后孤儿 goroutine 可能永久存活 | 设计只让 `d.wg` 保护 worker loop，不保护 per-event goroutine | 增加 `d.sinkWg` 追踪每次 `dispatchOne` 子 goroutine；`stop()` 在 drain 后调用 `sinkWg.Wait()` 兜底 |
| G1-03 | **P1** | 运维 | [assembly.go](../../kernel/assembly/assembly.go) `startCellWithHooks rollback` | 启动期 SIGTERM → start ctx 被取消 → `rollbackCells(ctx, i-1)` 使用已 cancelled ctx，BeforeStop/Stop/AfterStop 拿到立即 done 的 context，资源可能未释放 | rollback 路径与 bootstrap 外层 rollback 不一致（外层用 `context.Background()` + shutdownTimeout） | rollback 改为 `context.WithTimeout(context.Background(), cfg.HookTimeout)`，与 bootstrap 外层保持一致 |
| G1-04 | **P1** | 运维 | [assembly.go](../../kernel/assembly/assembly.go) `DefaultHookTimeout` + runtime/bootstrap | `shutdownTimeout=30s`，`terminationGracePeriodSeconds` 默认也 30s，无安全余量；preShutdownDelay+HTTP drain+N×cell LIFO stop 共享同一 shutCtx | 两个独立 30s 计时器未对齐，无文档警告 | 文档要求 `terminationGracePeriodSeconds >= shutdownTimeout + preShutdownDelay + 10s`；或 `phase0ValidateOptions` 中 warn |
| G1-05 | **P2** | 架构 | [kernel/cell/](../../kernel/cell/) 多文件 | `kernel/cell` 包承载 Cell/Slice 核心接口 + AuthPlan(JWT/MTLS) + Outbox EmitterFactory + Health alias，god-package 趋势 | 历次迭代"就近放置"导致职责漂移 | `auth_plan.go` → `kernel/auth/`；`mode_resolver.go` → `kernel/outbox/`；`health.go` 单行 alias 删除，调用方引用 `outbox.ErrDegraded` |
| G1-06 | **P2** | 架构+可维护性 | [kernel/cell/interfaces.go](../../kernel/cell/interfaces.go) L64-L94 | `Cell` 接口含 11 个方法，将生命周期管理（Init/Start/Stop）与元数据自省（OwnedSlices/ConsumedContracts/...）耦合 | 接口设计时未按调用方视角拆分 | 拆分为 `CellLifecycle`（生命周期5方法）和 `CellDescriptor`（元数据7方法）；`Cell` 嵌入两者保持兼容 |
| G1-07 | **P2** | 安全 | [assembly.go](../../kernel/assembly/assembly.go) `Snapshots()` | `RegistrySnapshot` 浅拷贝，含 slice 字段的 value 共享底层数组；调用方修改 slice 静默污染 assembly 路由注册元数据 | `Snapshots()` 只 copy 外层 map，不 deep copy value | 提供 `RegistrySnapshot.DeepCopy()` 并在 `Snapshots()` 中调用 |
| G1-08 | **P2** | 安全 | [hook_dispatcher.go](../../kernel/assembly/hook_dispatcher.go) | `slog.Any("panic", r)` 将 recovered panic value 原样写入日志，若 observer panic 时携带敏感信息（token、连接串）会流入日志聚合系统 | panic value 无类型约束 | 改为 `fmt.Sprintf("%v", r)` 并截断（≤256 字节） |
| G1-09 | **P2** | 可维护性+产品 | [kernel/cell/base.go](../../kernel/cell/base.go) L96 + [assembly.go](../../kernel/assembly/assembly.go) L194 | 错误信息输出 `"current state: %d"` 暴露整数，对开发者无意义（`current state: 2` 不知道是哪个状态） | `cellState`/`assemblyState` 是 iota int，无 `String()` 方法 | 为 `cellState` 和 `assemblyState` 各添加 `String()` 方法 |
| G1-10 | **P2** | 产品+架构 | [kernel/cell/registry.go](../../kernel/cell/registry.go) vs [kernel/registry/](../../kernel/registry/) | 三个 "registry" 概念共存造成命名混乱：`cell.Registry`（Init builder）、`cell.RegistryRecorder`、`kernel/registry.CellRegistry`（只读索引）；新开发者极易混淆 | 命名选择时未区分"注册写操作"与"元数据查询" | `cell.Registry` → `cell.Registrar`；`kernel/registry.CellRegistry` → `kernel/registry.CellIndex`（见开源对标结论） |
| G1-11 | **P2** | 产品 | [kernel/cell/base.go](../../kernel/cell/base.go) `Health()` | `BaseCell.Health()` 永远返回 `{Status: "healthy", Details: nil}`，外部依赖断开时健康端点仍报 healthy | Health 状态只由内部状态机驱动，无挂接外部探针钩子 | 提供 `WithHealthFunc(fn func() HealthStatus)` 选项，或引入 probe 注册机制（见开源对标） |
| G1-12 | **P2** | 产品 | [kernel/cell/registry.go](../../kernel/cell/registry.go) L74 | `Registry.Health()` 重复注册时只 `slog.Error` + 静默丢弃（first-wins），不返回错误，开发者不知道 probe 被丢弃 | `Health` 方法签名为 void | 改为返回 `error`；或在 `Snapshot()` 时将重复注册错误汇总 |
| G1-13 | **P2** | 运维 | [hook_dispatcher.go](../../kernel/assembly/hook_dispatcher.go) emit `queue_full` | `queue_full` drop 只递增 metric counter，无 slog 日志回退；无 real MetricsProvider 时 drop 事件完全沉默 | 两种可见性路径（metrics + slog）只实现了一种 | `queue_full` 分支增加 `slog.Warn` 回退 |
| G1-14 | **P2** | 运维 | assembly.Config | `Config` 暴露 4 个内部调度器旋钮（`HookObserverQueueSize` 等），与 `dispatcherConfig` 概念重复，混淆公共 API | 调度器实现细节泄漏进公开 Config | 提取为 `HookObserverConfig` 子结构体 `Config.Observer HookObserverConfig` |
| G1-15 | **P2** | 安全 | [canonical.go](../../kernel/assembly/canonical.go) L44 | `encodeValue` 基于 reflect 递归遍历无深度上限，循环引用对象会 stack overflow | 无 `depth` 计数器 | 添加 `maxDepth` 参数（建议 ≤32），超出返回 error |
| G1-16 | **P3** | 测试 | [kernel/assembly/timeout_test.go](../../kernel/assembly/timeout_test.go) | `AfterStop` 超时路径无专项测试；`OutcomeTimeout` 事件计数和错误格式未锁定 | happy-path 偏好，仅测 BeforeStop 超时 | 新增 `TestHookTimeout_AfterStopExceeds` |
| G1-17 | **P3** | 测试 | [kernel/assembly/assembly_test.go](../../kernel/assembly/assembly_test.go) | 并发 Start/Stop 无 race detector 测试，`sync.Mutex` 状态机正确性未经 `-race` 验证 | 并发覆盖盲区 | 新增 `TestAssembly_ConcurrentStartStop_RaceDetector` |
| G1-18 | **P3** | 可维护性 | [kernel/cell/mode_resolver.go](../../kernel/cell/mode_resolver.go) | 文件名与内容不符：文件定义 `EmitterConfig/EmitterOutcome/ResolveEmitter`，无"mode resolver"概念 | 历史 rename 遗留 | 改名 `emitter_resolver.go` |
| G1-19 | **P3** | 产品 | [kernel/cell/types.go](../../kernel/cell/types.go) L21 | `Level` 一致性等级常量无"何时选择"引导注释，开发者需翻 CLAUDE.md 查表 | 注释写于早期 | 每个 Level 常量加 `// Use when: ...` 一行说明 |
| G1-20 | **P3** | 产品 | [kernel/worker/worker.go](../../kernel/worker/worker.go) | `Worker.Start` 是阻塞 Run 语义，`Cell.Start` 是非阻塞启动语义；同名方法语义对立，新开发者易混淆 | 命名沿用了 Cell 接口的 Start 名称 | Worker 接口考虑改名 `Run(ctx) error` + `Shutdown(ctx) error` 与 Cell 区分 |

---

## 3. 根因分析

### 根因簇 A：共享状态并发保护不完整（G1-01、G1-07、G1-17）

**症状**：  
- `a.snapshots` 在 Init 循环中无锁写入（G1-01）  
- `Snapshots()` 返回的 RegistrySnapshot 浅拷贝（G1-07）  
- 并发 Start/Stop 无 race 测试（G1-17）

**数据流**：
```
goroutine A: Start() → startInternal()
  mu.Lock(); state=stateStarting; mu.Unlock()
  for _, c := range cells {
    c.Init(ctx, recorder)
    a.snapshots[c.ID()] = recorder.Snapshot()  ← MAP WRITE, NO LOCK
  }

goroutine B: Snapshots()
  mu.Lock()
  for k, v := range a.snapshots { cp[k] = v }  ← MAP READ, WITH LOCK
  mu.Unlock()
```

**根因**：`startInternal` 的 stateStarting 阶段只在入口/出口持锁，Init 循环体本身未持锁写 `a.snapshots`。Go runtime 对并发 map 读写的检测是 fatal（不是 panic，不可 recover），会立即终止进程。

**函数调用链影响范围**：任何在 `Start()` 期间调用 `Snapshots()` 的监控组件（健康聚合器、指标采集器）都会触发 fatal。

**修复**：Init 阶段用局部 map，全部成功后在锁内批量赋值：
```go
localSnaps := make(map[string]cell.RegistrySnapshot, len(a.cells))
// ... Init 循环写 localSnaps ...
a.mu.Lock()
for k, v := range localSnaps { a.snapshots[k] = v }
a.mu.Unlock()
```

---

### 根因簇 B：异步观察者 goroutine 生命周期失控（G1-02、G1-08、G1-13）

**症状**：  
- `dispatchOne` 超时后孤儿 goroutine 无界泄漏（G1-02）  
- `slog.Any("panic", r)` 泄漏 observer panic value（G1-08）  
- `queue_full` drop 无日志回退（G1-13）

**调用链**：
```
worker loop (d.wg 跟踪):
  for item := range d.ch {
    dispatchOne(*item.evt)

dispatchOne:
  result := make(chan struct{}, 1)
  go func() {            ← 每事件一 goroutine，无 WaitGroup 注册
    d.observer.OnHookEvent(e)  ← 若 observer 阻塞，永不退出
    result <- struct{}{}
  }()
  select {
  case <-result:   // 成功
  case <-t.C():   // 超时 → abandon goroutine，d.wg 不感知
  }

stop():
  close(d.ch)
  <-d.done  // 仅等待 worker loop，不等孤儿 goroutine
```

**根因**：`hookDispatcher` 的设计目标是"不阻塞主路径"，但超时后的孤儿 goroutine 既无追踪也无上界。若 observer 实现（如向 SIEM 推 HTTP）在网络分区时阻塞，每次 Start/Stop 最多泄漏 `cells × 4 phases` 个 goroutine，累计可耗尽 goroutine 调度预算。

**修复方向**：增加 `d.sinkWg sync.WaitGroup`，每次 `go dispatchOne()` 内部 `d.sinkWg.Add(1)` + `defer d.sinkWg.Done()`，`stop()` drain 后调用 `sinkWg.Wait()`（可用剩余 drainTimeout 兜底）。

---

### 根因簇 C：Shutdown Context 生命周期设计不一致（G1-03、G1-04）

**症状**：  
- 启动期 SIGTERM → rollback 使用已 cancelled start ctx（G1-03）  
- shutdownTimeout 与 k8s terminationGracePeriodSeconds 无安全余量（G1-04）

**调用链**：
```
bootstrap.Run(ctx)                          ← ctx 被 signal 取消
  → phase3InitAssembly(ctx, s)
    → asm.Start(ctx)                       ← 同一个可取消 ctx
      → startCellWithHooks(ctx, c, i)
        → c.Start(ctx) 失败
          → rollbackCells(ctx, i-1)        ← ctx 可能已 cancelled!
            → stopCellWithHooks(ctx, c)    ← BeforeStop/Stop 拿到已 done ctx
```

而 bootstrap 外层 rollback 路径（phases_shutdown.go）已用独立 `context.Background() + shutdownTimeout`，两条路径不一致。

**架构原因**：`assembly.New(cfg)` 没有持有独立的 shutdown budget context，rollback 完全依赖调用方传入的 ctx，无法对 start/stop 两阶段分别定义生命周期。

---

### 根因簇 D：kernel/cell 职责漂移（G1-05、G1-06、G1-10、G1-18）

**症状**：  
- `kernel/cell` 包含 AuthPlan、EmitterFactory、Health alias（G1-05）  
- `Cell` 接口 11 方法混合生命周期与元数据自省（G1-06）  
- "registry" 三义混用（G1-10）  
- 文件名 mode_resolver.go 与内容不符（G1-18）

**架构原因**：`kernel/cell` 作为 GoCell 最早定义的核心包，成为了"附近添加"的汇聚点。随着每次功能扩展"就近放置"，已演化为事实上的 god-package：同一包内包含运行时基础设施（Cell/Slice/BaseCell）、认证计划枚举（AuthPlan）、Outbox EmitterFactory（mode_resolver.go）、健康状态别名（health.go）。

**影响**：任何 import `kernel/cell` 的包都间接依赖了 JWT/MTLS 枚举和 Outbox 接口，增加了不必要的耦合，也使 `kernel/cell` 的边界测试和 archtest 规则越来越难以精确描述。

---

### 根因簇 E：Health 信息空洞（G1-11、G1-12）

**症状**：  
- `BaseCell.Health()` 永远返回 `{Status: "healthy"}` 无 Details（G1-11）  
- `Health(probe)` 重复注册静默丢弃（G1-12）

**根因**：BaseCell 实现健康检查时只依赖内部状态机（`cellStateStarted`），未提供挂载外部探针（DB 连接池、MQ 连通性）的扩展点。`cell.Registry.Health()` 返回 void 设计，使注册错误无法向 `Init` 调用方传播。

---

## 4. 开源项目对比表

### 主题 1：FIFO/LIFO 生命周期管理与 rollback 模式

| 框架 | 检查来源 | 观察到的模式 | 与本项目的相关性 |
|------|---------|---------|---------|
| **uber-go/fx** | [lifecycle.go#L176](https://github.com/uber-go/fx/blob/master/internal/lifecycle/lifecycle.go#L176), [app.go#L664](https://github.com/uber-go/fx/blob/master/app.go#L664) | Start FIFO/Stop LIFO；用 `numStarted` 精确控制 rollback 范围；rollback 复用同一 start ctx（简单但 ctx 可能已 cancel）；`StopTimeout` 是整个 Stop 的总预算，无 per-hook timeout | 高度相关。GoCell 的 rollback 路径（G1-03）应参考 fx 的 `numStarted` 模式，但需比 fx 更严格地处理 ctx 取消问题（使用独立 background ctx） |
| **go-kratos/kratos** | [app.go#L101](https://github.com/go-kratos/kratos/blob/main/app.go#L101), BeforeStart/AfterStart hooks | 4 个生命周期钩子（BeforeStart/AfterStart/BeforeStop/AfterStop）；Stop 逆序停止 server；每个 server 独立 goroutine Start，ctx cancel 作为停止信号 | 相关。GoCell 的 4 钩子设计与 Kratos 对齐。Kratos 中 Stop 用的是独立 `context.Background()`，支持 G1-03 修复方向 |
| **k8s controller-runtime** | [manager/internal.go#L394](https://github.com/kubernetes-sigs/controller-runtime/blob/main/pkg/manager/internal.go#L394) | `GracefulShutdownTimeout` per-phase 配置；明确文档要求 `terminationGracePeriodSeconds` 远大于 `GracefulShutdownTimeout` | 直接支持 G1-04。controller-runtime 有 `phase0ValidateOptions` 模式且文档显式说明配置关系 |

**结论（≥3 项支撑）**：FIFO/LIFO + rollback 是业界共识，但 rollback 使用独立 context（而非继承 start ctx）是更安全的做法。fx 当前也复用 start ctx（已知权衡），GoCell 应选择更安全的独立 ctx 路径。

---

### 主题 2：健康检查注册与信息丰富度

| 框架 | 检查来源 | 观察到的模式 | 与本项目的相关性 |
|------|---------|---------|---------|
| **k8s controller-runtime** | [healthz.go#L184](https://github.com/kubernetes-sigs/controller-runtime/blob/v0.24.0/pkg/healthz/healthz.go#L184), [manager.go#L54](https://github.com/kubernetes-sigs/controller-runtime/blob/v0.24.0/pkg/manager/manager.go#L54) | 组件注册 probe 函数（Checker = `func(req *http.Request) error`），Manager 聚合；无 BaseController 默认健康实现，组件须主动注册 | 直接支持 G1-11 修复。注册 probe 模式比"覆盖 Health() 方法"更灵活，且不会出现"基类假健康"问题 |
| **uber-go/fx** | [lifecycle.go](https://github.com/uber-go/fx/blob/master/lifecycle.go) | fx 无内建健康抽象，生命周期与健康检查完全解耦；健康探针由应用自行实现 HTTP server + OnStart 注册 | 部分相关。fx 的解耦思路提示：health check 不应与 lifecycle 耦合在同一基类 |
| **go-kratos/kratos** | 服务注册接口 | kratos 的健康检查通过 gRPC health protocol 或 HTTP endpoint 实现；服务框架层提供标准化 probe，组件不需要实现基类方法 | 相关。kratos 同样不依赖基类默认实现，而是在框架层提供标准化集成点 |

**结论（≥3 项支撑）**：注册 probe 函数优于要求组件覆盖基类 Health() 方法。GoCell 应在 `cell.Registry` 中提供 `RegisterHealthProbe(name string, fn func(ctx) error)` 接口，让各 Cell 在 Init 阶段显式注册外部依赖的探针函数。BaseCell.Health() 可降级为"仅报告 lifecycle 状态"，并在 Details 中标注 `source=base-default` 提醒运维。

---

### 主题 3：Registry 命名约定（注册写接口 vs 元数据查询）

| 框架 | 检查来源 | 观察到的模式 | 与本项目的相关性 |
|------|---------|---------|---------|
| **go-kratos/kratos** | [registry/registry.go#L10](https://github.com/go-kratos/kratos/blob/main/registry/registry.go#L10) | 明确拆分：`Registrar`（Register/Deregister 写操作）和 `Discovery`（GetService/Watch 读操作）；从不共用 Registry 命名 | 直接支持 G1-10。Kratos 是最典型的"写用 Registrar，读用 Discovery/Index"命名实践 |
| **kubernetes/apimachinery** | [runtime/scheme.go#L46](https://github.com/kubernetes/apimachinery/blob/master/pkg/runtime/scheme.go#L46), controller-runtime [scheme.go#L84](https://github.com/kubernetes-sigs/controller-runtime/blob/main/pkg/scheme/scheme.go#L84) | `Builder`（注册意图）→ `AddToScheme`（写）→ `Scheme`（查询）；三层命名语义递进 | 相关。K8s 也不把写和读都叫 Registry，Builder 承载注册意图，Scheme 承载查询 |
| **zeromicro/go-zero** | [discov/publisher.go#L17](https://github.com/zeromicro/go-zero/blob/master/core/discov/publisher.go#L17), [subscriber.go#L16](https://github.com/zeromicro/go-zero/blob/master/core/discov/subscriber.go#L16) | 写操作用 `Publisher`，读操作用 `Subscriber`；内部用 Registry 管理连接，不暴露给外部 | 部分相关。动词化角色命名（Publisher/Subscriber）与 Kratos 的 Registrar/Discovery 一样，都避免了读写同名 |

**结论（≥3 项支撑）**：业界三个主流框架均将"注册写操作接口"与"元数据读查询结构"区分命名，GoCell 应将 `cell.Registry` 改名为 `cell.Registrar`，将 `kernel/registry.CellRegistry` 改名为 `kernel/registry.CellIndex`（或包名改为 `kernel/index`）。

---

## 5. 建议与修复优先级

### P0 必须立即修复

**G1-01（`a.snapshots` 数据竞争）**  
- 影响：生产任意时刻调用 `Snapshots()` 可触发 `fatal: concurrent map read and map write`，进程立即终止，不可 recover  
- 修复成本：低（使用局部 map + 一次性持锁赋值，约 5 行改动）  
- 目标 Sprint：当前

### P1 应在下一 Sprint 内修复

**G1-02（hookDispatcher 孤儿 goroutine）**  
- 影响：network partition 下 SIEM observer 阻塞，每次 Start/Stop 泄漏最多 `cells×4` 个 goroutine  
- 修复：增加 `d.sinkWg`，`stop()` 后 `sinkWg.Wait()`

**G1-03（rollback 使用已 cancelled ctx）**  
- 影响：SIGTERM 期间 rollback 路径资源无法正确释放  
- 修复：`rollbackCells(context.WithTimeout(context.Background(), cfg.HookTimeout), upTo)`

**G1-04（shutdownTimeout 预算对齐）**  
- 影响：k8s 默认配置下，多 cell 串行关闭可能被 SIGKILL 截断  
- 修复：文档 + `phase0ValidateOptions` warn

### P2 规划入 Backlog

| 问题 | 建议操作 |
|------|---------|
| G1-05（kernel/cell god-package） | 逐步迁移：auth_plan → kernel/auth，mode_resolver → kernel/outbox |
| G1-06（Cell 接口过大） | 拆分 CellLifecycle + CellDescriptor，Cell 嵌入两者 |
| G1-07（RegistrySnapshot 浅拷贝） | 实现 DeepCopy()，Snapshots() 调用 |
| G1-08（panic value 日志泄漏） | fmt.Sprintf("%v", r) 截断 256 字节 |
| G1-09（状态整数错误信息） | 为 cellState/assemblyState 实现 String()，优先级低成本小 |
| G1-10（registry 命名混乱） | cell.Registry → cell.Registrar（需全项目搜索替换） |
| G1-11（BaseCell Health 空洞） | 引入 RegisterHealthProbe 注册机制 |
| G1-12（Health 重复注册静默丢弃） | 返回 error 或 Snapshot 时汇总 |
| G1-13（queue_full 无日志） | 增加 slog.Warn 回退 |
| G1-14（Config 调度器旋钮泄漏） | 提取 HookObserverConfig 子结构体 |
| G1-15（canonical.go 无深度限制） | 添加 maxDepth 计数器 |

### P3 改进项（可在重构时处理）

- G1-16：补充 AfterStop 超时测试
- G1-17：补充并发 Start/Stop race 测试
- G1-18：mode_resolver.go 改名 emitter_resolver.go
- G1-19：Level 常量加 Use when 注释
- G1-20：Worker 接口 Start/Shutdown 改名为 Run/Shutdown

---

## 6. 亮点

| 设计亮点 | 位置 | 说明 |
|---------|------|------|
| FIFO Start / LIFO Stop + AfterStart 失败停止当前 Cell 后 rollback | [assembly.go `startCellWithHooks`](../../kernel/assembly/assembly.go) | rollback 边界情况处理完整，与 uber-go/fx 语义对齐 |
| `MustHaveClock` typed-nil 检测（反射实现） | [assembly.go New](../../kernel/assembly/assembly.go) | fail-fast 比运行时 NPE 早暴露接线错误 |
| hookDispatcher non-blocking emit + sync fence | [hook_dispatcher.go](../../kernel/assembly/hook_dispatcher.go) | 主路径零阻塞；`flush()` 为测试提供确定性屏障 |
| cfgMap 深拷贝隔离 | [assembly.go `cloneConfigMap`](../../kernel/assembly/assembly.go) | 防止 Init 路径跨 Cell 污染 config |
| `AssemblyRef` 接口打破循环依赖 | [kernel/cell/auth_plan.go](../../kernel/cell/auth_plan.go) | 接口放置位置的正确示范 |
| `AuthPlan` 密封接口（unexported `authPlanKind()`） | [kernel/cell/auth_plan.go](../../kernel/cell/auth_plan.go) | 外部包无法伪造 auth 计划 |
| `TestMain` + goleak 守护 goroutine 泄漏 | [kernel/assembly/assembly_test.go](../../kernel/assembly/assembly_test.go) | 测试防线对标 uber-go/fx 级别 |
| `ListenerRef` 封闭集合（unexported 字段） | [kernel/cell/listener.go](../../kernel/cell/listener.go) | 完全消除 listener-name typo 类 bug |
| `kernel/registry` 深拷贝 slice 字段 | [kernel/registry/cell.go](../../kernel/registry/cell.go) | 只读索引返回副本，调用方修改不污染注册表 |

---

*报告生成时间：2026-05-04*  
*审查使用六席位 + 3 项开源对标（uber-go/fx, kubernetes-sigs/controller-runtime, go-kratos/kratos）*
