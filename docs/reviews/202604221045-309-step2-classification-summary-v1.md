# 步骤2：分类、频次、处理方案、对标状态、领域与分层评估（汇总）

> 日期: 2026-04-22
> 依据: docs/reviews/202604221030-308-step1-common-issues-six-seat-report.md

---

## 1. 分类矩阵

### 1.1 按领域分类

| Domain | 问题数 | 代表问题 |
|---|---:|---|
| auth/security | 4 | CI-01, CI-04, CI-08, CI-09 |
| consistency/token | 2 | CI-10, CI-07 |
| contract/governance | 2 | CI-05, CI-06 |
| testing/quality-gate | 3 | CI-02, CI-03, CI-12 |
| observability/runtime-health | 1 | CI-11 |

### 1.2 按分层分类

| Layer | 问题数 | 代表问题 |
|---|---:|---|
| cells | 3 | CI-01, CI-07, CI-10 |
| runtime | 4 | CI-04, CI-05, CI-08, CI-11 |
| adapters | 1 | CI-09 |
| pkg/contracts | 1 | CI-06 |
| examples | 1 | CI-02 |
| tests/ci/cmd | 2 | CI-03, CI-12 |

---

## 2. 频次评估

### 2.1 根因簇频次

| RootCauseCluster | 频次 | 问题数 | 说明 |
|---|---|---:|---|
| RC-A | 高 | 3 | 路由-策略-注册链路断裂在多轮审查重复出现 |
| RC-B | 高 | 3 | 生产安全能力可选化问题重复出现 |
| RC-C | 高 | 2 | 契约与测试双轨长期存在 |
| RC-D | 高 | 3 | 运行态门禁不足多次触发 |
| RC-E | 中 | 1 | readiness 预算问题已出现但尚未泛化 |

### 2.2 严重级别频次

| Severity | 数量 |
|---|---:|
| P0 | 3 |
| P1 | 7 |
| P2 | 2 |

---

## 3. 处理方案矩阵

| ID | 推荐处理时机 | 方案A（最小修复） | 方案B（彻底方案） |
|---|---|---|---|
| CI-01 | 立即 | audit 路由改 auth 声明注册 + 回归测试 | 引入统一 RegisterRoutes 规范化改造 |
| CI-02 | 立即 | 修 key 长度并补 smoke | 建立 examples 配置静态检查器 |
| CI-03 | 立即 | 恢复 2 条关键 Journey | 建完整 full-assembly harness 覆盖全部 Journey |
| CI-04 | 本迭代 | real 模式强制 NonceStore | 引入统一 anti-replay 基础设施与度量 |
| CI-05 | 本迭代 | 启动期 route-policy 对账 | 统一策略 DSL + 编译期生成校验 |
| CI-06 | 本迭代 | 共享 schema types | 建 contract baseline registry + breaking CI gate |
| CI-07 | 本迭代 | 关键事件先typed化 | 全量事件 typed payload + 代码生成 |
| CI-08 | 下迭代 | 最小双 listener | 全量 route-group 隔离 + 网络策略联动 |
| CI-09 | 本迭代 | real 禁 static token | 完整 AppRole/K8s auth + 续期可观测 |
| CI-10 | 本迭代 | 切 refresh store 主链 | 清理旧 repo 接口并补并发/回归测试 |
| CI-11 | 下迭代 | 聚合 deadline | checker 接口升级 + 并发预算执行器 |
| CI-12 | 立即 | 扩 workflow 分支 | 统一治理 required checks 策略 |

---

## 4. 对标状态矩阵

| Theme | 项目数 | 状态 | 备注 |
|---|---:|---|---|
| route-policy 闭环 | 3 | pending | 候选: Kubernetes/Istio/Kratos |
| control-plane auth hardening | 3 | pending | 候选: Vault/Kubernetes/SPIRE |
| refresh rotation/replay | 3 | pending | 候选: Hydra/Keycloak/Auth0 |
| readiness budget | 3 | pending | 候选: Kubernetes/etcd/go-micro |
| contract governance gate | 3 | pending | 候选: go-zero/Kratos/Kubernetes |

门禁声明:
- 当前为主题候选和项目池映射，尚未完成逐主题证据提取。
- 在完成每主题 >=3 项目可追溯证据前，禁止下“最佳实践”结论。

---

## 5. 领域评估与分层评估

### 5.1 领域风险评估

| 领域 | 风险等级 | 原因 |
|---|---|---|
| auth/security | 高 | 存在 P0/P1 且涉及 internal 控制面 |
| testing/quality-gate | 高 | 首跑与旅程回归门禁缺口 |
| contract/governance | 中高 | 双轨模型导致长期语义漂移 |
| consistency/token | 中高 | refresh 主链中间态影响一致性 |
| runtime-health | 中 | readiness 预算问题需治理但暂未阻断 |

### 5.2 分层风险评估

| 分层 | 风险等级 | 原因 |
|---|---|---|
| runtime | 高 | 安全门禁与路由策略闭环问题集中 |
| cells | 高 | 业务路由与事件 payload 漂移风险 |
| tests/ci | 高 | 动态验证缺口导致回归漏检 |
| adapters | 中高 | Vault real 模式策略不收敛 |
| pkg/contracts | 中高 | contract schema 双轨造成治理偏差 |
| examples | 中高 | 对外体验入口稳定性不足 |

---

## 6. 下一步（步骤3/4/5输入）

1. 步骤3（领域六席位）输入:
- auth/security
- testing/quality-gate
- contract/governance
- consistency/token
- runtime-health

2. 步骤4（分层六席位）输入:
- runtime
- cells
- tests/ci
- adapters
- pkg/contracts
- examples

3. 步骤5（TOP10候选）:
- 从 CI-01..CI-12 按风险分筛选 TOP10，保留至少 4 个领域与 4 个分层覆盖。
