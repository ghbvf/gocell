# PR 评论格式（`pm:ship` / `pm:fix` / `pm:pr-review` 模板单源）

> 何时贴 / 留痕约定 / 标记规则见 `PROJECT.md` §5；P + Cx 评级见 §3。
> **footer 必填**（每个模板末尾那行）：AI 按当前身份自填 `<Claude Code|Codex>`、PR 号、head 分支（不跑 shell/env）。
> 贴完后 `gh pr comment` 原生返回新评论 URL（含 `#issuecomment-<id>`）——技能把该 URL 回显到当前窗口，作为权威留痕锚点。

## ship 评论（`<!-- pm:ship -->`）

```markdown
<!-- pm:ship -->
## 🛠 ship review + fix

**Findings**：<总数>（已修 Cx1/Cx2 <n> · 遗留 Cx3/Cx4 <m> · OUT_OF_SCOPE <k>）
- **F1** [P1·Cx2] <一句话摘要> → ✅ 已修
- **F2** [P2·Cx3] <一句话摘要> → ⏸ 遗留（需人工决策）

**下一步**：切 `pr-status/needs-codex`（待 codex review）。

---
🤖 PR #<N> · Generated with <Claude Code|Codex> · branch <head 分支>
```

## fix 评论（`<!-- pm:fix -->`，每次 fix 都贴）

```markdown
<!-- pm:fix -->
## 🔁 fix（findings triage + fix）

**Findings**：<总数>（已修 Cx1/Cx2 <n> · 遗留 Cx3/Cx4 <m> · OUT_OF_SCOPE <k>）
- **F1** [P1·Cx2] <一句话摘要> → ✅ 已修
- **F2** [P2·Cx3] <一句话摘要> → ⏸ 遗留（需人工决策）

**下一步**：切 `pr-status/ready`（全清）或列 `pr-review/changes-requested` 遗留。

---
🤖 PR #<N> · Generated with <Claude Code|Codex> · branch <head 分支>
```

## pr-review 评论（`<!-- pm:pr-review -->`，独立 review 留痕）

```markdown
<!-- pm:pr-review -->
## 🔍 pr-review（六维度分级审查）

**根因簇**：<簇数> · **Findings**：<总数>（P0 <a> · P1 <b> · P2 <c> · P3 <d>；Cx1 <w>/Cx2 <x>/Cx3 <y>/Cx4 <z>）
- **C1** <根因一句>（维度 <…>，系统性 <Grep N 处>）→ F1,F3
- **F1** [P1·Cx2] <一句话摘要>
- **F2** [P2·Cx3] <一句话摘要>

**修复分流**：Cx1/Cx2 → `/fix`；Cx3/Cx4 → 需人工决策（最小/彻底/重构三案种子）。
**总体结论**：通过 / 需修复 / 需讨论 + 一句话理由。

---
🤖 PR #<N> · Generated with <Claude Code|Codex> · branch <head 分支>
```
