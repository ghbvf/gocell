---
name: issues
description: "GitHub Issues + Project v2 #3 项目管理单源技能。Part A：epic 拆解 + wave 实施顺序调度（找子任务 → blocked-by DAG → wave 1-4 滚动排序：OPEN 重排、已完成不动、超窗不入字段 → 写 Project Wave 字段 + 回填 epic body + 回评）。Part B：issue/PR 原子操作（建/改 backlog issue、area/type/pri label、PR 双轴状态 label 流转、统一 PR 评论格式 ship/fix 共用）。非 epic issue 号 → 查代码判状态（只判不修，建议 /fix 或 close）。当用户要整理 epic 排 wave、建/改 backlog issue、贴 label、切 PR 状态、给 PR 留评论、核一个 issue 是否还成立时使用。"
argument-hint: "<epic #N | #issue（非epic→状态核查）| create-issue | edit-labels | pr-status | comment> [...]"
allowed-tools: [Read, Grep, Bash, Agent, AskUserQuestion]
---

See .claude/skills/issues/SKILL.md

**贴 PR 评论时 footer 的 `Generated with` 填 `Codex`**（其余字段同 `.claude/skills/issues/SKILL.md` 的 Part B4 / `.github/project-template/pr-comment.md`）。
