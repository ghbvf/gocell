# v4.2 Workflow 适配修改计划

## 决策记录

| 决策 | 结论 |
|------|------|
| 产物改名 | harness-constraints.md -> kernel-constraints.md, harness-review-report.md -> kernel-review-report.md |
| 角色改名 | Harness 专家 -> Kernel Guardian |
| architect agent | 新建(S2 架构审查 + S6 裁决升级) |
| roadmap agent | 新建(S2 范围审查 + S8 roadmap 回灌) |
| GoCell 概念 | 本轮补齐 |

---

## 执行顺序(5 波)

```
Wave 1: 路径基础设施 ─── 脚本/YAML 路径修正
   |
   v
Wave 2: 全局重命名 ───── 机械替换, 24 个文件, ~150 处
   |
   v
Wave 3: 新建 Agent ───── architect.md + roadmap.md (与 Wave 4 可并行)
   |
   v
Wave 4: 门禁对齐 + 逻辑修复 ── user-signoff 条件化, reviewer 单一来源
   |
   v
Wave 5: GoCell 概念补齐 ──── assembly/status-board/journey/L3-L4/CLI/contract版本/元数据
```

---

## Wave 1: 路径基础设施 (3 个文件, 5 处)

### 1.1 `.claude/skills/phase-gate/scripts/bash/phase-gate-check.sh`

| 行 | 找到 | 替换为 |
|----|------|--------|
| ~10 | `REPO_ROOT/.specify/phase-gates.yaml` | `REPO_ROOT/.claude/skills/phase-gate/phase-gates.yaml` |
| ~9 | SCRIPT_DIR 上溯 3 级到 repo root | 上溯 5 级(scripts/bash -> phase-gate -> skills -> .claude -> v4.2) |

### 1.2 `.claude/skills/phase-gate/phase-gates.yaml`

| 行 | 找到 | 替换为 |
|----|------|--------|
| 2 | `消费者: .specify/scripts/bash/phase-gate-check.sh` | `消费者: .claude/skills/phase-gate/scripts/bash/phase-gate-check.sh` |

### 1.3 `.claude/skills/phase-gate/SKILL.md`

| 处 | 找到 | 替换为 |
|----|------|--------|
| ~8处 | `.specify/scripts/bash/phase-gate-check.sh` | `.claude/skills/phase-gate/scripts/bash/phase-gate-check.sh` |
| ~2处 | `.specify/phase-gates.yaml` | `.claude/skills/phase-gate/phase-gates.yaml` |

---

## Wave 2: 全局重命名 (24 个文件, ~150 处)

每组为一次 replace_all 操作, 按顺序执行。

### 2-A: 产物文件名

| 找到 | 替换为 | 影响文件数 | 处数 |
|------|--------|-----------|------|
| `harness-constraints.md` | `kernel-constraints.md` | 13 | ~51 |
| `harness-review-report.md` | `kernel-review-report.md` | 8 | ~23 |

### 2-B: 角色名 + Agent 名

| 找到 | 替换为 | 影响文件数 | 处数 |
|------|--------|-----------|------|
| `Harness 专家` | `Kernel Guardian` | 9 | ~31 |
| `Agent(name=harness-expert)` | `Agent(name=kernel-guardian)` | 1 | 1 |
| `harness-expert` (剩余引用) | `kernel-guardian` | 2 | ~3 |

### 2-C: 路径替换

| 找到 | 替换为 | 影响文件数 | 处数 |
|------|--------|-----------|------|
| `.specify/scripts/bash/phase-gate-check.sh` | `.claude/skills/phase-gate/scripts/bash/phase-gate-check.sh` | 12 | ~12 |
| `.specify/scripts/bash/create-new-feature.sh` | `.claude/skills/phase-gate/scripts/bash/create-new-feature.sh` | 2 | ~2 |
| `.specify/phase-gates.yaml` | `.claude/skills/phase-gate/phase-gates.yaml` | 3 | ~4 |
| `controlplane/ui` | `examples/{project}/ui` | 4 | ~4 |
| `src-v2/` | (按上下文改为 GoCell 实际路径) | 5 | ~5 |

