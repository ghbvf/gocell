# Task Dependency Analysis — Phase 2

> 产出日期: 2026-04-05
> 输入: tasks.md (42 tasks, 5 waves) + kernel-constraints.md (9 KG issues, 29 constraints)

---

## 任务依赖图

```
Wave 0 — 前置准备
==================================================

  T-001 (YAML 元数据修正)
    |
    +--[P]--> T-003 (Cell 可选注册钩子)
    |           |
    +--[P]--> T-004 (CLAUDE.md 依赖规则)
    |           |
    +--[S]--> T-002 (outbox Subscriber 接口)
                |
                +---+---+
                    |
                    v
                  T-005 (Wave 0 Gate)

Wave 1 — runtime/ 独立模块 (全部 [P] 依赖 T-005)
==================================================

  T-005
    |
    +--[P]--> T-010 (request_id)
    +--[P]--> T-011 (real_ip)
    +--[P]--> T-012 (recovery)
    +--[P]--> T-013 (access_log)
    +--[P]--> T-014 (security_headers)
    +--[P]--> T-015 (body_limit)
    +--[P]--> T-016 (rate_limit)
    +--[P]--> T-020 (config)
    +--[P]--> T-021 (shutdown)
    +--[P]--> T-022 (worker)
    +--[P]--> T-023 (auth 接口+中间件)
    +--[P]--> T-024 (metrics)
    +--[P]--> T-025 (tracing)
    +--[P]--> T-026 (logging)
    |
    +--[P]--> T-027 (eventbus) ← 额外依赖 T-002
    |
    +--[S]--> T-028 (Wave 1 Gate) ← 依赖 T-010..T-027 全部完成

Wave 2 — runtime/ 集成 + Cell domain
==================================================

  T-028
    |
    +--[S]--> T-030 (health)
    |           |
    |           v
    |         T-031 (router) ← 还依赖 T-010..T-016
    |           |
    |           v
    |         T-032 (bootstrap) ← 还依赖 T-020, T-021, T-022, T-027
    |
  T-005
    |
    +--[P]--> T-040 (access-core domain)
    +--[P]--> T-041 (audit-core domain)
    +--[P]--> T-042 (config-core domain)
    |
    +-------> T-033 (Wave 2 Gate) ← 依赖 T-032, T-040, T-041, T-042

Wave 3 — Cell 完整实现
==================================================

  T-033
    |
    +--[S]--> T-050 (config-core Cell, 参考实现)
    |           |
    |           +--[S]--> T-051 (access-core Cell) ← 模式参考 T-050
    |           |
    |           +--[S]--> T-052 (audit-core Cell)  ← 模式参考 T-050
    |
    +--[S]--> T-053 (Wave 3 Gate) ← 依赖 T-050, T-051, T-052

Wave 4 — 集成 + 验证 + 文档
==================================================

  T-053
    |
    +--[S]--> T-060 (cmd/core-bundle 更新)
    |           |
    |           +--[S]--> T-061 (Hard Gate Journey)
    |           +--[S]--> T-062 (Soft Gate Journey)
    |           +--[S]--> T-067 (内核集成验证)
    |           +--[S]--> T-068 (覆盖率 Gate)
    |           +--[S]--> T-071 (E2E 集成测试)
    |
    +--[P]--> T-063 (runtime doc.go)
    +--[P]--> T-064 (Cell 开发指南)
    +--[P]--> T-065 (README 更新)
    +--[P]--> T-072 (OpenAPI 文档生成)
    |
  T-005
    +--[P]--> T-066 (Makefile)
    +--[P]--> T-070 (Docker placeholder)
    |
    +-------> T-069 (最终 Gate) ← 依赖 T-061..T-068 全部
```

---

## 关键路径

### 主线关键路径（最长依赖链）

```
T-001 → T-002 → T-005 → T-027 → T-028 → T-030 → T-031 → T-032 → T-033
  → T-050 → T-051 → T-053 → T-060 → T-061 → T-069
```

**链长**: 14 个任务，串行强制顺序

**时间估算（以任务复杂度单位）**:

