---
name: stage-2-review
description: "4角色并行审查spec: 架构+范围+内核+产品"
argument-hint: "[branch-name]"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Agent]
---

# 阶段 2: 4 角色并行审查 spec

**执行者**: 总负责人派发 4 个 Agent

**入口条件**: S1 出口通过（spec.md + checklists/requirements.md 存在）

---

## 操作步骤

### 步骤 1: 并行派发 4 个审查 Agent

同时派发以下 4 个 agent（使用各 agent 定义文件中声明的工具权限）：

**Agent 1 — 架构师**:
```
角色: 架构师
任务: 审查 specs/{branch}/spec.md，从技术架构角度给出 5-10 条修改建议。
审查焦点:
- DDD 分层合理性
- 聚合边界划分
- 模块耦合风险
- 性能/可扩展性隐患
- 与现有架构的兼容性
输入: specs/{branch}/spec.md + specs/{branch}/product-context.md
产出: specs/{branch}/review-architect.md
```

**Agent 2 — Roadmap 规划师**:
```
角色: Roadmap 规划师
任务: 审查 specs/{branch}/spec.md，从范围/PRD 对齐角度给出 5-10 条修改建议。
审查焦点:
- 与 PRD 的对齐度
- 范围蔓延风险
- Phase 间依赖关系
- 优先级合理性
- 后续 Phase 的影响预评估
输入: specs/{branch}/spec.md + PRD + roadmap plan
产出: specs/{branch}/review-roadmap.md
```

**Agent 3 — Kernel Guardian**:
```
角色: Kernel Guardian
任务: 审查 specs/{branch}/spec.md，产出结构化报告 specs/{branch}/kernel-constraints.md。
报告必须包含:
(a) 从 GoCell 内核集成角度的 5-10 条修改建议
(b) 集成风险评估（高/中/低）
(c) 本 Phase 必须验证的内核约束清单
(d) 工作流可执行性评估 — spec 能否走完 9 阶段(S0-S8)？哪里可能卡住？
输入: specs/{branch}/spec.md + specs/{branch}/product-context.md + GoCell 内核代码
产出: specs/{branch}/kernel-constraints.md
```

**Agent 4 — 产品经理**:
```
角色: 产品经理
任务: 读取 spec.md + product-context.md，从验收标准/用户故事角度给出 5-10 条修改建议。
每条建议必须标注类别标签:
- [验收标准缺失] — spec 中某功能缺少可验证的 AC
- [开发者体验] — API 设计不直觉、godoc 不清晰、错误信息不友好
- [范围偏移] — 与 product-context.md 定义的范围不符
- [兼容性风险] — 可能破坏现有 API 的向后兼容性
输入: specs/{branch}/spec.md + specs/{branch}/product-context.md
产出: specs/{branch}/review-product-manager.md
```

### 步骤 2: 等待全部返回

**阻塞**: 必须等待全部 4 个 agent 返回，不可提前进入下一步。逐一确认每个 agent 的产出物已生成。

### 步骤 3: 汇总审查意见

确认以下文件均已产出：
- `specs/{branch}/review-architect.md`
- `specs/{branch}/review-roadmap.md`
- `specs/{branch}/kernel-constraints.md`
- 产品经理审查意见

### 步骤 4: 阶段门检查

```bash
python3 .claude/skills/phase-gate/scripts/phase-gate-check.py --stage S2 --branch {branch} --check exit
```

---

## 硬性产出物

| 文件 | 路径 | 责任角色 |
|------|------|---------|
| review-architect.md | `specs/{branch}/review-architect.md` | 架构师 |
| review-roadmap.md | `specs/{branch}/review-roadmap.md` | Roadmap 规划师 |
| kernel-constraints.md | `specs/{branch}/kernel-constraints.md` | Kernel Guardian |
| review-product-manager.md | `specs/{branch}/review-product-manager.md` | 产品经理 |

## 出口条件

```
[ ] 4 个 agent 全部返回（逐一确认）
[ ] review-architect.md 已产出且非空
[ ] review-roadmap.md 已产出且非空
[ ] kernel-constraints.md 已产出且含 (a)(b)(c)(d) 四部分
[ ] 产品经理审查意见已产出
[ ] phase-gate-check.py --stage S2 --branch {branch} --check exit = PASS
```
