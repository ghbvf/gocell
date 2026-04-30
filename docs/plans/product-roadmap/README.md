# GoCell 产品路线规划

> 状态：**实施期**（2026-04-30 锁定全开源 MIT + 方案 D 单仓多 module + winmdm 第一客户）
> 受众：产品决策者 / 长期技术规划 / winmdm 项目实施者
> 与 `../engineering-baseline/` 的关系：framework 自身改造在 engineering-baseline；本目录是 GoCell 作为产品底座的应用规划 + winmdm PRD

## 文档清单

- **`202604301130-microsoft-zt-stack-analysis.md`** —— **微软零信任栈架构分析（参考材料）**
  - §0 为什么对标微软栈（事实标杆 / 完整产品线 / 生态最大 / 公开文档完整）
  - §1 三原则（Verify explicitly / Least privilege / Assume breach）
  - §2 6 支柱架构（Identity / Endpoint / Apps / Data / Infra / Network）+ 跨切关注点（Visibility / Automation / Governance）
  - §3 Conditional Access 中央策略引擎（信号源 / 决策 / if-then DSL）
  - §4 数据流：信号 → Microsoft Graph API → CA → 执行点
  - §5 Entra ID 作为身份 + 设备身份的事实数据库（不在 Intune 而在 Entra ID）
  - §6 Intune 作为 compliance hub（自家 MDM + 19 家第三方）
  - §7 三类 Partner API（Device Compliance Partner 19 家 / MTD Partner 多家 / SAML+OIDC + Graph API 统一接入）
  - §8 数据保护层（Purview MIP + BitLocker，GoCell 不做）
  - §9 学习点 vs 不照搬部分
  - §10 对 GoCell zt 设计的具体启示（验证 zt-extensibility 设计 + 微软栈给的额外灵感）
  - **核心结论**：zt-extensibility 探讨文档的设计与微软高度一致，方向正确

- **`202604301100-zt-extensibility-and-mdm-decoupling.md`** —— **零信任可扩展性 + MDM 解耦探讨（探讨期，zt 启动时回顾）**
  - §0 触发：winmdm PRD 对齐时识别 zt 设计两条原则
  - §1 两条核心原则：(1) 企业现有系统接入零代码改造；(2) zt module 不依赖 mdm
  - §2 问题 1：企业现有系统接入 zt 三种模式（A 零接入 IAP 反代 / B 半接入虚拟 cell + Actor / C 深度接入 SDK + ext_authz Sidecar）
  - §3 问题 2：4 个中立契约（devicestate / deviceidentity / devicecompliance / remotecommand）提到 core/contracts；外部 MDM adapter（Intune/Jamf/SCCM）建在 zerotrust/adapters/externalmdm
  - §4 与现有路线图冲突点（zt 启动时决策清单）
  - §5 启动条件 / 决策时间点（2028 Q4 设计 / 2029 Q1 PR Z1.1）
  - §7 总结：当前不动作，winmdm Phase 1 按 PRD 推进；2029 Q1 zt 启动时回顾
  - **唯一可提前的工作**：4 个中立契约定义可提前到 winmdm Stage 2 做（+1-2 周）

- **`202604301030-winmdm-prd-on-gocell.md`** —— **winmdm 产品需求文档（基于 GoCell 重构版，第一客户）**
  - §0 与旧 v6.0 PRD 的差异（5 微服务 → 11 cell + 6 assembly；Redis Streams → RabbitMQ；2026 Q1 MVP → 2027 Q1 启动）
  - §1 产品概述 + 核心架构原则（MDM 优先 + 通道完整自治 + deviceidentity 唯一设备 + 通道无关策略 + 优化项延后）
  - §2 GoCell Cell 设计（11 cell × 多 slice + 关键设计要点：deviceidentity / policycell.router / devicelifecycle）
  - §3 Assembly 拓扑（6 + 1 兜底 + 部署形态切换 + 跨 assembly 通信 + wmcore HA）
  - §4 开发顺序（4 Stage 串行：基础设施 → MDM → Agent → 上层应用）
  - §5 功能模块映射（v6.0 F-E/F-D/F-G/F-P/F-S/F-R/F-M/F-A → cell.slice 对照）
  - §6 验收标准（继承 v6.0 SMART + 新增 unified_id 一致性）
  - §7 NFR + §8 路线图与里程碑（M0-M5）+ §9 Phase 2+ 优化项清单
  - §10 仓库与代码组织 + §11 与 GoCell 路线图协同 + §12 决策点
  - §13 旧 v6.0 章节迁移索引

