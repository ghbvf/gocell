---
name: stage-1-specify
description: "Speckit Specify: 创建分支+生成spec+验证需求完整性"
argument-hint: "[branch-name]"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Skill]
---

# 阶段 1: Speckit Specify

**执行者**: 总负责人触发

**入口条件**: S0 出口通过（phase-charter.md + role-roster.md + product-context.md 存在）

---

## 操作步骤

### 步骤 1: 创建并切换到 Feature Branch

```bash
git checkout -b feat/{number}-{short-name} develop
```

```bash
git push -u origin feat/{number}-{short-name}
```

**重要**:
- `{number}` 为全局递增编号，通过 `git branch --list 'feat/*' | wc -l` 推算下一个可用编号
- `{short-name}` 为简短描述（kebab-case）
- 只运行一次，不重复创建
- 创建后立即 push 到 remote，确保 S5 per-PR 能以此为 base 创建 PR

### 步骤 2: 执行 Speckit Specify

```
/speckit.specify {Phase 描述}
```

**关键**: 将 `specs/{branch}/product-context.md` 作为上下文输入传递给 speckit.specify。

speckit.specify 的输入必须包含:
- Phase 目标描述（来自 phase-charter.md）
- 产品上下文（来自 product-context.md 的 persona + 成功标准 + 范围边界）

### 步骤 3: 验证 spec.md 质量

检查生成的 `specs/{branch}/spec.md`，确认 FR（功能需求）中包含以下三类需求声明：

```
[ ] FR 中包含文档需求 — 例如"系统必须提供 API 文档"
[ ] FR 中包含 DevOps 需求 — 例如"系统必须更新 Docker/CI 配置"
[ ] FR 中包含测试需求 — 例如"系统必须提供 E2E 测试"或"Playwright"
```

如任一缺失，手动追加到 spec.md 的 FR 章节。

### 步骤 4: 确认 checklist 产出

确认 `specs/{branch}/checklists/requirements.md` 已由 speckit.specify 自动生成。

### 步骤 5: 阶段门检查

```bash
python3 .claude/skills/phase-gate/scripts/phase-gate-check.py --stage S1 --branch {branch} --check exit
```

phase-lint 会额外检查 spec.md 是否包含文档/DevOps/测试关键词（基于 phase-gates.yaml 的 content_checks 配置）。

---

## 硬性产出物

| 文件 | 路径 | 责任角色 |
|------|------|---------|
| spec.md | `specs/{branch}/spec.md` | 总负责人/Speckit |
| requirements.md | `specs/{branch}/checklists/requirements.md` | Speckit |

## 出口条件

```
[ ] spec.md 存在且非空
[ ] checklists/requirements.md 存在
[ ] spec.md FR 含文档需求声明
[ ] spec.md FR 含 DevOps 需求声明
[ ] spec.md FR 含测试/E2E/Playwright 需求声明
[ ] phase-gate-check.py --stage S1 --branch {branch} --check exit = PASS
```

**注**: product-context.md 已在 S0 出口保证存在，S1 不再重复检查。
