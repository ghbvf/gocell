# Backlog Re-rating Rubric

> 单源评估规则。`docs/backlog.md` + `docs/backlog/cap-*.md` 全量重评级时遵循本文件；评级口径漂移以本文件最新版本为准。
>
> 适用：2026-05-19 启动的全量 re-rating（约 250 条 OPEN，分 5 阶段执行）。
>
> 关联 charter：`.claude/rules/gocell/ai-robust.md`（AI-robust 三档分级 + Funnel 双向锁评级）。本 rubric 不与 charter 冲突，仅补充 backlog item 维度的 P/Cx/Flag 重评流程。

---

## 1. 三轴评估

每条 item 必须按下列顺序走完 3 步，不允许跳过完成性核验直接调 P/Cx：

| 步骤 | 轴 | 核心问题 | 决定 |
|---|---|---|---|
| **Step 1** | 完成性 | item 是否仍 OPEN？描述前提是否仍成立？ | DONE / STALE / DUP / OPEN |
| **Step 2** | Cx 复杂度 | 实施一次需要改动的文件域 + 类型加载需求 | Cx1 / Cx2 / Cx3 / Cx4 |
| **Step 3** | P 严重程度 | 不修后果 + 架构/去重/抽象命中 | P0 / P1 / P2 / P3 |

---

## 2. Step 1 — 完成性核验

### 必做的代码核实

不允许仅看 backlog 描述就判定状态。每条 item 必须至少做一项代码核实：

1. **Files 列对应路径**：用 `Read` 打开主要文件，定位描述中点名的符号/段落
2. **描述中的 grep 锚点**：把描述里的函数名/类型名/常量名/魔法字符串拿去 `grep -rn`
3. **PR Source 列**：若来源是 PR，对照 PR diff 看是否已 merge 落地（`git log --oneline --grep` 或 `gh pr view`）
4. **archtest 名字**：描述里的 archtest 名（如 `OUTBOX-LEASE-ID-CAS-01`）必须能在 `tools/archtest/` 找到，否则 item 描述本身 stale

### 状态判定

| 判定 | 触发条件 | 处理 |
|---|---|---|
| **DONE** | 描述中"修复方向"已在代码落地，archtest/test 已守 | Flag 改 ✅，候选下次归档批次（不在本 PR 删行）|
| **STALE** | 前提失效（描述中"现状"已不成立）但残留问题不同 | **同 PR re-scope**：改写描述 + 调 P/Cx；不删行 |
| **STALE-CLOSE** | 前提失效 + 无残留问题 | Flag 改 ✅ 备注 STALE，候选归档 |
| **DUP** | 与其他 item 完全/接近重叠 | 标 `(DUP of <ID>)`，并入目标 item 描述；本行下次归档 |
| **OPEN** | 描述前提成立，仍待实施 | 进 Step 2/3 重评 |

### STALE 范本

> 参考 `C-09` 2026-05-19 re-scope：`cell_routes.go` / `RegisterSubscriptions` 前提已失效，但 auditcore `cell.go` 17 func 未拆 + accesscore lifecycle 注册错位是真残留问题——同 PR 改写描述 + Flag/Trigger 调整为"并入 C-04"。

---

## 3. Step 2 — Cx 复杂度

按"实施一次需要改动的文件域 + 类型加载需求"分级。**改动估算以 PR diff 为单位**，不是工时。

| 级 | 文件域 | 类型加载 | 跨域 | 典型 |
|---|---|---|---|---|
| **Cx1** | 单文件 / 同文件 ≤ 3 处 | 不需要 typeseval | 单包 | 改字面量、补 godoc、加单元测试 |
| **Cx2** | 同包 ≤ 5 文件 | 可能需要 archtest typed | 单包或单 cell | 加新方法、抽 helper、补 archtest 单条 |
| **Cx3** | 跨包 5–15 文件 | 需要 archtest typed / typeseval | 1 个 kernel 子系统 或 ≤ 2 cell | 接口扩字段 + 多实现同步、funnel 双向锁、ADR amendment |
| **Cx4** | ≥ 15 文件 / ≥ 3 cap | 需要跨包 type info + reflect/codegen | 跨 kernel/runtime/cells/adapters | 接口 ctx 透传、cell 接口重构、codegen 链路改造 |

### Cx 升降信号

- **升 Cx**：需要新建 codegen 模板 / 需要 ADR / 需要 conformance test 跑遍多个实现 / 需要 schema 迁移
- **降 Cx**：已存在 archtest 模板可套用 / 已存在 helper 可复用 / 只改 godoc

### 不允许的 Cx

- `Cx5+`：拆为多个 item / 多个 Wave
- 缺 Cx（如旧表 `Cx—`）：必须补出来；填不出来即说明 item 描述不够具体，先在 Step 1 标 STALE

---

## 4. Step 3 — P 严重程度

### 基础档位

| 级 | 含义 | 用法 |
|---|---|---|
| **P0** | 发布阻塞 / 数据丢失 / 安全漏洞 / 编译失败 | 不在本次重评新增；保留给 incident-driven 紧急条目 |
| **P1** | 架构/安全/正确性关键 + 抽象/去重/funnel 闭环关键 | 架构 refactor 的上限 |
| **P2** | 常规债务、影响维护性但不阻塞功能 | 默认档 |
| **P3** | 触发型 / 可延后 / 性能微调 / 文档完善 | |

> **P0 红线**：架构 refactor 即使跨 ≥ 3 cap 也最高顶 P1。只有"线上故障 / 数据完整性破坏 / 安全 CVE"才进 P0，且需在描述里写 incident ID 或 CVE 编号。

### 架构 / 去重 / 抽象命中信号（满足任一即命中）

