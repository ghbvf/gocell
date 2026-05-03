# L4 计划：反复模式根治（剩余 — Claude 分层规则骨架 + retrospective）

> 日期: 2026-04-26（精简版: 2026-04-29，移除已完成阶段 0/1 详情、已迁移阶段 3）
> 基线: `origin/develop @ ad986cad`（2026-04-29，同步至最近合入 PR #316；#321/#323/#325/#326/#316 未关闭本 plan 剩余范围）
>
> **状态摘要**：
> - ✅ 阶段 0 Baseline 同步（#291 / #292）
> - ✅ 阶段 1 CI 模式治理 8/8 + 6.1 收尾（#293 / #294 / #295 / #296 / #297 / #298 / #300 / #302 / **#307 PR-MODE-6.1**）；累计 ~68h；详细落地见 git log（commits `293..302` + `307`）。
> - ⤴ 阶段 3 v1 Schema 演进 ADR：已迁入 `docs/plans/202604272358-2-2-ci-batch2-k8s-verify.md` 的 PR-CI-3 V1-RESPONSE-EVOLVE；本 plan 不再保留独立阶段。
> - ⏳ **本 plan 剩余**：阶段 2（PR-CLAUDE-LAYERED 骨架）+ 阶段 4（6 角色 retrospective），合计 ~7h；阶段 4 前置中的 batch2 PR-CI-1 已由 #321 关闭，仍需等待 PR-CI-2/3/4/5/6。

---

## Context（保留）

2026-04-26 分层 6 角色全仓审查暴露 **31 条 P1 / 8 个反复模式**。阶段 1 已用 8 条 archtest 静态规则消除 ~14 条 P1 未来复发（58% 反复模式自动拦截）。剩余两段把治理沉到 Claude 分层骨架 + 跑 retrospective 收尾。

**8 个反复模式**（用于阶段 2 占位条款）：
fail-open 默认值 / 双源漂移 / 失败语义不一致 / 测试假绿 / 错误传播不完整 / 边界泄漏 / 声明 ≠ 实现 / 调度可观测弱

---

## 阶段 2 — Claude 分层规则骨架（PR-CLAUDE-LAYERED，~4h，⏳ 未开始）

**抽象**：参考 `docs/new-setting/` 已设计未应用的分层方案，**先写大概骨架** + 把反复模式守护作为占位条款写入对应层 CLAUDE.md，**详细内容后续 plan 再展开**。

**目标**：
- 建立分层目录结构（kernel/CLAUDE.md 等 6 个层 CLAUDE.md）
- 反复模式守护占位（每条 1-3 行 + TODO 标注）
- review/fix skill 维度补全
- memory 强约束 4 个反复模式 IN_SCOPE

**改动清单**：

| 文件 | 内容 | 状态 |
|---|---|---|
| `kernel/CLAUDE.md`（新） | 大概骨架 + **Panic 禁止列表**（占位 1-2 段，TODO 后续详化） | 复制 `docs/new-setting/kernel/CLAUDE.md` 现有骨架 + 加 Panic 段 |
| `runtime/CLAUDE.md`（新） | 大概骨架 + **Fail-Closed 默认值原则**（占位）+ Composition Root 模式 | 复制 + 加段 |
| `adapters/CLAUDE.md`（新） | 大概骨架 + **构造器 error-first** + **TLS 强制（real mode）** + **连接预算** 占位 | 复制 + 加段 |
| `cells/CLAUDE.md`（新） | 大概骨架 + **Metadata 声明同步** + **边界纯化（不暴露 adapter 类型）** 占位 | 复制 + 加段 |
| `pkg/CLAUDE.md`（新） | 大概骨架 | 复制 |
| `contracts/CLAUDE.md`（新） | 大概骨架 + **v1 演进策略（指向 ADR）** 占位 | 复制 + 加段 |
| 根 `CLAUDE.md` | 精简 + 加"渐进式披露：进入子目录加载对应 CLAUDE.md" 说明；保留跨层规则 | 改 |
| `.claude/rules/gocell/eventbus.md` | L51 后补 **Fail-Closed 原则**（consumer init / unmarshal / 幂等三段，占位 1-2 行 + TODO）| 扩 |
| `.claude/rules/gocell/go-standards.md` | L101 后新 **Panic 禁止列表**（kernel/cells/runtime/adapters 禁 panic 占位）| 扩 |
| `.claude/agents/reviewer.md` | "运维/部署" 维度后加 3 项：Consumer 生命周期 / Zero-value 安全 / Panic 扫描 | 扩 |
| `.claude/skills/fix/SKILL.md` | L52 批量模式 Triage 步骤 4 后加 **反复模式标签**（[FAIL-OPEN] / [PANIC-RISK] / [METADATA-DRIFT] / [TEST-QUALITY] / [BOUNDARY-LEAK] 5 标签）| 扩 |
| `~/.claude/projects/-Users-shengming-Documents-code-gocell/memory/feedback_recurring_patterns_strict_inscope.md`（新）| 升级既有 `feedback_pr_findings_default_inscope.md`：8 个反复模式之一被命中时**禁止 OUT_OF_SCOPE / P2 / follow-up**；列 8 模式清单 | memory 新建 |
| `~/.claude/projects/-Users-shengming-Documents-code-gocell/memory/feedback_metadata_declaration_integrity.md`（新）| Metadata 声明 ↔ 实现同步约束（slice.yaml allowedFiles ↔ find；contractUsages ↔ AddHandler）| memory 新建 |

