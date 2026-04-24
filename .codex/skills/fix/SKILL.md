---
name: fix
description: "问题诊断与修复: 验证+根因+复杂度分级+修复方案+backlog登记。当用户说'这个问题存在吗''帮我分析这个bug''诊断一下这个模块''修复这个问题'时触发。支持单条问题和多方审查报告批量输入。"
argument-hint: "<问题描述|文件:行号|review报告路径>"
allowed-tools: [Read, Write, Edit, Glob, Grep, Bash, Agent, AskUserQuestion]
---

See .claude/skills/fix/SKILL.md