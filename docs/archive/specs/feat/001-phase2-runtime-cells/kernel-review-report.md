# Kernel Review Report -- Phase 2: Runtime + Built-in Cells

## 审查人: Kernel Guardian
## 日期: 2026-04-05

---

## 7 维度评分

| # | 维度 | 评分 | 说明 |
|---|------|------|------|
| A | 工作流完整性 | 绿 | S0-S8 全部执行，gate-audit.log 记录 S1/exit ~ S8/entry 共 14 条 PASS，无 FAIL 或跳过 |
| B | Speckit 合规 | 黄 | Phase 2 首次启用工作流。spec/plan/tasks/decisions 结构完整，但属于"边做边建"，部分模板（checklist-template.md 只产出 requirements.md 而非标准 checklist）尚未稳定。可接受的初始偏差 |
| C | 角色完整性 | 绿 | role-roster 声明 12 个 ON 角色 + 6 个 Review Bench 席位全部 ON。前端开发者/DevOps 标记 OFF 并附 SCOPE_IRRELEVANT 理由，首次跳过无警告。review-architect/review-roadmap/review-product-manager/kernel-constraints 四份审查报告均存在且被 decisions.md 逐条裁决 |
| D | 内核集成健康度 | 绿 | kernel/ 本 Phase 新增 Subscriber 接口（kernel/outbox）和 HTTPRegistrar/EventRegistrar/RouteMux 可选接口（kernel/cell），均通过编译 + 测试。kernel/ 全部包覆盖率 >= 90%（assembly 94.5%, cell 99.0%, governance 96.2%, journey 100%, metadata 96.7%, registry 100%, scaffold 93.2%, slice 94.2%）。kernel/ 未新增对 runtime/adapters/cells 的依赖。gocell validate 零 error。无退化 |
| E | 标准文件齐全度 | 绿 | 21 个文件齐全：phase-charter / role-roster / spec / review-architect / review-roadmap / review-product-manager / kernel-constraints / decisions / product-context / plan / tasks / task-dependency-analysis / product-acceptance-criteria / research / checklists/requirements / review-findings / tech-debt / qa-report / user-signoff / gate-audit.log。缺少 kernel-review-report.md（本文件，S8.2 产出） |
| F | 反馈闭环 | 黄 | Phase 2 首次启用工作流，Phase 0+1 在工作流体系前完成，无上一 Phase 的 kernel-review-report / product-review-report / tech-debt 可继承。phase-charter.md 已显式标注 N/A 并说明理由。连续性断裂是事实但非过失 |
| G | Tech Debt 趋势 | 黄 | 新增 23 [TECH] + 3 [PRODUCT] = 26 条，解决 0 条。首次启用工作流无历史基线，绝对数量可接受（Phase 2 新增约 120 文件/80+ 模块，26 条约每 4.6 文件 1 条）。但 3 条高风险项需关注：bootstrap 覆盖率 51.4%（#15）、handler 层覆盖率缺口（#13）、config watcher 未集成 bootstrap（#20） |

---

## 维度详细分析

### A. 工作流完整性

gate-audit.log 完整记录了 S1 ~ S8 的 entry/exit 检查点：

```
S1/exit   PASS → S2/entry PASS → S2/exit PASS → S3/entry PASS
S3/exit   PASS → S4/entry PASS → S4/exit PASS → S5/entry PASS
S5/exit   PASS → S6/entry PASS → S6/exit PASS → S7/entry PASS
S7/exit   PASS → S8/entry PASS
```

S0 (Init) 通过 role-roster.md + phase-charter.md 的存在性证明已执行。全流程无回退、无跳过。

### B. Speckit 合规

Phase 2 是工作流体系首次全程实践。成果：

- spec.md 结构完整（FR-1~FR-13, NFR-1~NFR-5, 15 节，约 800 行）
- decisions.md 14 项裁决，逐条记录来源、理由、被否决方案
- tasks.md 42 项任务，4+1 Wave 结构，含依赖标注和 Gate 验证
- product-acceptance-criteria.md 按 P1/P2/P3 分级，覆盖所有 FR