| 任务 | 复杂度 | 累计 | 阻塞说明 |
|------|--------|------|---------|
| T-001 | 中 | 1 | 元数据修正，KG-1/2/3 全部在此解决 |
| T-002 | 低 | 2 | Subscriber 接口声明，KG-6 前置 |
| T-005 | 低 | 3 | Gate 验证 |
| T-027 | 高 | 4 | EventBus 实现，对标 Watermill，含 Publisher + Subscriber + 重试 + DLQ |
| T-028 | 低 | 5 | Gate 验证 |
| T-030 | 低 | 6 | health 端点 |
| T-031 | 中 | 7 | router 集成 |
| T-032 | 高 | 8 | Bootstrap 编排器，对标 fx + Kratos，最复杂的 runtime 组件 |
| T-033 | 低 | 9 | Gate 验证 |
| T-050 | 高 | 10 | config-core 参考实现，5 slices |
| T-051 | 高 | 11 | access-core，7 slices + JWT RS256，Phase 2 最复杂任务 |
| T-053 | 低 | 12 | Gate 验证 |
| T-060 | 中 | 13 | core-bundle 集成 |
| T-061 | 高 | 14 | 5 条 Hard Gate Journey 端到端验证 |
| T-069 | 低 | 15 | 最终 Gate |

### 次要关键路径

```
T-001 → T-005 → T-040 → T-033 → T-052 → T-053 → T-060 → T-062 → T-069
```

此路径受 T-033 阻塞——T-040/T-041/T-042 虽可并行，但必须等待 T-032（bootstrap）完成才能进入 Wave 2 Gate。

### 关键路径节点风险标注

| 节点 | 关键路径位置 | 阻塞风险 |
|------|------------|---------|
| T-027 (EventBus) | 第 4 位 | **极高** — 所有事件驱动 Cell 功能、3 条 Soft Gate Journey、audit-core 全部依赖此实现 |
| T-032 (Bootstrap) | 第 8 位 | **高** — Wave 3 全部 Cell 实现和 Wave 4 集成验证的唯一入口 |
| T-051 (access-core) | 第 11 位 | **高** — JWT RS256 + 7 slices，复杂度最大，且 Hard Gate Journey 5 条中 4 条依赖 access-core |
| T-050 (config-core) | 第 10 位 | **中** — 作为参考实现，模式选型错误将导致 T-051/T-052 返工 |

---

## 风险点