- **`202604300950-plan-d-go-workspace-multimodule-migration.md`** —— **方案 D 实施计划（已采纳）**
  - §1-2 决策回顾 + 当前 vs 目标状态（mdm module 已写实 winmdm 11 cell + 1 adapter + 6+1 assembly）
  - §3-4 目录结构设计 + 4 个 module 划分（core / mdm / zerotrust / tools）
  - §5 go.work + 各 go.mod 配置（**顶层 = core 关键设计**：现有 import path 零修改）
  - §6 import path 迁移策略
  - §7 archtest 跨 module 配置（CM-LAYER-01~13 守卫）
  - §8 contracts 跨 module 引用 + boundary.yaml fingerprint
  - §9 CI/CD 多 module path-based filter（节省 ~70% CI 时间）
  - §10 转换路径：阶段 0 不动 / **阶段 1 PR A1.1-A1.17 winmdm 11 cell 渐进开发** / 阶段 2 加 zerotrust module
  - §11 与 E1-E10 + winmdm Stage 1-4 路线图协同
  - §12-15 风险评估 + 时间估算 + 决策点 + 总结

- **`202604300930-repository-structure-decision.md`** —— **仓库结构决策（已锁定方案 D）**
  - §0 决策结论（2026-04-30 锁定全开源 MIT + 方案 D）
  - §1 5 个候选方案对比（保留作历史记录）
  - §2 业界对照（Kubernetes / HashiCorp / CloudWeGo / Temporal / uber-go）
  - §4.1-4.5 ~~商业化方案 C~~（已弃用，仅作历史参考）
  - §4.6 **方案 D 全开源单仓多 module（已采纳）**
  - §10 总结：MIT 单仓多 module + winmdm 作为 mdm module 第一客户

- **`202604300900-gocell-as-platform-foundation.md`** —— GoCell 作为平台底座（路线图视角）
  - §1 基于 25 包的库形态能拼出什么应用
  - §2 Tier C 进一步解耦分析（E15 候选）
  - §3 **应用 1：MDM 系统（winmdm，第一客户）**：11 cell + 1 adapter + 6+1 assembly + Stage 1-4 时间估算（17 个月含 GoCell v1.0）
  - §4 应用 2：企业零信任开发平台（8 个新 cell + 4 adapter，复用 winmdm 阶段 cell）
  - §5 总体路线（M0-M7 里程碑 + 风险 + 全开源策略）
  - §6 决策点（多数已确认）

## 与 framework 视角的解耦

| 视角 | 目录 | 关心什么 |
|---|---|---|
| **Framework 视角** | `../engineering-baseline/` | GoCell 自身怎么改进（E1-E14 落点、API 简化、codegen 消化样板、库化承诺） |
| **产品视角**（本目录） | `./` | GoCell 作为底座做什么产品（winmdm / 零信任 / 其他）+ winmdm 第一客户 PRD |

两个视角解耦：framework 改造与产品演进可以并行推进；framework v1.0 完成（M0 2026 Q4）是 winmdm Stage 1（M1 2027 Q1）的前置条件。

## 推荐阅读顺序

1. 先读 `../engineering-baseline/202604300800-final-form-capability-overview.md` 了解 GoCell 最终形态
2. 读 `202604301030-winmdm-prd-on-gocell.md` 了解 winmdm 第一客户的完整需求和实施计划
3. 读 `202604300900-gocell-as-platform-foundation.md` §3-§5 看路线图视角的 winmdm + 零信任布局
4. 读 `202604300950-plan-d-...` 了解方案 D 实施细节（go.work / 多 module 切换 PR）
5. 仅当怀疑方案选型时再读 `202604300930-...`（决策已锁定，作为历史参考）
6. zt 启动前（2028 Q4）回顾 `202604301100-zt-extensibility-and-mdm-decoupling.md` + `202604301130-microsoft-zt-stack-analysis.md`

## 关键决策摘要（2026-04-30 锁定）

| 决策项 | 结论 |
|---|---|
| 商业模式 | **全开源 MIT**（所有产品 / 项目统一） |
| 仓库结构 | **方案 D**：单仓库 `github.com/ghbvf/gocell/` + go workspace 多 module |
| Module 划分 | core（顶层）/ mdm / zerotrust / tools |
| 第一客户 | **winmdm**（Windows 私有化 MDM，详见 PRD） |
| 协议优先级 | Windows-only（OMA-DM / SyncML / XCEP / WSTEP / MS-MDE） |
| EventBus | RabbitMQ（GoCell 默认；Redis Streams 不引入） |
| 时间线 | GoCell v1.0 2026 Q4 → winmdm v1 GA 2028 Q1 → 零信任 v1 GA 2029 Q4 |
| Phase 2+ 延后 | WNS / WebRTC / P2P / TimescaleDB / WebSocket / 远程桌面 / BitLocker 密钥托管 / 补丁 / OOBE |
