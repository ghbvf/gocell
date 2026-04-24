---
name: ship
description: "全流程实施：探索→计划→worktree→TDD→实施→PR→review→/fix Cx1/Cx2→人工确认。L1(跳过探索,1 reviewer)/L2(单agent探索,1 reviewer)/L3(默认,三agent探索,1 reviewer)/L4(三agent探索+6角色review)"
argument-hint: "[--level=L1|L2|L3|L4] <backlog-id 或任务描述>"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Agent, AskUserQuestion]
---

See .claude/skills/ship/SKILL.md