偏差：checklists/ 目录仅含 requirements.md 而非模板定义的标准 checklist 格式。属于模板自身尚在成熟的初始偏差。

### C. 角色完整性

12 个 ON 角色在对应产出中均有痕迹：

| 角色 | 产出证据 |
|------|---------|
| 总负责人 | decisions.md 14 项裁决 |
| 架构师 | review-architect.md (10 条建议) |
| 产品经理 | review-product-manager.md (10 条建议) |
| 项目经理 | plan.md Wave 划分 + task-dependency-analysis.md |
| Kernel Guardian | kernel-constraints.md (9 条 KG-建议 + 29 条约束清单) |
| 后端开发者 | feat commit (0c2e257) ~120 文件变更 |
| 文档工程师 | doc.go (3 包) + Cell 开发指南 |
| QA 自动化 | qa-report.md + 48 包全 PASS |
| 6 个 Review Bench 席位 | review-findings.md (P0: 2, P1: 14, P2: 17) |

### D. 内核集成健康度

Phase 2 对 kernel/ 的修改范围：

1. **kernel/outbox**: 新增 `Subscriber` 接口 -- `Subscribe(ctx, topic, handler) error`，与已有 `Publisher` 对称。设计正确，与 Watermill message.Subscriber 对标
2. **kernel/cell**: 新增可选接口 `HTTPRegistrar`（`RegisterRoutes(mux RouteMux)`）和 `EventRegistrar`（`RegisterSubscriptions(sub outbox.Subscriber)`），通过类型断言调用，保持 Cell 接口向后兼容
3. **kernel/cell**: 新增 `RouteMux` 抽象类型，避免 cells/ 直接 import chi

验证结果：
- 分层隔离：kernel/ 无 runtime/cells/adapters import（C-01~C-04 PASS）
- 生命周期：BaseCell 状态机正确（C-05~C-08 PASS，qa-report S1 验证）
- 元数据合规：gocell validate 零 error（C-09~C-13 PASS）
- 覆盖率：kernel/ 全部 >= 90%（C 要求维持）

**结论：内核无退化，扩展合理且受控。**

### E. 标准文件齐全度

对照工作流 S0-S8 标准产出物清单：

| 文件 | 存在 | 来源阶段 |
|------|------|---------|
| phase-charter.md | 有 | S0 |
| role-roster.md | 有 | S0 |
| spec.md | 有 | S1 |
| review-architect.md | 有 | S2 |
| review-roadmap.md | 有 | S2 |
| review-product-manager.md | 有 | S2 |
| kernel-constraints.md | 有 | S2 |
| decisions.md | 有 | S3 |
| product-context.md | 有 | S3 |
| plan.md | 有 | S4 |
| tasks.md | 有 | S4 |
| task-dependency-analysis.md | 有 | S4 |
| product-acceptance-criteria.md | 有 | S4 |
| research.md | 有 | S4 |
| checklists/requirements.md | 有 | S4 |
| review-findings.md | 有 | S6 |
| tech-debt.md | 有 | S6 |
| qa-report.md | 有 | S7 |
| user-signoff.md | 有 | S7 |
| gate-audit.log | 有 | 全程 |
| kernel-review-report.md | 本文件 | S8 |

全部标准文件齐全。

### F. 反馈闭环

Phase 2 是首次启用完整工作流的 Phase。Phase 0+1 在工作流体系建立前完成，因此：

- 无上一 Phase 的 kernel-review-report.md 可供"必须修复项"继承
- 无上一 Phase 的 tech-debt.md 可供"遗留已解决"统计
- phase-charter.md 的 N/A 声明表已显式记录此情况

本 Phase 产出的 tech-debt.md（26 条）和本报告的"必须修复项"将成为 Phase 3 的反馈闭环起点。从 Phase 3 开始，此维度可正常评估。

### G. Tech Debt 趋势

首次基线，统计概览：

| 类别 | 新增 | 解决 | 净增 |
|------|------|------|------|
| [TECH] | 23 | 0 | +23 |
| [PRODUCT] | 3 | 0 | +3 |
| **合计** | **26** | **0** | **+26** |

按风险域分布：