| 维度 | 命中条件 |
|---|---|
| Type | `arch-opt` / `refactor` / `debt` 且描述含 *统一 / 合并 / 拆分 / 抽象 / decompose / normalize / converge / unify / dedup / single source / funnel / sealed / Hard 升级* |
| Files | 触及 `kernel/` 多包 / `tools/archtest/` typed funnel / ≥ 3 cell |
| AI-robust | Soft → Hard / Medium 上游升 Hard / Funnel 双向锁未闭合（charter §"Funnel 双向锁评级"）|
| 跨域 | 影响 ≥ 3 个 cap |
| Charter mandated | charter 已 mandate 显式登记（如 funnel godoc 中已点名）|

### 升级规则（命中后）

| 当前 P | 升至 |
|---|---|
| P3 | **P2** |
| P2 | **P1** |
| P1 | 维持（架构 refactor 顶到 P1，不再上调）|

### 反向降级规则（避免重评放水）

| 信号 | 降级 |
|---|---|
| 纯 feat / bug 触发型，且无业务方推动 + P2 | 降 **P3** |
| 描述含"推测性" / "无 benchmark 数据" + P2 | 降 **P3** |
| 描述含"评估" / "review" / "待审视"无明确 outcome + P2 | 降 **P3** |
| `🟢 PR 内收口` 已完成 | 改 ✅ → DONE |

### P × 命中 矩阵速查

| 命中？ | 原 P | 新 P |
|---|---|---|
| ✅ 命中 | P3 | P2 |
| ✅ 命中 | P2 | P1 |
| ✅ 命中 | P1 | P1（维持）|
| ❌ 未命中 + 降级信号 | P2 | P3 |
| ❌ 未命中 | 任意 | 维持 |

---

## 5. Flag 调整规则

| 当前 Flag | 触发条件 | 新 Flag |
|---|---|---|
| 🟠 触发条件已达成且代码可核实 | 触发文本对应 PR/事件已发生 | **🟡** 排队 |
| 🟠 触发条件未达成 | — | 维持 🟠 |
| 🟡 + 命中升 P1 | 提到 P1 后不一定立即排期 | 维持 🟡 |
| 🟢 PR 内收口 已 ship | code 核实落地 | **✅** 候选归档 |
| 🔴 发布阻塞 | 仍阻塞 | 维持；不阻塞则改 🟡 + 备注 |
| ✅ | — | 候选归档（不在本 PR 物理删行）|

---

## 6. 输出形态

### 6.1 原表内编辑

只改 **P/Cx 列** + **Flag 列** + **Trigger 列**。**不改 ID、不动 Source、不删行**。

描述列编辑仅在两种场景允许：
1. STALE re-scope（如 C-09 范本）
2. 命中 DUP 时追加 `(DUP of <ID>)` 标注

### 6.2 评级日志

每阶段产物追加一段到 `docs/backlog/RERATING-LOG.md`：

```markdown
## Phase N — cap-XX (yyyy-mm-dd)

- 扫描条目数：NN
- DONE 候选：N（list IDs）
- STALE re-scope：N（list IDs + 一句话说明）
- DUP：N（list IDs → merge target）
- P 升级：N（list IDs：原 P → 新 P + 命中维度）
- P 降级：N（list IDs：原 P → 新 P + 降级信号）
- Flag 调整：N（list IDs：原 → 新）
- 规则修订（如有）：— 描述规则边界发现的问题 + 是否回改 RERATING-RUBRIC.md
```

### 6.3 commit 形态

每阶段独立 commit：

```
docs(backlog): re-rate cap-04 — bump N arch/dedup items + verify N DONE candidates

- 完成性核实 N 条（DONE M / STALE M / DUP M / OPEN M）
- P 升级 M：<列代表性 ID>
- P 降级 M：<列代表性 ID>
- Flag 调整 M
```

---

## 7. 阶段拆分

| 阶段 | 范围 | 条数 | 目标 |
|---|---|---|---|
| **P0**（本 PR）| 写 rubric + 加 backlog.md 顶部引用 | — | 规则单源 |
| **P1 试评** | cap-01 + cap-03 + cap-07 + cap-09 | ~20 | 校准规则，验证识别信号 |
| **P2 in-file 主体** | cap-04 + cap-05 + cap-06 + cap-08 + cap-10 + cap-12 | ~60 | in-file 章节收口 |
| **P3 大文件** | cap-02 + cap-13 | ~48 | metadata + observability |
| **P4 工具链 + 横切** | cap-14 + cap-x-cross | ~134 | 抽象统一 / 去重最密集 |

每阶段独立 PR。P1 试评后若发现规则误判率 > 20%，回改本文件再进 P2。

---

## 8. 反模式

下列做法在重评中**禁止**：

1. **批量改 P 不核实代码** — 任何 P 变化必须能在阶段 log 列出核实证据（grep 命中行 / 文件路径 / PR diff）
2. **架构 refactor 上调 P0** — 即使跨 ≥ 3 cap 也顶 P1
3. **删除 item 行** — DONE/STALE-CLOSE/DUP 都只改 Flag，物理归档由独立的归档批次完成
4. **合并描述但不留 DUP 标注** — merge 必须可追溯
5. **跳过 Step 1 完成性核验直接调 P/Cx** — 否则可能在已 DONE 项上花精力
6. **本 PR 内既改规则又开始执行 P1** — rubric 修订要独立 PR 才能形成稳定标尺

---

## 9. 引用

- charter：`.claude/rules/gocell/ai-robust.md`（AI-robust 三档 + Funnel 双向锁）
- backlog 主表：`docs/backlog.md`
- 归档：`docs/backlog/archive/`
- 评级日志：`docs/backlog/RERATING-LOG.md`（本批次启动后逐阶段追加）