| # | 风险 | 阻塞概率 | 影响范围 | 缓解措施 |
|---|------|---------|---------|---------|
| R1 | **EventBus 实现延迟 (T-027)**：in-process EventBus 需同时实现 Publisher + Subscriber + 重试 + DLQ，对标 Watermill 复杂度不低。T-002 的 Subscriber 接口设计如果不稳定，T-027 需返工 | 高 (60%) | T-028 Gate 阻塞 → Wave 2/3/4 全部延迟；audit-core 6 个事件订阅无法工作；3 条 Soft Gate Journey 失败 | 1) T-002 接口设计时参考 Watermill message.Subscriber 签名确定稳定接口 2) T-027 分两步：先实现同步 Publish/Subscribe，再补重试+DLQ 3) 在 T-027 完成前，T-040/T-041/T-042 domain 层可并行推进 |
| R2 | **Bootstrap 编排复杂度 (T-032)**：需集成 config + eventbus + assembly + HTTP server + worker + shutdown，任一模块 API 不稳定都会阻塞 Bootstrap | 高 (50%) | Wave 3 全部 Cell 实现阻塞；cmd/core-bundle 无法启动；所有 Journey 验证无法执行 | 1) Wave 1 各模块产出时明确公开 API 签名并 freeze 2) Bootstrap 采用 interface 编程，允许模块 stub 替代 3) 优先实现最小可运行 Bootstrap（config + assembly + HTTP），worker/eventbus 后挂载 |
| R3 | **JWT RS256 实现 (T-051)**：access-core 包含 JWT 签发/验证 + session 管理 + RBAC + 7 slices，是单个最复杂任务。golang-jwt/jwt/v5 的密钥管理和 Claims 自定义有学习曲线 | 中 (40%) | T-053 Gate 延迟；5 条 Hard Gate Journey 中 4 条（login/refresh/logout/lockout）阻塞 | 1) T-023 (runtime/auth) 定义 TokenVerifier/Authorizer 接口时，同步设计 JWT Claims 结构 2) T-051 拆分：先实现 session-validate + identity-manage（登录核心），再实现 authorization/rbac 3) JWT 签发/验证封装为独立 internal/jwt 包，可单独测试 |
| R4 | **YAML 元数据缺口 (KG-1/2/3)**：audit-core 缺少 6 个 subscribe 声明，config-subscribe contractUsages 为空，http.auth.me.v1 无 serving slice。如果 T-001 修正不完整，gocell validate 在每个 Gate 都会报错 | 高 (30%) | 每个 Wave Gate 验证阻塞；Stage 7 QA 的 gocell validate 零 error 目标无法达成 | 1) T-001 必须覆盖 KG-1/2/3/8 所有修正项 2) T-001 出口条件硬卡 `gocell validate` 零 error 3) 维护一个 checklist 逐项勾选 |
| R5 | **Slice 依赖注入模式未确定 (KG-4)**：Slice.Init(ctx) 不接受依赖参数，16 个 slice 的 service 注入模式不统一将导致 Wave 3 返工 | 中 (50%) | T-050/T-051/T-052 实现模式不一致；Cell 开发指南 (T-064) 无法准确描述 | 1) 在 T-003 (kernel/cell 可选注册钩子) 中明确采用方案 B：构造时注入 2) T-050 (config-core 参考实现) 作为模式验证，确认后 T-051/T-052 复制模式 3) 在 T-003 产出时补充 interface 注释说明注入约定 |
| R6 | **Assembly 与 Bootstrap 职责重叠 (KG-5)**：Assembly.Start 和 Bootstrap 各自有启动编排逻辑，错误处理和回滚策略可能冲突 | 低 (20%) | T-032 实现返工；Cell 生命周期约束 C-06/C-07 验证失败 | 1) T-032 设计时明确：Bootstrap 是 Assembly 的使用者，不是替代者 2) Assembly 负责 Cell 子集生命周期，Bootstrap 负责顶层编排 3) 在 spec 补充职责边界图 |
| R7 | **跨 Cell Journey 验证失败 (T-062)**：J-audit-login-trail / J-config-hot-reload / J-config-rollback 全部依赖 EventBus 跨 Cell 事件流转，任一环节断裂整条 Journey 失败 | 中 (40%) | T-069 最终 Gate 可能降级（Soft Gate Journey 允许 stub 辅助，但仍需基本可通过） | 1) T-062 标记为 Soft Gate，允许 stub 辅助通过 2) 优先确保 T-061 Hard Gate Journey 全部 PASS 3) 跨 Cell 事件流转用 integration test 在 T-071 提前验证 |

---

## Batch 划分建议

基于并行度最大化原则，将 42 个任务重组为 10 个 Batch：

### Batch 1: 元数据对齐 + 内核接口扩展
- **包含任务**: T-001, T-002, T-003, T-004
- **前置条件**: Phase 1 代码稳定，develop 分支 green
- **并行度**: T-003, T-004 可并行；T-002 串行依赖 T-001；T-001 必须首先完成
- **执行顺序**: T-001 → {T-002 | T-003 | T-004}
- **出口条件**: 
  - `gocell validate` 零 error
  - kernel/outbox/outbox.go 包含 Subscriber 接口
  - kernel/cell/interfaces.go 包含 HTTPRegistrar / EventRegistrar
  - go build + go test 通过
- **预估复杂度**: 中
- **KG 覆盖**: KG-1, KG-2, KG-3, KG-6(部分), KG-8

### Batch 2: Wave 0 Gate
- **包含任务**: T-005
- **前置条件**: Batch 1 全部完成
- **并行度**: 无（单任务）
- **出口条件**: `go build ./... && go test ./... && gocell validate` 全绿
- **预估复杂度**: 低

