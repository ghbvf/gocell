# GoCell Backlog

> Phase 0-4 已完成并合并到 develop。本文档汇总全部待办事项。
> 更新日期: 2026-04-06

---

## Tier 1: 全量代码 Review（3-5 天）

### 目标
对 200 文件 / 18,840 行代码做跨 Phase 集成 review，产出依赖图和模块级 findings。

### 任务

| # | 任务 | 预估 |
|---|------|------|
| T1-1 | 生成模块依赖图（`go list -json ./...` → DOT/SVG） | 2h |
| T1-2 | Review kernel/（11 包，4,429 行）— 接口稳定性、coverage 交叉验证 | 4h |
| T1-3 | Review cells/（6 cell，5,811 行）— 聚合边界、errcode 一致性 | 4h |
| T1-4 | Review runtime/（8 包，2,835 行）— 生命周期、中间件完整性 | 3h |
| T1-5 | Review adapters/（6 包，4,185 行）— 接口实现合规、集成测试 | 3h |
| T1-6 | Review examples/（3 项目，233 行）— 教学质量、可运行性 | 2h |
| T1-7 | CI 加 golangci-lint / staticcheck | 2h |
| T1-8 | 产出 review 报告 + 汇总 findings | 2h |

---

## Tier 2: Review 产出的修复 + Tech-Debt 清理（3-5 天）

### 活跃 Tech-Debt（16 条）

#### P1 — 高优先级（7 条）

| ID | 来源 | 问题 | 预估 |
|----|------|------|------|
| P4-TD-03 | S6 P1-8 | `IssueTestToken` HS256 死代码（测试陷阱） | 30min |
| P4-TD-04 | S6 P2-1 | order-cell 声明 L2 但无 outboxWriter enforce | 1h |
| P4-TD-05 | S6 INT-1 | 缺少 outbox 全链路 3-container 集成测试 | 2h |
| P4-TD-06 | S6 P1-9 | CI validate 已修复（Tier 0） | RESOLVED |
| P4-TD-07 | S6 P1-5 | example docker-compose start_period 已修复（Tier 0） | RESOLVED |
| P3-TD-10 | Phase 2 #54 | Session refresh TOCTOU 竞态 | 4h（高风险） |
| P2-T-02 | Phase 2 | J-audit-login-trail e2e 测试 | 2h |

#### P2 — 中优先级（9 条）

| ID | 来源 | 问题 | 预估 |
|----|------|------|------|
| P4-TD-01 | S6 P2-5 | 缺少共享 NoopOutboxWriter | 30min |
| P4-TD-02 | S6 P2-3 | chi.URLParam 耦合（10 个文件） | 2h |
| P4-TD-09 | Tier0 F-06 | List 端点缺分页（page 字段 + 分页控制） | 2h |
| P4-TD-10 | Tier0 F-07 | POST 201 响应未包装 `{"data":...}` 格式 | 2h |
| P4-TD-11 | Tier0 F-14 | in-memory repository 缺并发测试 | 1h |
| P3-TD-11 | Phase 2 #56-59 | access-core domain 模型重构 | 4h（高风险） |
| P3-TD-12 | Phase 2 #62 | configpublish.Rollback version 校验 | 2h |
| P3-TD-04 | Phase 3 | websocket/oidc/s3 sandbox httptest 问题 | 已用 skip guard 缓解 |
| P3-TD-02 | Phase 3 | postgres adapter 覆盖率确认（testcontainers 已加，需重测） | 1h |

---

## Tier 3: 核心能力完善 — v1.1（持续）

### metadata-model-v3 校验规则补全

来源: KG 分析，对照 `docs/architecture/metadata-model-v3.md`。

| # | 缺失规则 | 优先级 | 说明 |
|---|---------|--------|------|
| G-1 | FMT-11: 动态状态字段禁入非 status-board 文件 | HIGH | V3 核心约束，完全未实现 |
| G-2 | TOPO-07: actor.maxConsistencyLevel 约束 | MEDIUM | 解析了但无校验 |
| G-3 | FMT: owner.team/owner.role 非空校验 | MEDIUM | 必填字段无验证 |
| G-4 | FMT: deprecated contract 引用阻断（非仅 warning） | MEDIUM | 当前仅警告不阻断 |
| G-5 | VERIFY: verify 标识符前缀格式严格校验 | LOW | 隐式匹配可接受 |
| G-6 | Assembly boundary.yaml 存在性校验 | LOW | 派生文件，非真相源 |
| G-7 | slice.belongsToCell / contract.ownerCell 自动推导 | LOW | DX 改善 |

### 未实现的 Kernel 子模块

来源: master-plan Section 5 vs 实际实现。Phase 4 决策 5 正式记录为 v1.1 scope cut。

| 子模块 | master-plan 描述 | 实践评估 | v1.1 优先级 |
|--------|-----------------|---------|------------|
| **kernel/wrapper** | traced sync/event/command wrapper | 解决 chi.URLParam 耦合 + 契约级可观测 | P1 |
| **kernel/command** | 命令队列接口 | iot-device 暴露 L4 无框架支持 | P1 |
| kernel/webhook | receiver + dispatcher | 无实际需求验证 | P2 |
| kernel/reconcile | 最终状态收敛 | 无实际需求验证 | P2 |
| kernel/replay | projection rebuild | 无实际需求验证 | P3 |
| kernel/rollback | rollback metadata | 无实际需求验证 | P3 |
| kernel/consumed | consumed marker | 已被 idempotency.Checker 覆盖 | DROP |
| runtime/scheduler | cron/定时任务 | 无实际需求验证 | P2 |
| runtime/retry | retry/backoff | 已在 ConsumerBase 中实现 | P3 |
| runtime/tls | TLS/mTLS | 无实际需求验证 | P3 |
| runtime/keymanager | 密钥管理 | 已在 auth/keys.go 中部分实现 | P3 |

### Cell 接口审计

| 问题 | 说明 |
|------|------|
| Cell 接口 11 个方法 | 混合了 metadata accessor + lifecycle，考虑拆分为 Cell + CellLifecycle + CellMetadata |
| adapter 15 个 t.Skip | 6 个 adapter 共 15 个 skip 的集成测试待补全 |

---

## Tier 4: 发布准备

| # | 任务 | 说明 |
|---|------|------|
| R-1 | 仓库公开或 GOPRIVATE 配置文档 | `go get` 当前无法使用 |
| R-2 | v1.0.0 tag | 无 semver tag，pkg.go.dev 无法索引 |
| R-3 | CONTRIBUTING.md | 无贡献指南 |
| R-4 | 性能基准 | 无 benchmark |
| R-5 | 棕地迁移指南 | 已有项目如何接入 GoCell |
| R-6 | 错误码目录 | 统一 errcode 文档 |

---

## 执行建议

```
Tier 1（全量 Review）→ Tier 2（修复）→ Tier 4（发布）
                                      ↘ Tier 3（v1.1 持续）
```

Tier 1 产出的 findings 决定 Tier 2 的实际范围。Tier 3 和 Tier 4 可并行。
