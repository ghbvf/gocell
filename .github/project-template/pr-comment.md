# PR 评论格式（`pm:ship` / `pm:fix` 模板单源）

> 何时贴 / 留痕约定 / 标记规则见 `PROJECT.md` §5；P + Cx 评级见 §3。

## ship 评论（`<!-- pm:ship -->`）

```markdown
<!-- pm:ship -->
## 🛠 ship review + fix

**Findings**：<总数>（已修 Cx1/Cx2 <n> · 遗留 Cx3/Cx4 <m> · OUT_OF_SCOPE <k>）
- **F1** [P1·Cx2] <一句话摘要> → ✅ 已修
- **F2** [P2·Cx3] <一句话摘要> → ⏸ 遗留（需人工决策）

**下一步**：切 `pr-status/needs-codex`（待 codex review）。
```

## fix 评论（`<!-- pm:fix -->`，每次 fix 都贴）

```markdown
<!-- pm:fix -->
## 🔁 fix（findings triage + fix）

**Findings**：<总数>（已修 Cx1/Cx2 <n> · 遗留 Cx3/Cx4 <m> · OUT_OF_SCOPE <k>）
- **F1** [P1·Cx2] <一句话摘要> → ✅ 已修
- **F2** [P2·Cx3] <一句话摘要> → ⏸ 遗留（需人工决策）

**下一步**：切 `pr-status/ready`（全清）或列 `pr-review/changes-requested` 遗留。
```
