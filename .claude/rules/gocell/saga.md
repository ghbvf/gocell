# Saga 引擎强约束索引

> 本文件是 **导航索引**，不是权威真值源。每条规则的完整盲区清单、符号清单、AI-robust 举证材料活在对应的 archtest 函数 godoc（`tools/archtest/saga_invariants_test.go`）和 governance 规则实现（`kernel/governance/rules_saga.go`）中。本文件只汇总 ID / 一行摘要 / 评级，供 reviewer 快速导航。

---

## Archtest Invariants（`tools/archtest/saga_invariants_test.go`）

所有 saga 主题 archtest 强制合并至单一文件，由 `SAGA-INVARIANTS-FILE-CONSOLIDATED-01` 机器守卫（任何在该文件外声明 `// INVARIANT: SAGA-*` 的文件都会导致 CI 红）。

| ID | 摘要 | 评级 |
|----|------|------|
| SAGA-STEP-COMPENSATE-PURE-01 | `CompensateFunc` 赋值槽（包括 `saga.Step{Compensate: ...}` composite literal）的函数体禁止调用 `outbox.Writer`、`outbox.Emitter`、`persistence.TxRunner`、`*sql.Tx`、`pgx.Tx` 等持久化接口；Compensate 必须是纯反向回滚，事务层由 Coordinator 持有 | Hard（类型识别 CompensateFunc 槽 + types.Implements 识别禁用 receiver；import alias 无效） |
| SAGA-COORDINATOR-NO-HEARTBEAT-LOOP-01 | `runtime/saga` 包（不含 `runtime/saga/executor`）禁止直接调用 `journal.Journal.Heartbeat`；心跳由 executor 子包的 per-step goroutine 独立维护，Coordinator 不持有集中式心跳循环 | Hard 上游（`Coordinator.journal` 收窄为 `journal.JournalCore`，无 Heartbeat 方法，compile 不可表达集中式循环）+ Medium 下游（archtest 禁 Heartbeat callsite 残留；func-value 字段路径由 SAGA-JOURNAL-HOLDER-SEAL-01 规则 3 Medium 守） |
| SAGA-EXECUTOR-RAND-INJECTED-01 | `runtime/saga/executor/` 生产文件禁止调用 `math/rand`/`math/rand/v2` 的包级全局函数（`rand.Int64N` 等）；只允许构造器（`rand.New`、`rand.NewPCG`、`rand.NewChaCha8`、`rand.NewSource`），确保重试 jitter 源可测试注入 | Hard 下游（TypesInfo.ObjectOf 精确解析 pkg path + nil receiver；import alias 无效）+ Medium 上游（executor 包边界，跨包传递链不追踪） |
| SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01 | 每个满足 `kernel/saga/journal.Journal` 接口的生产具名类型，其包下的 `_test.go` 文件必须有 `sagajournaltest.RunConformanceSuite` 调用；新增 Journal 实现必须接入一致性测试套件 | Medium（`types.Implements` 类型感知扫描 + `_test.go` 调用点解析；Hard 路径 = codegen golden 枚举实现，追踪于 gh #1003） |
| SAGA-JOURNAL-HOLDER-SEAL-01 | `runtime/saga` 生产 struct 禁止持有（作为字段）：(1) `journal.Journal` 或 `journal.Heartbeater`；(2) `journal.JournalCore` 仅允许 `runtime/saga.Coordinator` 持有；(3) 心跳形态的 func 字段（`func(context.Context, idutil.SafeID, idutil.SafeID, time.Duration) (bool, error)`）| Medium（go/types 字段类型解析；Hard 路径 = sealed construction 使非 Coordinator 持有 JournalCore 编译不可表达，追踪于 gh #982） |
| SAGA-DRIVE-BEHIND-LEADER-GATE-01 | `runtime/saga` 中 `driveOne` 调用必须只存在于 `tickOnce` 函数体内（A1）；`tickOnce` 必须调用 `acquireLead`（A2）；`acquireLead` 返回的 `lead` 必须以 if 语句守卫 `driveOne` 调用（A3）；三层共同确保无 leader 不驱动 | Medium（纯 AST selector 名匹配 + 控制依赖门控；Hard 路径 = typed gate token 穿入 driveOne 签名，追踪于 gh #1110） |
| SAGA-STATUS-FANOUT-COVERAGE-01 | 新增 `saga.Status` 常量或 `journal.EventKind` 常量必须同步覆盖四处扇出载体：conformance 终态覆盖（keyless struct literal 编译门控）、readyz 状态表文档、alerting kind 说明文档、`TerminalEventKind` 映射；由 `sagacoveragegen` 单源生成骨架并 golden-lock | Hard（codegen funnel：生成 struct 字段 + keyless literal 编译穷举；golden-diff byte-lock 上游；compile error 下游） |
| SAGA-STEP-RUN-OUTSIDE-TX-01 | `runtime/saga/` 生产文件中 `kernel/saga.StepFunc` 调用必须只出现在 `safeRun` 函数体内（A1）；`TxRunner.RunInTx` 的 closure body 内禁止调用 `safeRun`（A2）；合称确保步骤不在持有数据库事务时执行 | A1 Hard（TypesInfo.ObjectOf typed callsite-uniqueness + posInRanges body gate）+ A2 Medium（AST EachInSubtree closure scan；helper 包间传递链 B2 参见 gh #980） |
| SAGA-INVARIANTS-FILE-CONSOLIDATED-01 | 所有 `tools/archtest/*_test.go` 中声明 `// INVARIANT: SAGA-*` 的文件必须是 `saga_invariants_test.go` 本身；禁止 saga 主题规则散落在独立文件中（防止 PR #1213 清理后再度碎片化） | Medium（内容扫描 + 跨文件不变式；Hard 不可达——Go 编译不受文件分布约束） |
| SAGA-CONSTRUCTOR-NIL-GUARD-01 | `runtime/saga` 与 `runtime/saga/executor` 下的顶层 `New*` 构造器，其非 variadic 的 interface 位置参数（`journal.Journal`、`persistence.TxRunner`、`outbox.Emitter`、`saga.Resolver`、`executor.Heartbeater`）须在函数体内经 `validation.IsNilInterface` 显式守卫，`clock.Clock` 经 `clock.MustHaveClock` 守 | Medium（**非 funnel 类约束，单轴评级**——「必填 interface 参数缺守卫」无 caller-allowlist 下游 / sealed-interface 上游可分，不套用双向锁格式。archtest type-aware：绑定参数 types.Object 身份 + callee 解析；缺守卫在 Go 可表达故非类型系统封闭。Hard 路径 = 接入 `gocell:"required"` tag funnel + `gocell generate required-deps` 生成 `validateRequired()`，追踪 gh #1317） |
| SAGA-SLOG-INSTANCE-FIELDS-CALLER-01 | "每条 per-instance saga 日志携带 lease_id"（#1266）的载体 funnel：所有 per-instance 标识属性经 `runtime/saga/internal/sagalog.InstanceFields(instanceID, leaseID, extra...)` 单源构造（`lease_id`/`instance_id` 为必填位置参，漏 lease_id = 编译错误）；archtest 禁止 `runtime/saga/` 生产代码在 `InstanceFields` 体外裸写 `slog.String("instance_id"\|"lease_id", …)`，强制所有日志站点过 funnel + 自动覆盖未来站点。取代 #1263 起逐站点 lease_id 测试断言（Soft）——typed funnel ≠ log-string-anchor archtest | 下游 Hard（lease_id 必填位置参，type system 守）+ 上游 Medium（archtest caller-allowlist；Go 无法强制所有 LogAttrs 过 funnel，与 SPAN-SETATTR-HOLDER-SEAL #851 / HEALTHZ-HOLDER-SEAL #893 / outbox principal-write #1282 同永久天花板，won't-do（追踪 gh #1452））|

