---
name: stage-3-decide
description: "综合裁决: 逐条处理审查建议+更新spec+记录决策"
argument-hint: "[branch-name]"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Skill]
---

# 阶段 3: 综合裁决 + 更新 Spec + 记录决策

**执行者**: 总负责人

**入口条件**: S2 出口通过（kernel-constraints.md + review-architect.md + review-roadmap.md + review-product-manager.md 存在）

---

## 操作步骤

### 步骤 1: 指令重注入

在开始裁决前，重述以下上下文以防止 Agent Drift：

1. **Phase 目标**（来自 phase-charter.md）
2. **kernel-constraints.md 中的关键约束**（特别是"必须验证"清单）
3. **product-context.md 中的范围边界**

将以上内容作为裁决基准，贯穿整个步骤 2。

### 步骤 2: 逐条审查 4 方建议

读取以下 4 份审查意见：
- `specs/{branch}/review-architect.md`
- `specs/{branch}/review-roadmap.md`
- `specs/{branch}/kernel-constraints.md`（建议部分）
- `specs/{branch}/review-product-manager.md`

对每条建议做裁决，标记为以下之一：

| 裁决 | 含义 | 后续动作 |
|------|------|---------|
| **采纳** | 建议合理，编码回 spec | 步骤 3 通过 speckit.clarify 更新 |
| **拒绝** | 建议不适用于当前范围 | 记录拒绝理由到 decisions.md |
| **延迟** | 建议合理但不在本 Phase 执行 | 记录到 decisions.md 延迟列表 |

特别关注: 对 `kernel-constraints.md` 中每条建议逐一标注 accept/reject/defer。

### 步骤 3: 使用 speckit.clarify 更新 spec

将所有"采纳"的建议通过 `/speckit.clarify` 编码回 spec.md。

```
/speckit.clarify {采纳的建议列表}
```

**禁止**: 手动修改 spec.md。所有变更必须通过 speckit.clarify 执行。

### 步骤 4: 写 decisions.md

产出 `specs/{branch}/decisions.md`，使用 ADR（Architecture Decision Record）格式：

```markdown
# Decisions — Phase {N}: {名称}

## 裁决日期
{日期}

## 审查来源
- 架构师: review-architect.md ({N} 条建议)
- Roadmap 规划师: review-roadmap.md ({N} 条建议)
- Kernel Guardian: kernel-constraints.md ({N} 条建议)
- 产品经理: ({N} 条建议)

## 重要决策

### 决策 1: {标题}
- **决策**: {具体内容}
- **理由**: {为什么做这个决策}
- **被否决的替代方案**: {曾考虑但否决的方案 + 否决理由}

### 决策 2: ...

## Kernel Guardian 约束裁决
| 约束项 | 裁决 | 理由 |
|--------|------|------|
| {约束 1} | accept/reject/defer | {理由} |
| ... | ... | ... |

## 延迟到后续 Phase 的项目
| 项目 | 来源 | 延迟理由 | 计划 Phase |
|------|------|---------|-----------|
| ... | review-architect.md | ... | Phase N+1 |

## 被拒绝的建议
| 建议 | 来源 | 拒绝理由 |
|------|------|---------|
| ... | review-roadmap.md | ... |
```

### 步骤 5: 阶段门检查

```bash
python3 .claude/skills/phase-gate/scripts/phase-gate-check.py --stage S3 --branch {branch} --check exit
```

---

## 硬性产出物

| 文件 | 路径 | 责任角色 |
|------|------|---------|
| spec.md（更新） | `specs/{branch}/spec.md` | 总负责人（via speckit.clarify） |
| decisions.md | `specs/{branch}/decisions.md` | 总负责人 |

## 出口条件

```
[ ] spec.md 已通过 speckit.clarify 更新（非手动修改） [AGENT]
[ ] decisions.md 已写且含 ADR 格式 [GATE]
[ ] decisions.md 含 Kernel Guardian 约束裁决表（每条均标注 accept/reject/defer） [GATE]
[ ] decisions.md 含延迟项列表 [AGENT]
[ ] phase-gate-check.py --stage S3 --branch {branch} --check exit = PASS
```
