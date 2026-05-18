# ADR: archtest fixture 诊断断言载体选型 — golden-file 单源

> Status: Proposed
> Date: 2026-05-18
> Implementation: PR-A（本分支 `claude/review-pr-557-agents-DzzXk`，基于 develop）
> ref: PR #557（`refactor/604-archtest-fixturespec-marker`，count-only 现状，本 ADR 取代其断言形态）;
>      docs/backlog/cap-14-tooling.md L30 `FIXTURESPEC-DIAGNOSTIC-POSITION-BINDING-01`（本 ADR 即该 backlog 条目的提前落地决策）;
>      docs/backlog/cap-14-tooling.md L29 `FIXTURESPEC-COUNT-MATCH-UPSTREAM-HARD-01`;
>      .claude/rules/gocell/ai-collab.md §"载体决策原则" 第 1 条（codegen funnel + golden）+ §"AI-rebust 三档分级";
>      golang.org/x/tools/go/analysis/analysistest（`// want` prior art，本仓库 `tools/nogo/unconditionalskip/analyzer_test.go` 已在用）

## Context

`tools/archtest/` 的 fixture 测试需要断言"规则 R 在 fixture F 上产生恰好 N 条诊断、落在位置 L、带消息 M"。历史 Soft 反模式是测试表硬编码 fixture 行号（`wantLines []int{534}`），加 import / reformat 即漂移——这是 PR #536 CI 失败的系统性根因。

PR #557 的解法：引入 `fixturespec.Violation()` typed marker（Hard funnel），`AssertDiagnosticCount` 经 `*types.Info` 把"got 诊断数 vs marker 数"对齐。三 agent review（PR #557）+ backlog `FIXTURESPEC-DIAGNOSTIC-POSITION-BINDING-01` 共同确认：

1. **该机制是 cardinality-only**：`AssertDiagnosticCount` 唯一断言 `len(got) == CountViolationMarkers(pass)`，对 `Diagnostic.Rel/Line/Message` 不做任何比对。"漏报真实 violation + 误报邻近行"的 scanner regression 留 `len` 不变即静默通过。
2. **是 PR604 主动设计选择**，非疏忽——backlog L30 明确"与真实 violation 位置解耦，回避 marker 位置随 violation 行漂的痛点"，并已调研 analysistest / dominikh go-tools / go-critic golden 三家 prior art。
3. **PR body/godoc 过度宣称**"行漂移免疫 / 物理同行"，与 backlog 诚实记载（"cardinality-only，position deferred 到 P2/Cx3"）形成两套话术。
4. **#557 零 ADR 引入 Hard 治理机制**——这是它绕开标准件的结构性根因（见下）。

### 为什么 #557 会自己发明一套（根因）

`analysistest` 的 `// want` 是**注释**。章程 §三档分级里注释锚点 = **Soft**；§载体决策原则规定"Soft 形态严禁立项"。一个为过 AI-rebust review 而优化的作者，看到"用 analysistest = comment = Soft = 严禁立项"，唯一合规出路就是自造 typed-marker Hard funnel。章程 review 镜头只问"enforcement 是否 Hard"，**不问"测试是否仍测位置"**——funnel 顺利通过 review（PR #557 Agent 1：无 Cx1），恰因章程不度量被牺牲的东西。

根因是**作用域类别错误**：章程为"生产代码治理"设计（威胁 = AI 钻空子），被越界套用到"测试夹具期望声明"（威胁 = 测试覆盖力回归，非 AI 钻空子）。对夹具套 Hard-or-reject，必然逼出比标准件更弱的重新发明。无 ADR 让这次越界无人拦下。本 ADR 同时承担**章程作用域澄清**职责。

## Decision

**fixture 诊断断言改用 golden-file 单源载体**，取代 #557 的 cardinality-only marker 断言：

1. 规则 R 在 fixture F 上的实际诊断输出（`Rel:Line:Col Severity RuleID Message` 规范化文本）序列化为签入的 golden artifact（`tools/archtest/testdata/<fixture>/diag.golden`）。
2. `go test ./tools/archtest/... -update`（典型 golden flag）重生成全部 golden。
3. CI 走 `bash hack/verify-archtest.sh`，golden 不一致 → `git diff --exit-code` 红。
4. fixture 退化为**纯触发代码**，不再含任何 `fixturespec.Violation()` 手放 marker——期望完全由规则输出派生。

**#557 关系**：PR-A 基于 develop，**取代** #557 的断言形态。`fixturespec.Violation` marker + `FIXTURESPEC-VIOLATION-CALLER-ALLOWLIST-01` / `FIXTURESPEC-COUNT-MATCH-ENFORCED-01` 两个 funnel meta-archtest 在 golden 形态下**无守护对象**（无 marker 可声明），随 #557 一并不落地。#557 应在 PR-A ship 后关闭为 superseded。

### 为什么选 golden（而非 analysistest / typed-marker+position）

三层闭合，且**最不双源**：

