---
name: issues
description: "GitHub Issues + Project v2 #3 项目管理单源技能。Part A：epic 拆解 + wave 实施顺序评论（找子任务 → blocked-by DAG → wave 1-4 容量装箱排序：每 wave ≤4、pri 优先、wave 内切并行组/串行链、OPEN 重排、已完成与超窗单列 → 只追加 epic 评论；不写 Project 字段、不改 epic body）。Part B：issue/PR 原子操作（建/改 backlog issue、area/type/pri label、PR 双轴状态 label 流转、统一 PR 评论格式 + 冲突预检/CI watch 跟进，ship/fix 共用）。非 epic issue 号 → 查代码判状态（只判不修，建议 /ship 或 close）。当用户要整理 epic 排 wave、建/改 backlog issue、贴 label、切 PR 状态、给 PR 留评论、核一个 issue 是否还成立时使用。"
argument-hint: "<epic #N | #issue（非epic→状态核查）| create-issue | edit-labels | pr-status | comment> [...]"
allowed-tools: [Read, Grep, Bash, Agent, AskUserQuestion]
---

See .claude/skills/issues/SKILL.md

**贴 PR 评论时 footer 的 `Generated with` 填 `Codex`**（其余字段同 `.claude/skills/issues/SKILL.md` 的 Part B4 / `.github/project-template/pr-comment.md`）。
