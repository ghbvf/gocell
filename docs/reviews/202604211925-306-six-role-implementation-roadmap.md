# 六角色联合实施路径规划（架构优先）

## 规划原则

- 优先级顺序：架构调整 > 设计与分层收口 > bug 修复 > 新功能
- 通过条件：每阶段定义明确 exit criteria，未达标不进入下一阶段
- 角色协同：每阶段由六席位共同验收（架构/安全/测试/运维/可维护性/产品）

---

## Stage 0：准入门槛（立即）

目标：先清理阻塞项，确保可进入重构阶段。

### 待完成事项

1. L1 审计路由策略闭环
2. S-nonce real 模式强制 NonceStore
3. S4b/A14 real 模式禁止 static vault token

### 六角色验收点

- 架构：路由声明、策略绑定、启动校验链一致
- 安全：重放攻击与静态凭据路径被硬阻断
- 测试：401/403/200/admin 跨用户链路测试齐全
- 运维：启动失败信息可诊断，指标可观测
- 可维护性：禁止新增旁路注册方式
- 产品：对外行为稳定（错误码与语义一致）

Exit Criteria：P0=0 且生产前 P1 阻塞=0

---

## Stage 1：架构收口（第一优先）

目标：建立防复发架构门禁。

### 工作包

1. L2 PolicyRegistry（路由-策略完整性）
2. A21 健康检查签名升级与 budget 统一
3. EventRouter Setup/Run 双阶段语义固化（对齐 ER-ARCH-01）

### 六角色分工

- 架构：定义 registry 与生命周期边界
- 安全：定义策略完备性最小规则（白名单机制）
- 测试：新增启动期 fail-fast 与回归矩阵
- 运维：定义 readiness 指标与告警阈值
- 可维护性：抽象接口，控制复杂度与重复代码
- 产品：确保错误反馈对使用者可理解

Exit Criteria：

- 未声明策略路由可在启动期阻断
- /readyz 在受控 budget 内稳定返回
- eventrouter 启动就绪与运行健康语义分离

---

## Stage 2：设计与分层治理（第二优先）

目标：建立单一事实源，降低长期演进成本。

### 工作包

1. L6 contract 共享模型下沉（pkg/contracts）
2. CONTRACT-BREAKING-01 最小规则集上线
3. L8 分页错误处理 helper 收口
4. L11 main/release 治理门禁覆盖

### 六角色分工

- 架构：定义 schema 共享边界与依赖方向
- 安全：确保错误模型与契约字段不会泄露敏感信息
- 测试：metadata/contracttest 一致性测试与 breaking baseline 测试
- 运维：将 contract-breaking 纳入 CI required checks
- 可维护性：清理双轨结构与重复逻辑
- 产品：统一错误与分页响应体验

Exit Criteria：

- contract 解析模型只有一套权威类型
- breaking 变更在 CI 可阻断
- 分页错误处理行为一致

---

## Stage 3：缺陷修复与运行态质量（第三优先）

目标：补齐“可运行”防线，减少发布后回归。

### 工作包

1. L7 examples-smoke job + key 长度稳态构造
2. F10 恢复 2 条高价值 journey（session-refresh, audit-login-trail）
3. internal policy 对齐（L10）

### 六角色分工

- 架构：确保示例路径与真实组装路径一致
- 安全：覆盖 refresh/internal 负向用例
- 测试：把 skip stub 逐步替换为可执行 harness
- 运维：完善 smoke 失败日志与快速定位信息
- 可维护性：避免示例层重复实现基础组件
- 产品：保证示例“开箱即跑”

Exit Criteria：

- examples 在 CI 可启动
- 关键 journey 不再依赖 `t.Skip`
- internal 调用语义一致无漂移

---

## Stage 4：新功能（最后）

目标：在治理基线稳定后推进功能新增，避免债务滚雪球。

### 可进入的新功能池（示例）

1. P1-8 device-list API
2. P1-4 validate JSON/SARIF 输出
3. P1-5 metadata 性能基准

进入条件：Stage 0-3 全部达标

---

## 推荐实施节奏（两周示例）

1. Week 1：Stage 0 + Stage 1
2. Week 2：Stage 2 + Stage 3（并行小队）
3. Week 3 起：按 capacity 进入 Stage 4

---

## 风险与应对

1. 风险：架构变更影响面大
- 应对：先加门禁后迁移，采用 feature flag/白名单过渡

2. 风险：治理规则过严导致短期阻塞
- 应对：提供过渡豁免但设到期时间

3. 风险：测试恢复带来 CI 时长上升
- 应对：分层执行（PR 快速集 + 夜间全量集）

---

## 下一阶段启动建议

仅当以下条件满足才进入“新功能增加”阶段：

1. 审计路由策略漏洞已关闭
2. 控制面认证 fail-closed 已落地
3. 路由策略 registry 与健康 budget 门禁上线
4. 合同模型单一事实源与 breaking gate 已启用