| 域 | 数量 | 高风险项 |
|----|------|---------|
| 安全/权限 | 9 | SEC-03(密钥硬编码), SEC-06(XFF 信任), SEC-11(无认证中间件) |
| 测试/回归 | 7 | T-01(handler 覆盖率), T-03(bootstrap 覆盖率) |
| 架构一致性 | 4 | ARCH-04(BaseSlice 空壳), ARCH-07(L2 无事务) |
| 运维/部署 | 3 | D-07(config watcher 未集成 bootstrap) |
| DX | 2 | DX-02(11 包缺 doc.go) |
| 产品/UX | 3 | -- |

26 条中 23 条标记 Phase 3 修复，3 条标记 Phase 3-4。全部为有意识的延迟决策，非遗漏。Phase 2 无 DB/adapter，安全和一致性类债务（SEC-03~SEC-11, ARCH-07）在 Phase 3 引入真实基础设施后自然需要解决。

---

## 必须在下一 Phase (Phase 3) 修复的项 (不超过 3 条)

### 1. [TECH #20] config watcher 未集成到 bootstrap 生命周期

**理由**: J-config-hot-reload journey 依赖 config watcher 与 bootstrap 的完整集成。当前 watcher 独立运行，bootstrap.Stop 不会停止 watcher，存在资源泄漏和优雅关闭不完整风险。Phase 3 引入真实配置热更新场景后此问题无法回避。

**修复方向**: bootstrap 的 Run() 中启动 config.Watcher，Stop() 中关闭。watcher 的 context 应从 bootstrap 的 shutdownCtx 派生。

### 2. [TECH #13] 10/16 slices handler 层覆盖率 < 80%

**理由**: Cell 级聚合覆盖率达标（85-87%），但 handler 层作为 HTTP 边界层，是安全攻击面（参数注入、类型混淆、响应泄露）的集中区域。Phase 3 引入认证中间件（SEC-11 修复）后，handler 测试是验证认证链正确性的必要条件。

**修复方向**: 使用 httptest.NewRecorder + chi.NewRouter 对每个 handler 端点编写请求-响应级测试，覆盖正常路径 + 参数错误 + 认证失败。

### 3. [TECH #5 + #6] cmd/core-bundle 密钥硬编码 + JWT HS256

**理由**: 两项合并处理。SEC-03 密钥硬编码虽有注释标记，但 Phase 3 引入 Docker 部署后若未修复将直接暴露。SEC-04 HS256 对称签名在多实例部署时密钥分发困难，RS256 是 spec 原始要求（phase-charter.md 明确提及 "JWT RS256"）。Phase 3 是最后的无用户流量窗口，必须完成迁移。

**修复方向**: (a) 密钥改为环境变量读取 + 启动时缺失则 fail-fast; (b) JWT 签名算法迁移至 RS256，Cell.Init 注入公私钥对。

---

## 总体评价

Phase 2 是 GoCell 从元数据治理框架到可运行框架的关键转型，同时也是 Speckit 工作流的首次全程实践。两个维度均成功完成。

**工程交付**: 约 120 文件变更，新增 runtime/ 13 个模块 + 3 个 Cell（16 slices）+ kernel/ 接口扩展。48 个测试包全 PASS，kernel/ 覆盖率 >= 90%，cells/ 覆盖率 >= 80%（Cell 级聚合），gocell validate 零 error。核心架构约束（分层隔离、Cell 生命周期状态机、元数据合规、契约完整性）全部守住，内核无退化。

**工作流成熟度**: S0-S8 全执行无跳过，14 条 gate 全 PASS。4 方审查（架构/Roadmap/内核/产品）共产出 39 条建议，decisions.md 逐条裁决（accept 26 / defer 7 / reject 3），tech-debt 26 条全部有意识登记。这为 Phase 3 建立了可追溯的反馈闭环基线。

**风险可控**: 26 条 tech-debt 中无 P0 遗留（S6 已修复 bcrypt + DTO 两个 P0 安全问题）。3 条"必须修复项"均有明确修复方向和 Phase 3 窗口。user-signoff CONDITIONAL APPROVE，所有适用视角评分 >= 3。

**Kernel Guardian 判定: Phase 2 PASS。**