---

## Governance 规则（`kernel/governance/rules_saga.go`）

以下规则在 `gocell validate` 运行时对 `kind: saga` 的 `contract.yaml` 强制执行（`PhaseBase`，CI fail-closed）。

| Code | 摘要 | 评级 |
|------|------|------|
| SAGA-CONTRACT-BLOCK-PRESENT-01 | `kind: saga` 的 contract 必须有非空 `saga:` 块；缺少 saga block 的 saga contract 无法派生任何步骤定义，contractgen 无法生成类型化胶水代码 | Medium（governance rule；Hard 主门控 = contractgen builder delegation。注：gh #960 已评估 parser load-time jsonschema.Validate 并**否决**——parse-time 校验是 Medium runtime guard，真 Hard 是 codegen funnel） |
| SAGA-CONTRACT-STEPS-NONEMPTY-01 | `saga.steps[]` 必须至少包含一个步骤；空步骤列表的 saga 会静默成功但不执行任何前向工作 | Medium（同上） |
| SAGA-CONTRACT-STEP-NAME-VALID-01 | 每个步骤名必须匹配 `^[a-zA-Z][a-zA-Z0-9]*$`（`SagaStepNamePattern`）；contractgen 将步骤名转换为 Go 标识符（`Run<Name>` / `Compensate<Name>` / `<Name>Output`），含 `.`、`:`、`/` 的名称会生成不可编译的 Go 代码 | Medium（同上） |
| SAGA-CONTRACT-STEP-NAME-UNIQUE-01 | 同一 saga contract 内步骤名必须唯一；重复步骤名导致补偿日志、指标和运行时状态机无法无歧义引用步骤 | Medium（同上） |
| SAGA-CONTRACT-STEP-SCHEMA-REF-01 | 每个步骤必须声明非空的 `output` schema `$ref`；output schema 是步骤间的类型契约（下一步的 `prevState` 参数），缺失使 codegen 和工具无法验证步骤输出 | Medium（同上） |
| SAGA-CONTRACT-COMPENSATION-ORDER-01 | `compensationOrder` 若设置必须为 `"reverse"`（唯一支持值）；空值等同 `"reverse"`；其他非空值是误配置，未来运行时新增第二种策略时会导致静默误解释 | Medium（同上） |
| SAGA-CONTRACT-CONSISTENCY-L3-01 | saga contract 必须声明 `consistencyLevel: L3`（WorkflowEventual）；saga 编排本质上是跨 cell 最终一致，声明 L0/L1/L2 暗示 saga 无法提供的保证（本地或单 cell 事务语义），L4 也不允许 | Medium（同上） |
| SAGA-CONTRACT-RETRY-TIMEOUT-01 | `saga.retries`、各步骤 `retries`、`saga.timeout`、步骤 `timeout` 必须是合法的 Go duration 字符串且非负；`RetryPolicy` 约束：`MaxAttempts >= 0`、各 interval >= 0、`MaxInterval >= BaseInterval`（当两者均非零时）；此规则是 in-memory fixture 和 `codegen: false` contract 的 Medium 兜底，contractgen builder delegation 是 Hard 主门控 | Medium（委托 `kernel/saga.RetryPolicy.Validate`；Hard 主门控 = contractgen builder delegation。gh #960 已否决 parser load-time jsonschema.Validate 路径，见上行） |
| SAGA-CELL-LEVEL-L3-DECLARE-01 | 某 slice 的 contractUsage `role: orchestrate` ⟹ 其 `belongsToCell` 对应的 `cell.yaml` 必须声明 `consistencyLevel: L3`（**单向**：orchestrate ⟹ L3；不是双向，详见下方澄清章节）。slice 的 belongsToCell 不在 project 时跳过（REF 覆盖） | Medium（governance rule；`gocell validate` PhaseBase CI gate） |

