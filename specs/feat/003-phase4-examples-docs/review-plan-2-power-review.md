# Review Plan 2: 三角色权力 Review（一致性审查）

> 架构师、Kernel Guardian、开发者三个角色同时对全量代码进行一致性审查，聚焦跨层规则的统一执行。

## 设计理念

与 Plan 1 的"按 PR 拆分"不同，本计划从**规则一致性**角度切入：同一条规则在整个代码库中是否被统一执行。三个角色分别代表不同的权力层次：

| 角色 | 权力层次 | 核心关注 | subagent_type |
|------|---------|---------|---------------|
| **架构师** (Architect) | 战略层 — 结构决策权 | 分层依赖、接口契约、模块边界 | `architect` |
| **Kernel Guardian** | 法规层 — 合规否决权 | 元数据格式、治理规则、契约完整性 | `kernel-guardian` |
| **开发者** (Developer) | 执行层 — 实现判断权 | 编码规范、错误处理、测试质量、可维护性 | `reviewer` |

## 审查维度矩阵

### Dimension 1: 分层隔离一致性

```
规则: kernel/ 不依赖 runtime/、adapters/、cells/
      cells/ 不依赖 adapters/
      runtime/ 不依赖 cells/、adapters/
      adapters/ 实现 kernel/ 或 runtime/ 定义的接口
```

| 检查项 | 架构师 | KG | 开发者 |
|--------|--------|-----|--------|
| import 路径是否违反分层 | **主审** | 验证 | — |
| 接口定义在正确层级 | **主审** | 验证 | 实现合理性 |
| 跨层数据流方向 | **主审** | — | 实现合理性 |

**实际依赖关系（已提取）**:
```
pkg/errcode, pkg/ctxkeys, pkg/id, pkg/uid  → (无内部依赖)
pkg/httputil                                → pkg/errcode

kernel/outbox, kernel/idempotency           → (无内部依赖，纯类型/接口)
kernel/cell                                 → kernel/outbox, pkg/errcode
kernel/metadata                             → pkg/errcode
kernel/assembly                             → kernel/cell, pkg/errcode
kernel/governance                           → kernel/cell, kernel/metadata, kernel/registry
kernel/registry                             → kernel/metadata
kernel/scaffold                             → pkg/errcode
kernel/slice                                → kernel/metadata, pkg/errcode

runtime/auth, runtime/config, runtime/shutdown, runtime/worker → (stdlib only)
runtime/eventbus                            → kernel/outbox, pkg/errcode, pkg/uid
runtime/http/middleware                      → pkg/ctxkeys
runtime/http/health                         → kernel/assembly
runtime/http/router                         → kernel/cell, runtime/http/*, runtime/observability/*
runtime/observability/*                     → pkg/ctxkeys, pkg/httputil
runtime/bootstrap                           → kernel/assembly, kernel/cell, kernel/outbox,
                                              runtime/config, runtime/eventbus, runtime/http/*,
                                              runtime/shutdown, runtime/worker

adapters/postgres                           → pkg/errcode, kernel/outbox, runtime/worker ⚠️
adapters/redis                              → pkg/errcode, kernel/idempotency
adapters/rabbitmq                           → pkg/errcode, kernel/outbox, kernel/idempotency
adapters/oidc                               → pkg/errcode
adapters/s3                                 → pkg/errcode
adapters/websocket                          → (stdlib only)

cells/access-core                           → runtime/auth, kernel/cell, kernel/outbox, runtime/eventbus
cells/audit-core                            → kernel/cell, kernel/outbox
cells/config-core                           → kernel/cell, kernel/outbox
cells/device-cell                           → (待确认)
cells/order-cell                            → (待确认)
```

**已知违规**:
- `adapters/postgres/outbox_relay.go` → `runtime/worker` — adapters 应只实现 kernel/runtime 接口，但 relay 直接导入 runtime/worker

### Dimension 2: 错误处理一致性

```
规则: 禁止裸 errors.New 对外暴露
      错误必须包装上下文 fmt.Errorf("scope: %w", err)
      handler 层统一转 HTTP 状态码，domain 禁止返回 HTTP 码
      500 不暴露内部细节
```

| 检查项 | 架构师 | KG | 开发者 |
|--------|--------|-----|--------|
| errcode 使用是否统一 | — | **主审** | 实例检查 |
| string literal vs 常量 | — | **主审** | 逐文件扫描 |
| error wrap 链完整性 | 接口层 | — | **主审** |
| HTTP 状态码映射 | — | — | **主审** |