**"先写大概，后续详细"原则**：
- 各层 CLAUDE.md 仅写骨架 + 反复模式守护**占位条款 1-2 行 + TODO 注释**
- 不在本 PR 完整迁移 `.claude/rules/gocell/*.md` 内容到层 CLAUDE.md（那是后续独立 plan 工作）
- review/fix skill 升级用最小增量（5-10 行新增），不重构整个 SKILL.md

**验收**：
- 6 个层 CLAUDE.md 文件存在且含反复模式占位段
- `golangci-lint run ./...` 0 issues（不影响代码）
- 新 memory 文件存在；`/clear` 后召回测试

---

## 阶段 4 — 6 角色 retrospective（~3h，⏳ 未开始）

**抽象**：阶段 1 + 2 累计 diff（含已迁出的 batch2 PR-CI-3 V1-RESPONSE-EVOLVE 完成后）整体走 6 角色并行 review，确认无新反复模式漏网。

**前置**：阶段 2 完成 + batch2 plan 全合并（PR-CI-1 已由 #321 关闭；剩 PR-CI-2/3/4/5/6）。

**6 角色矩阵**：架构 / 安全 / 测试 / 运维 / 可维护性 / 产品

**输出**：`docs/reviews/202604XXXX-recurring-patterns-retrospective.md`，包含：
- 8 条规则在生产代码上的真实拦截统计（修了多少处 fail）
- review skill 升级后下一个 PR 的实测效果（如时间允许，跑一个真 PR review 验证维度生效）
- 剩余技术债与下一轮治理建议

---

## 范围与依赖

### 范围内（本 plan 剩余）
- Claude 分层规则骨架（kernel/runtime/adapters/cells/pkg/contracts 各一 CLAUDE.md）
- review skill / fix skill / memory 升级
- 6 角色 retrospective

### 范围外
- 详细迁移 `docs/new-setting/` 到真实层 CLAUDE.md 实质内容（本 plan 只建骨架）— 留下一轮 plan
- v1 schema 演进 ADR — 已迁入 batch2 plan PR-CI-3
- 8 条 archtest 治理规则及其业务消化 — 已在阶段 1 完成（#293-#302 + #307）
- backlog1.md §2.4-2.7 的 PR-LIFECYCLE-ROBUSTNESS / PR-TEST-DEPTH / PR-API-CONSISTENCY / PR-CLI-CONSISTENCY — 既有节奏排期
- backlog1.md §3 既有 PR 扩充（PR-A53/A41/PR252-F2）
- 触发条件项 Wave 9/10（归 026 plan）

---

## 工时与排期

| 阶段 | PR 数 | 工时 | 状态 |
|---|---:|---:|---|
| 0 Baseline 同步 | — | 0.2h | ✅ |
| 1 CI 模式治理 8 PR + 6.1 收尾 | 9 | 68h | ✅ |
| 2 Claude 分层骨架 | 1 | 4h | ⏳ |
| 3 v1 Schema 演进 ADR | — | — | ⤴ 已迁 batch2 PR-CI-3 |
| 4 6 角色 retrospective | — | 3h | ⏳（依赖阶段 2 + batch2 全合）|

**剩余工时 ~7h**；单人 ~1 工作日。

---

## 验证矩阵（剩余阶段）

```bash
# 阶段 2 后
ls {kernel,runtime,adapters,cells,pkg,contracts}/CLAUDE.md   # 6 个文件存在
grep -l "反复模式\|Fail-Closed\|Panic 禁止" {kernel,runtime,adapters,cells,pkg,contracts}/CLAUDE.md
ls ~/.claude/projects/-Users-shengming-Documents-code-gocell/memory/feedback_recurring_patterns_strict_inscope.md

# 阶段 4 retrospective
ls docs/reviews/202604*-recurring-patterns-retrospective.md
```