---

## L3 与 Saga 的关键澄清：单向蕴含，非双向等价

**规则是单向的**：使用 saga 编排（orchestrate）⟹ `consistencyLevel: L3`，但 **L3 不等价于 saga**。

GoCell 中有三个 L3 cell 并非 saga 编排：

| Cell | 一致性级别 | L3 语义 | 是否使用 saga |
|------|-----------|--------|--------------|
| `accesscore` | L3（PR #525 由 L2 升级） | 查询投影（RBAC 规则消费 session.created 事件派生权限投影） | 否 |
| `configcore` | L3（PR #525 由 L2 升级） | 查询投影（config entry upserted 事件消费，派生热更新投影） | 否 |
| `examples/todoorder/cells/ordercell` | L3（PR #937 CQRS 投影） | CQRS 读模型投影 | 否 |

上述三个 cell 的 L3 表达的是 CLAUDE.md 中 L3 的原始含义——**跨 cell 最终一致**（查询投影、合规追踪）——这类场景不需要 saga 引擎。`SAGA-CELL-LEVEL-L3-DECLARE-01` 仅在 cell 存在 `kind: saga` contractUsage 时才触发，不会误伤这三个非 saga 的 L3 cell。

---

## 构造器 nil 守卫（SAGA-CONSTRUCTOR-NIL-GUARD-01）