### Batch 3: HTTP 中间件全集
- **包含任务**: T-010, T-011, T-012, T-013, T-014, T-015, T-016
- **前置条件**: Batch 2 (T-005) 通过
- **并行度**: **7 个任务完全并行**，零互相依赖
- **出口条件**: 
  - 每个中间件有独立 _test.go
  - 覆盖率 >= 80%
  - go build + go test 通过
- **预估复杂度**: 中（单个低，合计中）

### Batch 4: runtime/ 独立服务模块
- **包含任务**: T-020, T-021, T-022, T-023, T-024, T-025, T-026, T-027
- **前置条件**: Batch 2 (T-005) 通过；T-027 额外依赖 T-002（已在 Batch 1 完成）
- **并行度**: **8 个任务完全并行**（与 Batch 3 也可并行）
- **出口条件**: 
  - 每个模块有独立测试
  - runtime/eventbus 实现 outbox.Publisher + outbox.Subscriber
  - 覆盖率 >= 80%
- **预估复杂度**: 高（T-027 EventBus 和 T-023 auth 接口是重点）
- **KG 覆盖**: KG-6(完成)

> **注意**: Batch 3 和 Batch 4 可完全并行执行，最大并行度 = 15 个任务。

### Batch 5: Wave 1 Gate + Cell Domain 模型
- **包含任务**: T-028, T-040, T-041, T-042, T-066, T-070
- **前置条件**: Batch 3 + Batch 4 全部完成（T-028 依赖 T-010..T-027）
- **并行度**: 
  - T-028 串行（Gate 验证）
  - T-040/T-041/T-042 实际依赖 T-005（已在 Batch 2 完成），可与 Batch 3/4 并行推进，但 Gate 验证 T-033 需等 T-032
  - T-066/T-070 依赖 T-005，可任意时间并行
- **执行顺序**: T-028 → {T-040 | T-041 | T-042 | T-066 | T-070}
- **出口条件**: 
  - `go test ./runtime/... -cover` >= 80%
  - 3 个 Cell domain 模型编译通过 + 测试绿色
  - Makefile 可用
- **预估复杂度**: 中
- **优化说明**: T-040/T-041/T-042 虽然在 tasks.md 中标为依赖 T-005，但逻辑上它们是纯 domain 模型，不依赖 runtime/。如果实施人手充足，可提前到与 Batch 3/4 并行启动。

### Batch 6: runtime/ 集成（health + router + bootstrap）
- **包含任务**: T-030, T-031, T-032
- **前置条件**: T-028 (Wave 1 Gate) 通过
- **并行度**: **严格串行** T-030 → T-031 → T-032
- **出口条件**: 
  - Bootstrap 可编排 Assembly 启动/关闭
  - /healthz + /readyz 端点可用
  - chi-based router 挂载中间件 + 健康检查 + metrics
- **预估复杂度**: 高（T-032 是 Phase 2 第二复杂任务）
- **KG 覆盖**: KG-5

### Batch 7: Wave 2 Gate + Cell 实现
- **包含任务**: T-033, T-050, T-051, T-052
- **前置条件**: Batch 6 (T-032) + Batch 5 (T-040..T-042) 全部完成
- **并行度**: 
  - T-033 串行（Gate 验证）
  - T-050 必须先完成（参考实现）
  - T-051 和 T-052 可并行，但都依赖 T-050 的模式
- **执行顺序**: T-033 → T-050 → {T-051 | T-052}
- **出口条件**: 
  - 3 个 Cell 各自编译 + 测试绿色
  - 覆盖率 >= 80%
  - Cell 实现遵循 config-core 参考模式
- **预估复杂度**: 高（Phase 2 最大工作量集中区，T-051 含 JWT 实现）
- **KG 覆盖**: KG-4(验证方案 B), KG-7(确认依赖白名单)

### Batch 8: Wave 3 Gate + 集成交付
- **包含任务**: T-053, T-060
- **前置条件**: Batch 7 (T-050..T-052) 全部完成
- **并行度**: 严格串行 T-053 → T-060
- **出口条件**: 
  - go build + 可启动 core-bundle
  - 注册顺序: config-core → access-core → audit-core
  - Bootstrap 正确编排 3 个 Cell 生命周期
- **预估复杂度**: 中