### 2-D: 源项目泄漏清理

| 文件 | 找到 | 替换为 |
|------|------|--------|
| CLAUDE.md:20 | `selfloop_l1 schema, 7 existing migrations, 8 tables` | 删除或改为 GoCell 实际状态 |
| CLAUDE.md:21 | `Vue 3 + Vite + TypeScript (controlplane/ui)` | `Vue 3 + Vite + TypeScript (examples/ 示例项目, 条件启用)` |
| CLAUDE.md:26 | `002-self-loop-engine: Added Go 1.22+...` | 删除整行 |
| workflow-detailed.md | `SLE 内核集成` (3处) | `GoCell 内核集成` |
| workflow-detailed.md | `sle serve` (2处) | `GoCell example app` |
| devops.md | `SLE serve` (2处) | `GoCell App` |
| stage-2-review | `SLE 内核` (2处) | `GoCell 内核` |
| stage-4-plan | `sle serve` (1处) | `GoCell example app` |
| stage-7-qa | `sle serve` (1处) | `GoCell example app` |

### 2-E: PRD 标准化

| 找到 | 替换为 | 文件 |
|------|--------|------|
| `PRD v2` | `PRD(产品需求文档)` | workflow-detailed.md, stage-0, stage-2 |
| `PRD V4` | `PRD` | workflow-detailed.md |

---

## Wave 3: 新建 Agent (2 个新文件)

### 3.1 `.claude/agents/architect.md` (新建)

```
name: architect
description: 架构师 - GoCell 分层架构审查、接口稳定性评审、S6 裁决升级
```

职责:
- S2: 审查 spec.md, 从 GoCell 分层架构角度给出 5-10 条建议, 产出 review-architect.md
- S6: P0 经 3 轮未解决时升级到架构师裁决
- S8: 架构评审维度参与

GoCell 专属检查:
- kernel/ 接口向后兼容性
- Cell 聚合边界合理性
- 分层依赖方向(kernel -> cells -> runtime -> adapters)
- 新增公共 API 的稳定性评估

### 3.2 `.claude/agents/roadmap.md` (新建)

```
name: roadmap
description: Roadmap 规划师 - PRD 对齐审查、范围控制、Phase 间依赖分析
```

职责:
- S2: 审查 spec.md, 从范围/PRD 对齐角度给出 5-10 条建议, 产出 review-roadmap.md
- S8: Phase 结果回灌到下一 Phase 计划

GoCell 专属检查:
- 框架版本策略(semver 兼容窗口)
- Cell/Slice 交付优先级
- 跨 Phase 的 API 稳定性承诺

---

## Wave 4: 门禁对齐 + 逻辑修复 (6 个文件)

### 4.1 phase-gates.yaml

| 阶段 | 修改 |
|------|------|
| S2 exit | 已有 review-architect.md + review-roadmap.md, 验证OK |
| S7 exit | 确认 user-signoff.md 不在 required_files(已正确) |
| S8 entry | 确认无 user-signoff.md 硬性要求(已正确) |
| 全部 | harness-* 改名(Wave 2 覆盖) |

### 4.2 workflow-detailed.md — user-signoff 条件化

| 位置 | 修改 |
|------|------|
| S7 出口条件 | 加注: `user-signoff.md 仅当 role-roster.md 前端开发者=ON 时必须` |
| S8.0 入口检查 | `user-signoff.md 存在` 加 `(仅当前端开发者=ON)` |
| S8.3-A | `user-signoff.md 判定非 REJECT` 加 `(如适用)` |
| S8.3-B | `user-signoff.md 三视角完整` 加 `(如适用)` |

### 4.3 stage-7-qa/SKILL.md

| 修改 |
|------|
| 产出物表: user-signoff.md 标注 `(条件: 前端开发者=ON)` |
| 出口条件: user-signoff 相关检查加条件前缀 |

### 4.4 stage-8-close/SKILL.md

| 修改 |
|------|
| S8.3-B checklist 中 user-signoff 相关项加 `(如适用)` |

### 4.5 phase-gate/SKILL.md

