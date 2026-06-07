# PR 评论格式（`pm:ship` / `pm:fix` / `pm:pr-review` 模板单源）

> 何时贴 / 留痕约定 / 标记规则见 `PROJECT.md` §5；P + Cx 评级见 §3。
> **footer 必填**（每个模板末尾那行）：AI 自填 `<Claude Code|Codex>`、PR 号、head 分支、**worktree 路径**（当前工作目录；develop 直改填 `—`）、**session 会话id**（AI 想办法拿到，如 `$CLAUDE_CODE_SESSION_ID` / codex 等价；拿不到填 `—`）。
> **贴完回显 comment URL**：`gh pr comment` 的 stdout 返回评论 URL（含 `#issuecomment-<id>`，已实测），贴完必须捕获并回显给用户（命令形态单源见 `issues` Part B4）。

> **评论即 review 结果，无损（关键约定）**：评论是 `/fix <PR#>` 提取 findings 的**唯一来源**。
> 每条 Finding **必带 `file:line`**（fix 据此定位，不重新 review），根因 + 证据 + 建议 + 三级方案种子写进 `<details>`（人看摘要、fix 读详表，两不丢）。
> **OUT_OF_SCOPE finding 同享无损区，不准降级成计数 `OUT_OF_SCOPE <k>`**：每条 OOS 在 `<details>` 里带 file:line + 证据 + 三维根因（代码/架构/历史）+ 三级方案种子 + Files 列表 + 建 issue 命令草稿，使后续 `gh issue create`（body 按 `backlog.md` 字段映射：现状←证据+根因+影响 / 修复方向←方案种子 / Files←file:line 全集 / Source←`PR #<N> finding <Fk>`）能无损成文。
> **禁止有损浓缩**——只写计数 / 模糊一句话会让 fix 与后续建 issue 丢失定位与根因，违背本约定。

## 机器块（`gocell-pr-meta:v1`，隐藏，自动化执行器消费）

> 三模板 footer **之后**各带一行**隐藏机器块**，供 #935/#1657 本机执行器（Codex review daemon / Claude fix monitor）dispatch：
> `<!-- gocell-pr-meta:v1 <标准 base64(JSON)> -->`（CommonMark 隐藏，肉眼不可见）。
>
> - **产**（贴评论的技能/工具）：用 `jq -nc` 构造**事实** JSON（`kind`/`phase`/`verdict`/refs/`findings`/`cycle.round`，**不写 `next`**）`| bash hack/automation/pr-meta.sh emit` 得该行，**追加到填好的 body 末尾**再贴。`emit` 单源派生 `schema`/`cycle.exhausted`/`next`/`idempotencyKey`——手填无意义。
> - **消费**：`bash hack/automation/pr-meta.sh extract <PR#>` 拉评论 → 取最新块 → base64 解码 → schema 校验 → 比对 live `headSha`（不一致=过期，丢弃）。
> - **熔断（auto review↔fix ≤3 轮）**：`cycle.round` = 已完成 fix 轮数；`round ≥ maxRounds(3)` → `cycle.exhausted=true`，且 `changes-requested` 的 `next.agent` 被 helper 强制为 `human`——守护进程必停派、转人工，不得继续 auto 循环。
> - **标准 base64（非 url）**：CommonMark 禁 HTML 注释正文含 `--`；标准 base64 字母表 `A-Za-z0-9+/=` 无 `-`，结构上不可能产 `--`/`-->`（base64url 含 `-`，会破块）。
> - schema 单源 = `hack/automation/schema/pr-meta.v1.json`；helper = `hack/automation/pr-meta.sh`（`emit`/`decode`/`extract`/`round`/`selftest`）。**人读 footer 不动其格式**——footer 人读、机器块 dispatch，二者并存。
> - 各 kind 的 `phase`/`verdict` 取值见下方各模板末尾标注。

## ship 评论（`<!-- pm:ship -->`）

```markdown
<!-- pm:ship -->
## 🛠 ship review + fix

**reviewer** <数> · **Findings** <总数>（已修 Cx1/Cx2 <n> · 遗留 Cx3/Cx4 <m> · OUT_OF_SCOPE <k>）

- **F1** [P1·Cx2·安全] `path/to/file.go:120` — <一句话> → ✅ 已修
- **F2** [P2·Cx3·DX] `path/to/x.go:88` — <一句话> → ⏸ 遗留（需人工决策）
- **F3** [P2·Cx2·运维] `other/pkg/z.go:64` — <一句话> → 🚦 OUT_OF_SCOPE（issue 草稿见详表）

<details><summary>完整详表（根因 + 证据 + 建议 + 方案种子，/fix 与后续建 issue 读此）</summary>

**F1** [P1·Cx2·安全] `path/to/file.go:120`
- 证据：`<code 片段>`
- 建议：<彻底修复方向>
- 处置：✅ 已修（commit <sha>）

**F2** [P2·Cx3·DX] `path/to/x.go:88`
- 证据：`<code 片段>`
- 三级方案种子：最小 <…> / 彻底 <…> / 重构 <…>
- 处置：⏸ 遗留（原因：<…>）

**F3** [P2·Cx2·运维] `other/pkg/z.go:64`（🚦 OUT_OF_SCOPE，属 `other/` 子系统）
- 证据：`<code 片段>`
- 三维根因：代码 <…> / 架构 <1 处局部｜Grep N 处系统性> / 历史 <git log 同类>
- 三级方案种子：最小 <…> / 彻底 <…> / 重构 <…>
- 影响范围：直接 <…> / 间接 <…> / 同类 <Grep N 处>
- Files：`other/pkg/z.go:64` `other/pkg/w.go:30`
- → 建 issue 草稿（确认后跑）：`gh issue create --label backlog --label pri-p2 --label area-XX --label type-XX --title "[<ID>] <标题>" --body-file <backlog.md：现状←证据+根因+影响 / 修复方向←方案种子 / Files←上行 / Source←PR #<N> F3>`
</details>