### Batch 9: 验证 + 文档（并行扇出）
- **包含任务**: T-061, T-062, T-063, T-064, T-065, T-067, T-068, T-071, T-072
- **前置条件**: Batch 8 (T-060) 完成
- **并行度**: 
  - 验证类: T-061/T-062/T-067/T-068/T-071 可并行
  - 文档类: T-063/T-064/T-065/T-072 可并行
  - 两组之间也可并行
  - **最大并行度 = 9 个任务**
- **出口条件**: 
  - 5 条 Hard Gate Journey PASS
  - 3 条 Soft Gate Journey PASS（允许 stub 辅助）
  - C-01~C-29 内核约束全部通过
  - runtime/ >= 80%, cells/ >= 80%, kernel/ >= 90%
  - 文档覆盖 runtime/ + Cell 开发指南 + README
- **预估复杂度**: 高（验证量大，但并行度高可缓解）

### Batch 10: 最终 Gate
- **包含任务**: T-069
- **前置条件**: Batch 9 全部完成
- **并行度**: 无（单任务）
- **出口条件**: 
  - go build + go test + gocell validate 全绿
  - 5 Hard Gate + 3 Soft Gate Journey PASS
  - 覆盖率达标
  - 所有内核约束 C-01~C-29 通过
- **预估复杂度**: 低

---

## Batch 时序总览

```
时间轴 →

Batch 1  [T-001→{T-002|T-003|T-004}]
           |
Batch 2  [T-005]
           |
           +------------------------------------------+
           |                                          |
Batch 3  [T-010|T-011|T-012|T-013|T-014|T-015|T-016] |
           |                                          |
Batch 4  [T-020|T-021|T-022|T-023|T-024|T-025|T-026|T-027]
           |                                          |
           +------------------------------------------+
           |
Batch 5  [T-028→{T-040|T-041|T-042|T-066|T-070}]
           |
Batch 6  [T-030→T-031→T-032]
           |
Batch 7  [T-033→T-050→{T-051|T-052}]
           |
Batch 8  [T-053→T-060]
           |
Batch 9  [{T-061|T-062|T-063|T-064|T-065|T-067|T-068|T-071|T-072}]
           |
Batch 10 [T-069]
```

**总串行深度**: 10 Batch（不可压缩）
**最大并行宽度**: Batch 3+4 同时 = 15 个任务
**关键瓶颈 Batch**: Batch 6 (bootstrap 串行链) 和 Batch 7 (Cell 实现串行链)

---

## 优化建议

### 可提前并行的任务

以下任务逻辑依赖低于 tasks.md 声明的依赖，可提前启动：

| 任务 | 声明依赖 | 实际最小依赖 | 可提前到 |
|------|---------|-------------|---------|
| T-040 (access-core domain) | T-005 | 无（纯 domain 模型） | 与 Batch 1 并行 |
| T-041 (audit-core domain) | T-005 | 无（纯 domain 模型） | 与 Batch 1 并行 |
| T-042 (config-core domain) | T-005 | 无（纯 domain 模型） | 与 Batch 1 并行 |
| T-066 (Makefile) | T-005 | 无（纯 DevOps） | 与 Batch 1 并行 |
| T-063/T-064/T-065 (文档) | T-053 | T-050 完成后即可开始文档 | 与 Batch 7 后半段并行 |

### 串行瓶颈缓解

1. **T-030 → T-031 → T-032 链**（Batch 6）：三者严格串行。建议 T-032 (bootstrap) 先实现最小骨架（只编排 config + assembly + HTTP），worker 和 eventbus 挂载点后补，使 Batch 7 可更早开始。

2. **T-050 → T-051 链**（Batch 7）：T-051 依赖 T-050 的模式参考，但如果 T-050 完成第一个 slice 的完整模式（handler + service + test）后，T-051 即可开始，无需等 T-050 全部 5 个 slice 完成。

3. **T-051 和 T-052 并行**：tasks.md 标为串行（都依赖 T-050 模式参考），实际 T-052 (audit-core) 的 domain 模式与 T-051 (access-core) 不同（事件驱动 vs HTTP 服务）。两者可安全并行。
