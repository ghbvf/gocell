# PR 评论格式（`pm:ship` / `pm:fix` / `pm:pr-review` 模板单源）

> 何时贴 / 留痕约定 / 标记规则见 `PROJECT.md` §5；P + Cx 评级见 §3。
> **footer 必填**（每个模板末尾那行）：AI 自填 `<Claude Code|Codex>`、PR 号、head 分支、**worktree 路径**（当前工作目录；develop 直改填 `—`）、**session 会话id**（AI 想办法拿到，如 `$CLAUDE_CODE_SESSION_ID` / codex 等价；拿不到填 `—`）。

> **评论即 review 结果，无损（关键约定）**：评论是 `/fix <PR#>` 提取 findings 的**唯一来源**。
> 每条 Finding **必带 `file:line`**（fix 据此定位，不重新 review），根因 + 证据 + 建议 + 三级方案种子写进 `<details>`（人看摘要、fix 读详表，两不丢）。
> **禁止有损浓缩**——只写计数 / 模糊一句话会让 fix 丢失定位与根因，违背本约定。

## ship 评论（`<!-- pm:ship -->`）

```markdown
<!-- pm:ship -->
## 🛠 ship review + fix

**reviewer** <数> · **Findings** <总数>（已修 Cx1/Cx2 <n> · 遗留 Cx3/Cx4 <m> · OUT_OF_SCOPE <k>）

- **F1** [P1·Cx2·安全] `path/to/file.go:120` — <一句话> → ✅ 已修
- **F2** [P2·Cx3·DX] `path/to/x.go:88` — <一句话> → ⏸ 遗留（需人工决策）

<details><summary>完整详表（根因 + 证据 + 建议 + 方案种子，/fix 读此）</summary>

**F1** [P1·Cx2·安全] `path/to/file.go:120`
- 证据：`<code 片段>`
- 建议：<彻底修复方向>
- 处置：✅ 已修（commit <sha>）

**F2** [P2·Cx3·DX] `path/to/x.go:88`
- 证据：`<code 片段>`
- 三级方案种子：最小 <…> / 彻底 <…> / 重构 <…>
- 处置：⏸ 遗留（原因：<…>）
</details>

**下一步**：切 `pr-status/needs-codex`（待 codex review）。

---
🤖 PR #<N> · Generated with <Claude Code|Codex> · branch <head 分支> · worktree <路径|—> · session <会话id|—>
```

## fix 评论（`<!-- pm:fix -->`，每次 fix 都贴）

```markdown
<!-- pm:fix -->
## 🔁 fix（findings triage + fix）

**Findings** <总数>（已修 Cx1/Cx2 <n> · 遗留 Cx3/Cx4 <m> · OUT_OF_SCOPE <k>）

- **F1** [P1·Cx2·安全] `path/to/file.go:120` — <一句话> → ✅ 已修
- **F2** [P2·Cx3·DX] `path/to/x.go:88` — <一句话> → ⏸ 遗留（需人工决策）

<details><summary>完整详表（triage 依据 + 证据 + 建议，下次 fix / 人工读此）</summary>

**F1** [P1·Cx2·安全] `path/to/file.go:120`（IN_SCOPE）
- 证据：`<code 片段>`
- 修复：<做了什么> → ✅ commit <sha>

**F2** [P2·Cx3·DX] `path/to/x.go:88`（IN_SCOPE，遗留）
- 三级方案种子：最小 <…> / 彻底 <…> / 重构 <…>
- 遗留原因 + 升级窗口：<…>
</details>

**下一步**：切 `pr-status/ready`（全清）或列 `pr-review/changes-requested` 遗留。

---
🤖 PR #<N> · Generated with <Claude Code|Codex> · branch <head 分支> · worktree <路径|—> · session <会话id|—>
```

## pr-review 评论（`<!-- pm:pr-review -->`，独立 review 留痕）

> 评论 = 阶段 5 的五块**完整**写入（不浓缩）：summary + 根因簇 + Finding 列表（带 file:line）+ 详表 details + 修复分流 + 结论。

```markdown
<!-- pm:pr-review -->
## 🔍 pr-review（六维度分级审查）

**根因簇** <N> · **Findings** <M>（P0 <a>·P1 <b>·P2 <c>·P3 <d> ｜ Cx1 <w>·Cx2 <x>·Cx3 <y>·Cx4 <z>）· **结论** <通过/需修复/需讨论>

**根因簇**
- **C1** <根因一句>（维度 <…>；系统性 Grep <N> 处）→ F1,F3

**Findings**（每条带 file:line，/fix 无损提取）
- **F1** [P1·Cx2·安全] `path/to/file.go:120` — <一句话> → 簇 C1
- **F2** [P2·Cx3·DX] `path/to/x.go:88` — <一句话> → 簇 C1

<details><summary>完整详表（证据 + 建议 + 根因 + 方案种子，/fix 读此）</summary>

**F1** [P1·Cx2·安全] `path/to/file.go:120`（→ C1）
- 证据：`<code 片段>`
- 建议：<彻底修复方向>

**F2** [P2·Cx3·DX] `path/to/x.go:88`（→ C1）
- 证据：`<code 片段>`
- 三级方案种子：最小 <…> / 彻底 <…> / 重构 <…>
</details>

**修复分流**：Cx1/Cx2 → `/fix`；Cx3/Cx4 → 需人工决策（方案种子见详表）。<若 PR body 含 closing keyword 附 `← issue #<N>`>
**结论**：<一句话理由>

---
🤖 PR #<N> · Generated with <Claude Code|Codex> · branch <head 分支> · worktree <路径|—> · session <会话id|—>
```
