<!--
Backlog issue body 模版 — 经 `gh issue create --body-file <填好的本文件>` 创建。
labels（`backlog` + `area-XX` + `type-XX` + `pri-pX` + `cx-X`）与 title `[<ID>] <简短标题>` 由建 issue 的一方用 `--label` / `--title` 显式给（非 epic backlog 四轴齐全，cx 必填、无 unknown sentinel）；取值见 PROJECT.md §2/§3。

由 review/fix finding（OUT_OF_SCOPE / 派生）成文时，**无损映射**自 finding 详表（`pr-comment.md` 的 `<details>`），不得一句话带过：
  现状     ← 证据代码片段 + 三维根因（代码/架构/历史）+ 影响范围（直接/间接/同类 Grep N 处）
  修复方向 ← 三级方案种子（最小 / 彻底 / 重构）
  Files    ← finding 的 file:line 全集（不止主命中行）
  Source   ← `PR #<N> finding <Fk>`（OOS 自动建单再加 `Discovered via /ship|/fix #<N>`）
-->

## 现状

<当前代码状态 / 已落地 / 已尝试方案；finding 成文时填：证据代码片段 + 三维根因 + 影响范围>

## 修复方向

<设计思路 / 替代方案 / 范围；finding 成文时填：三级方案种子（最小 / 彻底 / 重构）>

## Files

- path/to/file1.go:line
- path/to/file2.go:line

## Trigger

<仅条件延后型（贴 `flag-cond` label）填：触发条件文本——什么事件 / PR 发生后才动手>

## Source

<来源：PR #<N> finding <Fk> / review path / issue#；OOS 自动建单加 `Discovered via /ship|/fix #<N>`>