| 修改 |
|------|
| S7 速查表: `qa-report.md, user-signoff.md` 改为 `qa-report.md (+ user-signoff.md 条件)` |

### 4.6 stage-6-review-fix/SKILL.md

| 修改 |
|------|
| 6 席定义处加注: `权威定义见 .claude/agents/reviewer.md` |

---

## Wave 5: GoCell 概念补齐 (7 个主题, 20 处修改)

### 5-A: Assembly (assembly.yaml)

| 文件 | 位置 | 添加内容 |
|------|------|---------|
| kernel-guardian.md | S2 约束清单 | `- [ ] Assembly: assembly.yaml 列出所有 Cell 并声明组装顺序(如涉及多 Cell)` |
| kernel-guardian.md | S4 任务检查 | `- [ ] assembly.yaml 包含新增 Cell(如有)` |
| phase-gates.yaml | S4 content_checks | 补充 pattern `assembly.yaml`(可选) |

### 5-B: Status Board (status-board.yaml)

| 文件 | 位置 | 添加内容 |
|------|------|---------|
| product-manager.md | S0 模板 | 引用 journeys/status-board.yaml 做 Phase 状态跟踪 |
| project-manager.md | S5.4 | 读取 status-board.yaml 辅助进度判断 |

### 5-C: Journey Catalog (journeys/catalog.yaml)

| 文件 | 位置 | 添加内容 |
|------|------|---------|
| product-manager.md | S0 | Success Criteria 引用 journey catalog |
| product-manager.md | S4 | AC 验证引用 journey catalog 确保用户场景覆盖 |

### 5-D: L3/L4 测试策略

| 文件 | 位置 | 添加内容 |
|------|------|---------|
| go-standards.md | L0-L4 表之后 | 新增每级测试要求: L3=event replay+幂等, L4=状态机转换+超时重试 |
| reviewer.md | 席位 3 | 加: `L3/L4 操作是否有对应一致性测试` |

### 5-E: CLI 工具 (gocell scaffold/generate/check/verify)

| 文件 | 位置 | 添加内容 |
|------|------|---------|
| stage-4-plan/SKILL.md | 任务完整性检查 | `[ ] 如涉及新 Cell/Slice: tasks.md 包含 gocell scaffold 任务` |
| stage-5-implement/SKILL.md | 开发者要求 | `使用 gocell scaffold 生成骨架, gocell generate 生成代码` |
| kernel-guardian.md | S4 | `[ ] 新增 Cell/Slice 使用 gocell scaffold(不手写骨架)` |

### 5-F: Contract 版本化

| 文件 | 位置 | 添加内容 |
|------|------|---------|
| kernel-guardian.md | S2 约束清单 | `- [ ] 契约版本: 跨 Cell contract 变更遵循版本兼容规则(minor=向后兼容, major=breaking)` |
| reviewer.md | 席位 1 | `- 跨 Cell contract 变更是否遵循版本语义` |

### 5-G: cell.yaml / slice.yaml 字段级指导

| 文件 | 位置 | 添加内容 |
|------|------|---------|
| kernel-guardian.md | S2 元数据合规 | cell.yaml 必须字段: cellId/type/consistencyLevel/owner/ownedSlices/contracts/verify |
| kernel-guardian.md | S2 元数据合规 | slice.yaml 必须字段: sliceId/belongsToCell/consistencyLevel/journeys/verify/allowedFiles |
| go-standards.md | 新增 section | `## GoCell 元数据文件规范` 引用 docs/architecture/metadata-schemas.md |

---

## 统计

| 波次 | 文件数 | MUST | NICE | 估算工时 |
|------|--------|------|------|---------|
| Wave 1: 路径基础 | 3 | 5 | 0 | 10min |
| Wave 2: 全局重命名 | 24 | ~140 | ~8 | 30min |
| Wave 3: 新建 Agent | 2 (新建) | 2 | 0 | 15min |
| Wave 4: 门禁+逻辑 | 6 | 10 | 2 | 15min |
| Wave 5: GoCell 概念 | 8 | 13 | 7 | 20min |
| **合计** | **24+2** | **~170** | **~17** | **~90min** |