| 维度 | golden | analysistest `// want` | typed-marker + position |
|------|--------|------------------------|--------------------------|
| 章程载体档位 | §载体决策 **第 1 条**（codegen funnel + golden = Hard 首选） | 注释锚点（章程定义 Soft） | archtest 第 3 档 fallback |
| 位置+消息绑定 | ✅ 派生自规则真实输出 | ✅ 同行注释 | ⚠️ 需重建 analysistest 匹配 |
| 抗漂移 | ✅ `-update` 自动重写，人不维护行号 | ⚠️ 注释随代码移动需手改 regex | ⚠️ marker 随触发行漂 |
| 对标 | K8s（`make generate && git diff --exit-code`、staticcheck baseline）+ go-critic golden | govet / staticcheck / 本仓库 nogo | 无外部对标（重新发明） |
| 实现量 | 中（golden 序列化 + update flag + 迁移） | 最小（标准库） | 最大（重新发明 analysistest） |

### 双源问题辨析（关键，回应 review 质询）

被表征的真值只有一个生成源：**规则逻辑 ⊗ fixture 源码**。其余皆为：(a) 人手写断言（#557 `Violation()` 计数 / `wantLines` / analysistest `// want`）——独立的人类第二真值，能因与回归无关的原因（数错、忘改）与规则分歧；(b) 机器重生快照（golden + CI `git diff --exit-code`）。

**(b) 严格比 (a) 更单源**：重生的 golden 不可能独立分歧——它就是规则输出的冻结，唯一"分歧"即 `git diff`，那恰是回归信号本身。golden 是所有选项里最接近单源者。章程 §载体决策第 1 条把 golden 列为 Hard 首选，正因"重生+diff"使生成器成为唯一源、golden 退化为派生 cache。

golden 记 `file:line` 看似 `wantLines` 病换地方——**否，差别在"重生"**。`wantLines` 有毒因要人手在命令式测试代码维护行号；golden 行号从不手工维护：改 fixture → `-update` → 行号自动重写 → `git diff` 显示 delta → reviewer 看到"加 import，N 行位移，golden 相应更新，诊断语义未变"。位置是派生且重生的，不是断言且维护的。这正是 K8s 赖以工作的分界，golden **消解**而非**搬迁**漂移。

真正的双源风险窄：仅当 golden 被手改/手工策展（直接编辑 `.golden` 让 CI 过而不修规则）。缓解（全为本仓库/章程已惯用）：golden 只能 `-update` 生成；contract-fanout 已规定"fixture diff 是 review 一等公民"，golden diff 享同等待遇；可选补 meta-archtest 锁 golden 仅随规则/fixture 同 commit 变更。

## Consequences

- **正面**：scanner regression（漏报+误报抵消、错行、错消息）不再静默通过；fixture 加 import/reformat 不再误报；消除 #557 的 count-only 覆盖力回归（含 notfound `assert.NotEmpty` 退化）；机制升 Hard 且单源；与 K8s / 章程首选载体对齐。
- **负面 / 代价**：golden 文件需 review 纪律（regenerate-only）；首次 `-update` 产出大量 golden（review 一次性确认基线）；迁移涉 backlog 自估 ~39 fixture pkg + ~15 caller，量大。
- **章程作用域澄清**：本 ADR 确立"fixture 期望声明不适用生产治理 Hard-or-reject；golden+regenerate+review 是其正确载体，且为章程 §载体决策第 1 条所背书"。后续同类 review 不得再以"注释/golden = Soft 严禁立项"否决 fixture 断言标准件。

## AI-rebust 评级

| 子件 | 评级 | 理由 |
|------|------|------|
| golden + `-update` + CI `git diff --exit-code` | **Hard** | 章程 §载体决策第 1 条 codegen funnel + golden；违反（漏 update / 手改 golden）= `git diff` 红，不可表达为"绿" |
| golden regenerate-only 纪律 | Medium → Hard 路径 | review 一等公民（Soft-ish）；可选 meta-archtest 锁"golden 变更必伴随规则/fixture 变更"升 Hard，列 PR-A 内或紧随 backlog |

## Scope（待用户拍板，ADR review 门）

本 ADR 决策"载体 = golden"。**实施范围**两档，需用户确认后才动 39-package：

- **范围 A（mechanism + exemplar）**：PR-A 仅落地 golden 序列化器 + `-update` flag + CI 接线 + 1~2 个范例 fixture 迁移作 golden proof + 本 ADR + 诚实性修正（对齐 backlog 措辞）。其余迁移分批走 PR-B/C。**符合原 PR-A 定义（先冻结机制契约，再批量迁移）。**
- **范围 B（full landing）**：PR-A 直接迁移全部 ~39 fixture pkg + ~15 caller。改动量大，单 PR review 负荷高。

推荐**范围 A**：契约（golden 载体）由本 ADR 冻结后，批量迁移是机械低风险操作，分批 PR 各自可审；notfound/errcode 特例（#557 review Agent 2 标真实等价性回归）单列 PR 因其非机械。