### Dimension 3: 元数据与契约一致性

```
规则: cell.yaml 必填 id/type/consistencyLevel/owner/schema.primary/verify.smoke
      slice.yaml 必填 id/belongsToCell/contractUsages/verify.unit/verify.contract
      contract.yaml 按 {kind}/{domain-path}/{version}/ 组织
      禁止旧字段名
```

| 检查项 | 架构师 | KG | 开发者 |
|--------|--------|-----|--------|
| YAML 必填字段完整性 | — | **主审** | — |
| 禁止字段检查 | — | **主审** | — |
| contract 目录结构 | **主审** | 验证 | — |
| 一致性等级 vs 实际实现 | **主审** | 验证 | **主审** |

### Dimension 4: 安全一致性

```
规则: RS256 为默认签名算法
      HS256 为 deprecated 路径
      密钥管理通过 JWTIssuer/JWTVerifier 接口
      日志禁止 dump 完整请求/响应 body
```

| 检查项 | 架构师 | KG | 开发者 |
|--------|--------|-----|--------|
| 签名算法一致性 | **主审** | — | 实现检查 |
| deprecated 路径 fail-fast | — | **主审** | 实现检查 |
| 密钥生命周期 | **主审** | — | 实现检查 |
| 日志安全 | — | — | **主审** |

### Dimension 5: 测试一致性

```
规则: 新增/修改代码覆盖率 ≥ 80%，kernel/ 层 ≥ 90%
      table-driven test
      consumer 声明要求
```

| 检查项 | 架构师 | KG | 开发者 |
|--------|--------|-----|--------|
| 覆盖率阈值 | — | 验证 | **主审** |
| 测试模式一致性 | — | — | **主审** |
| mock vs 集成测试选择 | **主审** | — | **主审** |

## 执行计划

### Phase 1: 并行扫描（3 角色同时）

每个角色启动 **2 个子 agent**（共 6 个 agent 并行）：

```
架构师-Agent-1: 扫描 kernel/ + runtime/ 的分层依赖 + 接口一致性
架构师-Agent-2: 扫描 adapters/ + cells/ + examples/ 的边界合规

KG-Agent-1: 扫描所有 *.yaml 元数据 (cell.yaml, slice.yaml, contract.yaml)
KG-Agent-2: 扫描所有 .go 文件的 errcode 使用 + 禁止字段 + 治理规则

开发者-Agent-1: 扫描 kernel/ + runtime/ + adapters/ 的编码规范
开发者-Agent-2: 扫描 cells/ + examples/ + tests/ 的编码规范 + 测试质量
```

### Phase 2: 交叉验证（1 轮）

Phase 1 输出后，启动 **1 个汇总 agent**：
- 合并三角色 findings
- 标记**冲突意见**（如架构师认为 OK 但 KG 认为违规）
- 按 Dimension 归类，输出一致性评分

### Phase 3: 裁决

对冲突意见进行裁决：
- 分层违规 → 架构师有最终决定权
- 元数据/契约违规 → KG 有否决权
- 实现细节 → 开发者有最终决定权

## 输出格式

```markdown
# 一致性 Review 报告

## 一致性评分卡
| Dimension | 一致率 | P0 | P1 | P2 | 裁决者 |
|-----------|--------|-----|-----|-----|--------|
| 分层隔离 | 95% | 1 | 2 | 0 | 架构师 |
| 错误处理 | 82% | 2 | 5 | 3 | 开发者 |
| 元数据契约 | 90% | 0 | 3 | 2 | KG |
| 安全 | 88% | 1 | 2 | 1 | 架构师 |
| 测试 | 78% | 0 | 4 | 2 | 开发者 |

## 冲突裁决记录
| # | 架构师意见 | KG意见 | 开发者意见 | 裁决 | 理由 |
...

## 按 Dimension 详细 Findings
...
```

## 产出文件

- `review-plan2-architect-findings.md` — 架构师视角
- `review-plan2-kg-findings.md` — KG 视角
- `review-plan2-developer-findings.md` — 开发者视角
- `review-plan2-consistency-report.md` — 汇总 + 裁决

## 预计总 agent 数: 7 (Phase1: 6 + Phase2: 1)
