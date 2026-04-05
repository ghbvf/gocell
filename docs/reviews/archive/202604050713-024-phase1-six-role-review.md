# 阶段 1 六角色基线审查报告

**审查日期**: 2026-04-05
**审查基线**: develop 分支 commit 2014298
**范围**: `pkg/ctxkeys`、`pkg/errcode`、`kernel/cell`、`kernel/idempotency`、`kernel/outbox`、`kernel/assembly`、`gocell.go`

## Executive Summary

- 总 finding 数: 49（P0: 0, P1: 18, P2: 31）
- 合流阻塞项: 0（无 P0）
- Signoff: **带条件通过** — 6 个 P1 并发安全问题建议在 Phase 2 业务 Cell 开发前修复

## 跨角色共识（3+ 角色独立发现）

1. **Register() 无 mutex** — 架构师 + 魔鬼 + 工具（P1）
2. **BaseCell 并发不安全** — 架构师 + 魔鬼（P1）
3. **Health() 无锁保护** — 架构师 + 魔鬼（P1）
4. **返回可变切片/map** — 架构师 + 魔鬼（P1）
5. **Assembly 错误处理混用 fmt.Errorf** — 架构师 + 工具 + DX（P1/P2）
6. **gocell.go 暴露内部类型且无验证** — 架构师 + DX + PM（P1/P2）

---

## 架构师 Findings

### F-1A-01: Cell 接口粒度过粗 — 生命周期与健康检查耦合
- **严重度**: P1
- **分类**: DESIGN
- **文件**: `kernel/cell/interfaces.go:62-76`
- **描述**: Cell 接口包含 12 个方法，混合标识/生命周期/健康三个关注点。按 ISP 应拆为 Identifiable + Lifecycle + HealthChecker。
- **建议修复**: 拆为组合接口 `Cell = Identifiable + Lifecycle + HealthChecker + SliceOwner`

### F-1A-02: gocell.go 暴露内部实现类型 CoreAssembly
- **严重度**: P1
- **分类**: DESIGN
- **文件**: `gocell.go:7`
- **描述**: `NewAssembly` 返回 `*assembly.CoreAssembly` 而非 `cell.Assembly` 接口，泄漏内部实现。
- **建议修复**: 返回值类型改为 `cell.Assembly`

### F-1A-03: CoreAssembly.Health() 无互斥保护
- **严重度**: P1
- **分类**: BUG
- **文件**: `kernel/assembly/assembly.go:149-155`
- **描述**: Health() 未持锁遍历 `a.cells`，并发场景可能 panic 或读到不一致状态。

### F-1A-04: BaseCell 无并发安全保护
- **严重度**: P1
- **分类**: BUG
- **文件**: `kernel/cell/base.go:14-77`
- **描述**: `started`/`healthy` 字段及切片无锁保护，concurrent Init/Start/Stop/Health 引发数据竞争。

### F-1A-05: Assembly 状态机竞态 — Stop 与 Start 并发
- **严重度**: P2
- **分类**: BUG
- **文件**: `kernel/assembly/assembly.go:124-146`
- **描述**: Stop() 只防 stateStopping 重入，无法防 Stop 与 Start 并发执行。

### F-1A-08: 错误处理不一致 — Assembly 混用 errcode 和 fmt.Errorf
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `kernel/assembly/assembly.go:56-137`
- **描述**: Register 用 `errcode.New()`，Start/Stop 用 `fmt.Errorf()`，上游无法统一 `errors.As`。

### F-1A-09: Dependencies 结构体使用可变 map
- **严重度**: P1
- **分类**: BUG
- **文件**: `kernel/cell/interfaces.go:5-10`
- **描述**: `Dependencies.Cells` 直接赋值 `a.cellMap`，Cell 可修改影响其他 Cell 初始化。

### F-1A-10: Slice 接口 Verify()/AllowedFiles() 无错误返回
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `kernel/cell/interfaces.go:78-87`

### F-1A-13: HealthStatus.Details map 无 schema
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `kernel/cell/types.go:62-65`

### F-1A-14: ParseLevel 错误时返回 0 (L0) — 默认值歧义
- **严重度**: P2
- **分类**: NIT
- **文件**: `kernel/cell/types.go:43-58`

### F-1A-15: Assembly.Register 无线程安全保护
- **严重度**: P1
- **分类**: BUG
- **文件**: `kernel/assembly/assembly.go:56-68`

### F-1A-17: Assembly Start 失败后 Init 过的 Cell 无回滚
- **严重度**: P2
- **分类**: BUG
- **文件**: `kernel/assembly/assembly.go:74-118`

### F-1A-19: errcode.WithDetails 浅拷贝
- **严重度**: P2
- **分类**: BUG
- **文件**: `pkg/errcode/errcode.go:65-82`

### F-1A-21: gocell.go API 太薄 — 无配置项
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `gocell.go`

### F-1A-22: ctxkeys 允许空值
- **严重度**: P2
- **分类**: NIT
- **文件**: `pkg/ctxkeys/keys.go:24-97`

### F-1A-24: BaseSlice.AllowedFiles() 默认路径无 ID 格式校验
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `kernel/cell/base.go:113-118`

---

## 领域专家 Findings

### F-1D-06: 一致性等级命名缺乏文档 — L3/L4 语义模糊
- **严重度**: P2
- **分类**: NIT
- **文件**: `kernel/cell/types.go:19-28`

### F-1D-07: Contract 无生命周期修改接口
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `kernel/cell/base.go:136-161`

### F-1D-11: CellType 枚举缺乏架构文档映射
- **严重度**: P2
- **分类**: NIT
- **文件**: `kernel/cell/types.go:10-17`

### F-1D-12: Owner 结构不完整 — 缺 Contact 信息
- **严重度**: P2
- **分类**: NIT
- **文件**: `kernel/cell/interfaces.go:38-42`

