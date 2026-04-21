# 三计划六席位并行审查总报告（含开源对标）

生成时间：2026-04-21 14:30  
审查对象：
- docs/plans/202604191515-auth-federated-whistle.md
- docs/plans/202604200313-v1.0-pre-release-plan.md
- docs/plans/202604201800-pg-pilot-layering-refactor-plan.md
- docs/backlog.md

审查方法：
- 六席位并行：架构 / 安全 / 测试 / 运维 / 可维护性 / 产品
- 根因合并：按“症状 -> 数据流/调用链 -> 架构原因 -> 影响范围”聚类
- 外部对标：每个主要建议主题均并行对比 3 个上游项目

## 1. 审查范围与总体风险

总体结论：
1. `202604201800-pg-pilot-layering-refactor-plan.md` 主体交付已完成，且与 backlog 已完成条目基本一致（R1a-R1e + R2a/R2b + L0）。
2. v1.0 前仍有 1 个 P0 阻塞（L1 裸路由鉴权缺失）与 5 个 P1 高优先（安全门禁、测试门禁、分支治理门禁）。
3. 三计划间存在“状态与口径漂移”：同一事项在计划和 backlog 的状态表达不一致，影响执行确定性。

风险判定：中高（可控）
- 可控性来自：已有清晰 backlog 与较完整架构改造基础。
- 风险来自：发布前门禁未完全“硬化”为阻塞规则，仍有条件延后项与验收断链。

## 2. 已完成 / 未完成盘点

### 2.1 已完成（确认）

1. pg-pilot 主体完成：R1a/R1b/R1c/R1d/R1e/R2 已合入。
2. Vault 核心链路完成：envelope、readiness、token renewal 指标、keyID 契约强化。
3. BuildApp + CellModule + SharedDeps 已替代 God Struct 路径。
4. Demo key/secret reject 已落地（L0、S2）。
5. backlog 已回填多项完成态（A13/A17/A19/A20/M3/A3 等）。

### 2.2 未完成（含条件延后）

阻塞/高优先：
1. L1 审计查询路由未受策略保护（P0）。
2. L2 路由策略注册表与启动期校验未落地（P1）。
3. S-nonce（service token 防重放）未落地（P1，条件延后）。
4. S4b + A14（real 模式 static token 禁止 + pluggable auth）未落地（P1/P2，条件延后）。
5. L11（governance CI 覆盖 main/release）未落地（P1）。
6. F10（journey harness 恢复）未落地，核心验收链路不足（P1）。
7. L7-examples（startup smoke）仍需转成发布前硬门禁（P1）。

改进优先：
1. L10 internal policy 对齐（防御纵深）。
2. L7(FMT15)/L8（API 一致性治理）
3. A21（health checker 统一 ctx budget）
4. S10/S11（config-core 类型语义与复杂度进一步收口）

## 3. 合并问题表（按严重级）

| 严重级别 | 来源席位 | 问题 | 证据（文件） | 影响 | 修复方向 |
|---|---|---|---|---|---|
| P0 | 安全/架构/产品 | L1 裸路由导致审计查询越权风险 | docs/backlog.md | 非 admin 可能访问审计数据，直接影响安全边界 | 立即修 L1，并同批落地 L2 防回归 |
| P1 | 安全/运维 | service token 防重放未落地（S-nonce） | docs/backlog.md | 多 pod/重试链路可重放 | real 模式强制 NonceStore + replay 集测 |
| P1 | 安全/运维 | real 模式 static vault token 仍可运行（S4b+A14 未闭环） | docs/backlog.md, docs/plans/202604201800-pg-pilot-layering-refactor-plan.md | 生产认证边界未封口，续期降级风险 | 合并一个 VAULT-AUTH wave：S4b+A14+可观测同批完成 |
| P1 | 测试/运维 | governance 仅 develop 分支门禁（L11） | docs/backlog.md, .github/workflows/governance.yml | main/release 可绕过治理 | 扩展 required checks 到 main/release |
| P1 | 测试/产品 | examples 可 build 但启动失败风险（L7-ex）与 journey 大面积 skip（F10） | docs/backlog.md | 首跑体验与发布验收不可靠 | L7-ex 先阻塞，F10 分阶段恢复并最终阻塞 |
| P1 | 架构/可维护性 | 三计划状态口径漂移（同事项多处不一致） | 三份 plan + backlog | 排期和验收判断混乱 | 统一 backlog 单一事实源，plan 仅保留实施路径 |
| P2 | 架构/运维 | readiness 聚合预算未统一（A21） | docs/backlog.md | checker 增长后尾延迟和误判增加 | health.Checker 升级为 ctx 版本，统一 deadline |
| P2 | 可维护性 | L8 分页错误处理重复模式未收口 | docs/backlog.md + 多 handler | 行为漂移与维护成本高 | 抽 pkg/httputil/pagination helper 后机械替换 |

## 4. 根因问题簇（含数据流 / 调用链）

### 簇 A：路由安全“声明存在，但门禁不闭环”

症状：
- L1 仍存在裸路由。
- L2 启动期策略校验尚未落地。

数据流：
- 请求进入 router -> 路由注册 -> handler 执行。
- 若未被策略包装，鉴权边界在入口即失守。

调用链（概念）：
- RegisterRoutes -> 路由挂载 -> AuthMiddleware/Policy（应当）-> handler。
- 当前缺口是“路由挂载到策略绑定”的强制校验缺失。

架构原因：
- 依赖人工约束，而非启动期 fail-fast。

影响范围：
- 审计、internal 路由、未来新增端点均受影响。

### 簇 B：生产认证“核心能力有了，但部署门禁未封口”