`runtime/saga` 引擎的两个核心构造器有必填的 interface 依赖（按实际位置参数签名）：

- `runtime/saga.NewCoordinator(j journal.Journal, tx persistence.TxRunner, em outbox.Emitter, reg saga.Resolver, clk clock.Clock, opts ...Option)`：必填 interface 依赖 = `journal.Journal`、`persistence.TxRunner`、`outbox.Emitter`、`saga.Resolver`；`clock.Clock` 单独由 `MustHaveClock` 守。（`distlock.Locker` 是**可选**依赖，经 `WithLeaderElect` option 注入并由 `leaderElectNil` flag 单独校验，不在本规则扫描的位置参数范围内。）
- `runtime/saga/executor.NewExecutor(hb Heartbeater, clk clock.Clock, opts ...Option)`：必填 interface 依赖 = `executor.Heartbeater`；`clock.Clock` 由 `MustHaveClock` 守。

**要求**：

1. 每个非 variadic 的 interface 位置参数须在构造函数体内经 `validation.IsNilInterface(dep) → fail-fast error` 显式守卫。
2. `clock.Clock` 参数须经 `clock.MustHaveClock(clk, ...)` 守卫（`MustHaveClock` 是 programmer-error panic 的豁免形态，clock 强制位置参，见 `.claude/rules/gocell/go-standards.md`）。
3. enforcement = `SAGA-CONSTRUCTOR-NIL-GUARD-01` archtest（type-aware，绑定参数 types.Object 身份 + callee 解析；Medium）。**注意**：saga 构造器目前**未**接入 `gocell:"required"` tag funnel，故 `REQUIRED-DEP-NIL-GUARD-01` 不覆盖 saga；本 archtest 是 saga 侧的唯一守卫。

**Hard 化路径**：接入 `gocell:"required"` struct tag funnel，由 `gocell generate required-deps` 从 tag 生成 `validateRequired()`，与 22 个平台 service 一致。该迁移会把 saga 构造器纳入 `REQUIRED-DEP-NIL-GUARD-01` 的 Hard codegen 守卫，届时本 archtest 可退役。路径可行，追踪于 **gh #1317**（触发条件 = saga coordinator/executor 进入生产 cell 使用）。

---

## 参考

- 实施计划：`docs/plans/202605230231-046-saga-l3-workflow-implementation-plan.md` §4 PR-08（governance + archtest 落地 PR）
- Saga 专属 ADR：`docs/architecture/202606021000-adr-saga-l3-orchestration-engine.md`（决策 D1–D10 + enforcement 档位映射 + 威胁矩阵 + 演进路径）；本文件是 archtest / governance 导航索引，决策权威以该 ADR 为准
- 运维故障 runbook：`docs/ops/saga-runbook.md`（lease 卡死 / 补偿失败 / journal 增长三场景诊断 SQL + 决策树）
- 契约变更扇出闭环：`.claude/rules/gocell/contract-fanout.md`（`saga.Status` / `journal.EventKind` 新常量触发扇出规则）
- AI-robust 治理章程：`.claude/rules/gocell/ai-robust.md`（评级定义、archtest 文件命名约定）
