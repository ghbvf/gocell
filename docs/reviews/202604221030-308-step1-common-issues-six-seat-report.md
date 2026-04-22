# 步骤1：常见问题报告（六席位并行抽取）

> 日期: 2026-04-22
> 数据源: docs/reviews, docs/backlog.md, docs/tech-debt-registry.md, specs/*/review-findings.md, VS Code debug-logs（补充）
> 方法: 六席位独立抽取 -> 根因簇归并 -> 常见问题清单

---

## 1. 执行摘要

- 六席位执行状态: 完成（6/6）
- 归并后常见问题: 12 条
- 严重度分布: P0=3, P1=7, P2=2
- 复杂度分布: Cx1=4, Cx2=5, Cx3=3
- 高频根因簇: RC-A 安全声明闭环缺失、RC-B 运行态门禁不足、RC-C 契约与测试双轨漂移

---

## 2. 常见问题清单（跨席位归并）

| ID | Severity | Complexity | 问题 | 主要证据 | 影响 | 处理方向 |
|---|---|---|---|---|---|---|
| CI-01 | P0 | Cx2 | 审计路由裸注册，策略未绑定 | docs/reviews/202604210001-v1.0-plan-completion-audit.md:57 | internal 数据越权风险 | 统一声明式路由注册 + 启动强校验 |
| CI-02 | P0 | Cx2 | 示例应用首跑崩溃（cursor key 长度） | docs/backlog.md:36 | 首次接入体验失败，示例不可用 | 修 key 构造 + examples-smoke 门禁 |
| CI-03 | P0 | Cx3 | Journey 验收长期 stub，跨 Cell 回归失效 | docs/backlog.md:114 | 发布前无法验证关键业务旅程 | 先恢复2条关键 Journey 并纳入 CI |
| CI-04 | P1 | Cx2 | ServiceToken NonceStore 非强制，重放窗口存在 | docs/backlog.md:31 | internal API 可被重放 | real 模式强制 NonceStore 缺失即 fail-fast |
| CI-05 | P1 | Cx3 | 缺 route-policy 启动对账，易重复引入裸路由 | docs/backlog.md:33 | 同类安全回归重复发生 | 新增 PolicyRegistry + Verify gate |
| CI-06 | P1 | Cx3 | contracttest 与 metadata 双轨模型 | docs/backlog.md:35 | 契约语义漂移，测试“假绿” | 统一共享 schema 类型 + breaking-check |
| CI-07 | P1 | Cx2 | 关键事件 payload 使用 map[string]any | docs/backlog.md:80 | 编译期无法约束字段变更 | typed event DTO + 合约对齐 |
| CI-08 | P1 | Cx3 | internal/public 仍共 listener，边界隔离不足 | docs/reviews/202604211230-023-auth-federated-whistle-six-seat-review.md:1 | 网络隔离与故障域边界不清 | 先双 listener，再 route-group 化 |
| CI-09 | P1 | Cx2 | Vault real 模式可静态 token 运行 | docs/backlog.md:68 | 生产认证策略易降级 | real 模式禁止 static token，接 AppRole/K8s auth |
| CI-10 | P1 | Cx2 | 刷新令牌主链未收口（F2中间态） | docs/reviews/202604211230-023-auth-federated-whistle-six-seat-review.md:1 | replay/一致性/审计语义不稳定 | 切主链到 refresh store 并清理旧路径 |
| CI-11 | P2 | Cx3 | Readyz 缺统一 deadline 与并发预算 | docs/backlog.md:73 | 探针尾延迟放大，误判风险上升 | Checker 签名升级 + 聚合预算 |
| CI-12 | P2 | Cx1 | 运行关键分支缺治理门禁覆盖（main/release） | docs/backlog.md:118 | 发布路径可绕过治理校验 | 扩展 workflow 触发分支并设 required check |

---

## 3. 根因簇（按共享根因归并）

| RootCauseCluster | 共享根因 | 关联问题 | 频次 | 涉及席位 |
|---|---|---|---|---|
| RC-A | 路由/策略声明与执行链路分离，缺 fail-fast | CI-01, CI-05, CI-08 | 高 | 架构/安全/测试/运维/产品 |
| RC-B | 生产安全能力未 fail-closed（可选化） | CI-04, CI-09, CI-10 | 高 | 安全/架构/运维 |
| RC-C | 契约与测试双轨、类型化不足 | CI-06, CI-07 | 高 | 架构/测试/可维护性 |
| RC-D | 运行态门禁弱于编译门禁 | CI-02, CI-03, CI-12 | 高 | 测试/运维/产品/可维护性 |
| RC-E | 运行时健康与可观测抽象滞后 | CI-11 | 中 | 运维/可维护性 |

---

## 4. 开源对标主题候选（步骤2/5使用）

1. route-policy 声明闭环与启动校验（Kubernetes / Istio / Kratos）
2. 控制面 token 防重放与机器认证（Vault / Kubernetes / SPIRE）
3. refresh token rotation 与 replay 处置（Hydra / Keycloak / Auth0）
4. readiness budget 与探针模型（Kubernetes / etcd / go-micro）
5. 契约治理单一事实源与 breaking gate（go-zero / Kratos / Kubernetes API conventions）

说明:
- 对标主题仅做候选，不在本报告下“最佳实践”定论。

---

## 5. 下一步输入

- 输入到步骤2: CI-01..CI-12 + RootCauseCluster
- 输入到步骤5: 按风险分生成 TOP10 候选池