症状：
- Vault envelope/renew/readiness 已完成。
- S4b/A14 仍条件延后。
- S-nonce 仍条件延后。

数据流：
- token 获取 -> 续期/重认证 -> internal 请求鉴权 -> 业务执行。
- 任一环节无强制门禁，会在 real 模式放大风险。

调用链（概念）：
- New provider/authenticator -> startup validate -> middleware guard -> request handling。

架构原因：
- 条件延后规则未绑定“发布阻塞条件”。

影响范围：
- 多 pod、生产化部署、内部高权限接口。

### 簇 C：测试与发布门禁“覆盖有设计，闭环不足”

症状：
- journey 测试大面积 skip。
- examples smoke 未统一阻塞。
- governance 未覆盖主分支。

数据流：
- 代码变更 -> CI 子集 -> 合并 -> 发布。
- 当前是“合并通过 != 发布可信”。

调用链（概念）：
- PR checks -> protected branch checks -> release candidate checks。

架构原因：
- 门禁分层存在，但未制度化为强约束。

影响范围：
- 发布稳定性、回归可见性、首跑体验。

### 簇 D：计划治理“单一事实源不足”

症状：
- 计划已完成与待开工描述共存。
- 同事项在 plan/backlog 状态漂移。

数据流：
- 审查结论 -> 计划更新 -> backlog 回填 -> 发布看板。

架构原因：
- 计划文档同时承载草案/执行/归档三角色。

影响范围：
- 任务切分、优先级判断、跨团队对齐。

## 5. 开源对比表（每主题 3 项）

> 说明：以下主题均已满足“每个主题 >=3 个独立上游项目”对比要求。

### 主题 A：路由策略门禁与启动期校验（对应 L1/L2/L10）

| 项目 | 观察到的模式 | 适配 GoCell 的结论 |
|---|---|---|
| Kubernetes apiserver | default deny、authz 前置、启动期 fail-fast、最小豁免面 | L2 应做“未声明策略即启动失败”；internal 默认拒绝 |
| Kratos | selector/operation 抽象，策略匹配与业务分离 | 继续推进策略声明集中化，但要补强冲突/遗漏校验 |
| go-zero | 生成式路由+鉴权绑定，减少裸路由 | 可借鉴“单一注册通道”，但需补“未绑定策略禁止注册” |

### 主题 B：Vault 认证模式、续期与健康探测（对应 S4b/A14/A13/A19）

| 项目 | 观察到的模式 | 适配 GoCell 的结论 |
|---|---|---|
| HashiCorp Vault | auth method 分层、lifetime watcher、sys/health 状态语义 | real 模式必须 auth mode 明确化 + 续期失败分级处理 |
| external-secrets | 多 auth 源配置模型、TTL 校验+重登、Ready/Reason 状态化 | A14 需先统一 auth spec，再接续期与 readiness 观测 |
| Bank-Vaults | 生产优先工作负载身份、静态 token 仅应急、探针+自愈 | S4b 不应继续后置到“临发布前”，应前置到 v1.0 门禁 |

### 主题 C：测试分层与发布门禁（对应 F10/L11/L7-ex）

| 项目 | 观察到的模式 | 适配 GoCell 的结论 |
|---|---|---|
| Kubernetes + test-infra | PR blocking / release blocking 分层，分支保护+required checks | 先把 L11/L7-ex 变阻塞，再分阶段提升 journey 到 release blocking |
| Temporal | unit/integration/functional 分层，flaky 治理闭环，异步重型测试轨道 | journey 可先 informing 再 blocking，并建立 flaky 指标闭环 |
| etcd | 主线快门禁 + 稳定性专项轨道 + 发布资产校验 | 建议 GoCell 建立“发布候选门”，避免仅依赖 PR 绿灯 |

## 6. 对未完成计划的优化建议

1. 把“先架构门禁、后问题收口、再新功能”制度化（见新计划文件）。
2. L1 与 L2 绑定为同一风险包：先修漏洞，再建防回归。
3. 将 S4b+A14+S-nonce 从“条件延后”升级为 v1.0 发布门禁（至少 real 模式强制）。
4. L11 与 L7-ex 立即前置，F10 分阶段恢复并在发布候选前全绿。
5. 以 backlog 作为唯一状态源：计划文档只保留实施路径与依赖，不重复维护完成状态。

## 7. 对已完成项的继续优化点（增量）

1. pg-pilot 已完成项归档化：从 active 计划中剥离历史执行细节，保留结论与遗留项。
2. BuildApp/CellModule 不再做结构性大改，只补冲突检测与可观测字段。
3. Vault 链路优先补 auth mode 门禁，不扩散额外 provider 复杂度。
4. readiness 优先做统一 ctx budget（A21）而非再加新 checker 类型。

## 8. 阻塞项与改进项

阻塞合并/发布项：
1. L1
2. L2
3. S-nonce（real/multi-pod）
4. S4b + A14（real 模式）
5. L11
6. L7-ex + F10（按分阶段门槛）

改进项：
1. L10
2. L7(FMT15)/L8
3. A21
4. S10/S11

## 9. 最稳妥实施方向（摘要）

第一阶段（架构与门禁）：L2 + L11 + L7-ex + 状态治理
第二阶段（风险问题）：L1 + S-nonce + S4b/A14 + F10 阶段1
第三阶段（一致性与体验）：L10 + L7/FMT15 + L8 + A21 + F10 阶段2
第四阶段（新功能）：P1-8 与其余功能需求

---

审查结论：
- 三计划方向总体正确，且 pg-pilot 已高质量完成。
- 现阶段核心不是“重做架构”，而是“把剩余门禁硬化并闭环”。
- 按新计划重排后，v1.0 风险可在 2~3 个批次内显著下降。