**下一步**：切 `pr-status/needs-review-again`（待再审：codex / `/pr-review`）。

---
🤖 PR #<N> · Generated with <Claude Code|Codex> · branch <head 分支> · worktree <路径|—> · session <会话id|—>
<!-- 机器块占位：贴评论前由 hack/automation/pr-meta.sh emit 生成并追加到此处（kind=ship phase=ship verdict=needs-review-again round=0）；勿手填 base64 -->
```

## fix 评论（`<!-- pm:fix -->`，每次 fix 都贴）

```markdown
<!-- pm:fix -->
## 🔁 fix（findings triage + fix）

**Findings** <总数>（已修 Cx1/Cx2 <n> · 遗留 Cx3/Cx4 <m> · OUT_OF_SCOPE <k>）

- **F1** [P1·Cx2·安全] `path/to/file.go:120` — <一句话> → ✅ 已修
- **F2** [P2·Cx3·DX] `path/to/x.go:88` — <一句话> → ⏸ 遗留（需人工决策）
- **F3** [P2·Cx2·运维] `other/pkg/z.go:64` — <一句话> → 🚦 OUT_OF_SCOPE（issue 草稿见详表）

<details><summary>完整详表（triage 依据 + 证据 + 建议，下次 fix / 人工 / 建 issue 读此）</summary>

**F1** [P1·Cx2·安全] `path/to/file.go:120`（IN_SCOPE）
- 证据：`<code 片段>`
- 修复：<做了什么> → ✅ commit <sha>

**F2** [P2·Cx3·DX] `path/to/x.go:88`（IN_SCOPE，遗留）
- 三级方案种子：最小 <…> / 彻底 <…> / 重构 <…>
- 遗留原因 + 升级窗口：<…>

**F3** [P2·Cx2·运维] `other/pkg/z.go:64`（🚦 OUT_OF_SCOPE，属 `other/` 子系统）
- 证据：`<code 片段>`
- 三维根因：代码 <…> / 架构 <1 处局部｜Grep N 处系统性> / 历史 <git log 同类>
- 三级方案种子：最小 <…> / 彻底 <…> / 重构 <…>
- 影响范围：直接 <…> / 间接 <…> / 同类 <Grep N 处>
- Files：`other/pkg/z.go:64` `other/pkg/w.go:30`
- → 建 issue 草稿（确认后跑）：`gh issue create --label backlog --label pri-p2 --label area-XX --label type-XX --title "[<ID>] <标题>" --body-file <backlog.md：现状←证据+根因+影响 / 修复方向←方案种子 / Files←上行 / Source←PR #<N> F3，派生注 Discovered via /fix #<N>>`
</details>

**下一步**：切 `pr-status/needs-check-fix`（待 `/pr-review --check` 验证；fix 不直接到 ready）。

---
🤖 PR #<N> · Generated with <Claude Code|Codex> · branch <head 分支> · worktree <路径|—> · session <会话id|—>
<!-- 机器块占位：贴评论前由 hack/automation/pr-meta.sh emit 生成并追加到此处（kind=fix phase=fix verdict=needs-check-fix round=prev+1）；勿手填 base64 -->
```

## pr-review 评论（`<!-- pm:pr-review -->`，独立 review 留痕）

> 评论 = 阶段 5 的五块**完整**写入（不浓缩）：summary + 根因簇 + Finding 列表（带 file:line）+ 详表 details + 修复分流 + 结论。
> **`--check` 变体**（验证上一轮 findings，见 pr-review 模式 B）：Finding 列表每条用 `✅已修复 / ❌未修复 / ⚠️回归 / 🔧部分` 替代「→ 簇 C{m}」；summary 用 `已修复 N / 未修复 M / 回归 K / 部分 J`；详表 `<details>` 记每条验证证据；结论给流转建议（全 ✅ → ready / 有遗留 → 回 /fix）。

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
<!-- 机器块占位：贴评论前由 hack/automation/pr-meta.sh emit 生成并追加到此处（kind=pr-review phase=review|check verdict=approved|changes-requested|ready round=carry）；勿手填 base64 -->
```