### F-1D-16: Waiver.ExpiresAt 为字符串 — 无类型检查
- **严重度**: P2
- **分类**: NIT
- **文件**: `kernel/cell/interfaces.go:20-25`

### F-1D-18: ContractRole RoleInvoke 语义冲突
- **严重度**: P1
- **分类**: DESIGN
- **文件**: `kernel/cell/types.go:77-89`

### F-1D-20: L0Dep 只声明 L0 依赖 — 模型不完整
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `kernel/cell/interfaces.go:54-58`

### F-1D-23: Assembly 接口缺少 ID() 方法
- **严重度**: P2
- **分类**: NIT
- **文件**: `kernel/cell/interfaces.go:99-104`

---

## 工具工程师 Findings

### F-1T-01: WithDetails 对 nil Details 的行为与注释不符
- **严重度**: P1
- **分类**: BUG
- **文件**: `pkg/errcode/errcode.go:68-82`

### F-1T-03: HealthStatus.Status 缺少类型化枚举
- **严重度**: P1
- **分类**: DESIGN
- **文件**: `kernel/cell/types.go:61-65`

### F-1T-04: BaseCell.Init 不保证幂等性
- **严重度**: P1
- **分类**: BUG
- **文件**: `kernel/cell/base.go:37-40`

### F-1T-05: Assembly Init 失败错误消息用 fmt.Errorf
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `kernel/assembly/assembly.go:90-98`

### F-1T-06: Assembly 生命周期错误码全用 fmt.Errorf
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `kernel/assembly/assembly.go:78-137`

### F-1T-08: Outbox Entry 缺少 Metadata/Headers 字段
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `kernel/outbox/outbox.go:12-20`

### F-1T-09: Idempotency 接口缺少默认 TTL
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `kernel/idempotency/idempotency.go:10-18`

### F-1T-10: 缺少 Assembly 异常序列测试
- **严重度**: P1
- **分类**: DESIGN
- **文件**: `kernel/assembly/assembly_test.go`
- **描述**: 缺少 Start 后 Init、Stop 后 Start、Stop 幂等性、并发 Start/Stop 等测试。

### F-1T-12: ContractRole 与 ContractKind 不匹配无验证函数
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `kernel/cell/consistency.go:9-22`

### F-1T-14: Generator.executeTemplate 错误嵌套多层
- **严重度**: P1
- **分类**: DESIGN
- **文件**: `kernel/assembly/generator.go:209-229`

---

## DX Findings

### F-1X-02: ParseLevel 大小写敏感性无文档
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `kernel/cell/types.go:41-59`

### F-1X-07: ctxkeys 模块死代码 — 全仓无消费者
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `pkg/ctxkeys/keys.go`

### F-1X-11: gocell.NewAssembly 无错误返回且无参数验证
- **严重度**: P1
- **分类**: DESIGN
- **文件**: `gocell.go:6-9`

### F-1X-13: CellMetadata 缺少构造函数与验证
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `kernel/cell/interfaces.go:27-36`

### F-1X-15: 集成测试不完整 — 缺多 Cell 编排场景
- **严重度**: P1
- **分类**: DESIGN
- **文件**: `gocell_test.go:12-24`

---

## 魔鬼代言人 Findings

### F-1S-01: Register 缺少并发安全保护
- **严重度**: P1
- **分类**: BUG | SECURITY
- **文件**: `kernel/assembly/assembly.go:56-68`
- **描述**: cellMap 和 cells 读写无锁，并发 Register 导致 map panic 或 TOCTOU。

### F-1S-02: BaseCell 状态字段无 mutex — race detector 会检出
- **严重度**: P1
- **分类**: BUG
- **文件**: `kernel/cell/base.go:14-20`

### F-1S-03: OwnedSlices()/ProducedContracts()/ConsumedContracts() 返回可变切片
- **严重度**: P1
- **分类**: DESIGN | SECURITY
- **文件**: `kernel/cell/base.go:32-34`
- **描述**: 直接返回内部切片引用，外部可 append/修改破坏 Cell 数据完整性。

### F-1S-04: Dependencies.Cells 直接暴露可修改的 map
- **严重度**: P1
- **分类**: DESIGN | SECURITY
- **文件**: `kernel/assembly/assembly.go:84-88`

### F-1S-05: Register() 在 Start 期间仍可调用
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `kernel/assembly/assembly.go:54-68`

### F-1S-06: Stop() 在 stateStarting 下行为未定义
- **严重度**: P1
- **分类**: DESIGN
- **文件**: `kernel/assembly/assembly.go:124-146`

### F-1S-07: Health() 无 mutex — 与 Register/Stop 竞态
- **严重度**: P1
- **分类**: BUG
- **文件**: `kernel/assembly/assembly.go:148-155`

---

## PM Findings

### F-1P-01: NewAssembly 不验证 ID
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `gocell.go:6-9`

### F-1P-02: Init 失败无完整回滚
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `kernel/assembly/assembly.go:90-98`

### F-1P-03: Health map 返回值可被外部修改
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `kernel/assembly/assembly.go:148-155`

### F-1P-04: Start/Stop 不检查 context 取消
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `kernel/assembly/assembly.go:74-146`

### F-1P-05: Dependencies.Contracts 永远为空 map
- **严重度**: P2
- **分类**: DESIGN
- **文件**: `kernel/assembly/assembly.go:84-88`

### F-1P-06: 生命周期顺序约束缺文档
- **严重度**: P2
- **分类**: NIT
- **文件**: `kernel/assembly/assembly.go`

### F-1P-07: 缺乏并发场景测试
- **严重度**: P2
- **分类**: NIT
- **文件**: `kernel/assembly/assembly_test.go`
