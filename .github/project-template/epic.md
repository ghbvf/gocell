<!--
Epic issue body 模版 — 经 `gh issue create --body-file <填好的本文件>` 创建。
labels（`epic` + `backlog` + `area-XX` + `pri-pX`）与 title `[EPIC] <能力级标题>` 由建 issue 的一方用 `--label` / `--title` 显式给。
子任务用 GitHub 原生 sub-issue 关联（不在 body 手写 task list）。
-->

## 目标 / 范围

<这个 epic 要达成什么能力级结果，边界在哪>

## 验收标准

- [ ] <所有子任务 close + 何种端到端能力可用>

## 实施顺序

<!-- Wave 字段派生视图，自动重生成；初次可留空。滚动：仅列 OPEN 的 Wave 1-4，已完成不列，超窗(>W4)单列 -->

Wave 1: #aaa, #bbb
Wave 2: #ccc（blocked-by #aaa）
超窗(>W4，未入字段): #fff
