---
name: pr-review
description: "对指定 PR 跑一次自动分级六维度 review。按 diff 净增删行数自动分配 1/2/3/6 reviewer agent 并行；主 agent 做根因聚类 + Cx 分级 + 修复分流建议，不自动 fix。"
argument-hint: "<PR 编号>"
allowed-tools: [Read, Glob, Grep, Bash, Agent]
disable-model-invocation: true
---

See .claude/skills/pr-review/SKILL.md

**完成后对根因进行开源对标，给出修复方向**