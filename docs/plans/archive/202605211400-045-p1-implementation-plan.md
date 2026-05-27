# [SUPERSEDED] 045 — P1 任务实施计划（2026-05-21）

> **2026-05-25 归档**：本 plan 是 point-in-time 执行视图（§8 已声明不滚动同步，真值走 Project v2 / GitHub Issues）。主线 21 项中 **15 已 close**，剩 6 项继续在 GitHub Issues 跟踪，归档不丢失。
>
> **Wave 完成度**：W1 6/6 ✅ / W2 5/5 ✅ / W3 3/4（#613 open）/ W4 部分（#823 ✅、#618-CELLEMITTER 随 commit `6f3834ece` ship、#699/#836 open）/ W5（#676 已关，见下）。
>
> **⚠️ 仍在飞的 6 项 P1 主线（归档后走 GitHub Issues,勿丢）**：
>
> | Issue | 标题 | Wave |
> |---|---|---|
> | #613 | C-04 CELLS-INIT-TEMPLATE-CONVERGE | W3 |
> | #699 | RepoReadyz upstream Hard | W4 |
> | #836 | Interface 出口表面闭环 bundle | W4 |
> | #687 | M3 Governance Rule Engine 数据化（Cx4） | W5 |
> | #686 | M2 Cell/Slice lifecycle state machine（Cx4） | W5 |
> | #782 | M4 反向 coverage archtest | W5 |
>
> **#676（W5 ContractSpec/ContractMeta 合一）已于 2026-05-25 关闭**（completed）：dual-**source** 早经 codegen + 包拆分闭环，issue body 引用的 `wrapper.ContractSpec` 已搬到 `kernel/contractspec`，前提失效。W5 该行作废。
>
> §6 Hold（条件触发）项均为 flag-cond/triggered backlog issue，与本 plan 归档无关，各自独立等触发。以下为归档前原文。

# 045 — P1 任务实施计划（2026-05-21）

> 源：GitHub Project v2 #3 `priority=P1 status!=Done` 共 35 条（2026-05-21 快照）。
> 目标：以 4-6 并行度切 5 个 Wave，前后 Wave 间只保留真实文件 / 概念依赖。
> 真值源：GitHub Issue body（`Trigger` / `Files` / `Source`）。本文件是执行视图，
> 不复制 issue body 全文。条目变更回 issue 改，PR 关闭 issue 同步更新本文件 ✅。

## 0. 依赖图（关键路径）

```
              #704 HEALTHZ-INTERFACE (M1) ─┬─► #699 RepoReadyz upstream Hard
                                           ├─► #686 M2-LIFECYCLE
                                           └─► #805 ReadyProbeName typed funnel

              #615 cell-pkg decompose ──► #613 CELLS-INIT-TEMPLATE ──► #618-CELLEMITTER

              #689 ValidationResult sealed ──► #687 M3-RULE-ENGINE ──► #782 M4-COVERAGE

              #829 contract shared $ref mixin ─► #677 errors schema 单源

              #676 ContractSpec/ContractMeta 合一 ── trigger: K#04 PR-4 codegen 窗口
```

非关键路径项均独立成 PR，互不阻塞。

## 1. Wave 1（立即开工，6 并行）

零依赖、零触发、单 PR 闭环。预计 1-2 天全部 merge。

| Issue | 标题 | Cx | Files 焦点 |
|---|---|---|---|
| **#698** | OTEL span PII redact + Insecure 守卫 | 2 | `adapters/otel/span.go` |
| **#655** | RbacAssign L2 PG 原子性测试 | 2 | `cells/accesscore/.../rbacassign` + PG outbox testcontainer |
| **#713** | SafeID wire-decode 上游 archtest（Hard 升级）| 2 | `tools/archtest/safeid_funnel_test.go`（新）|
| **#714** | GaugeVec funnel 上游 Hard（`promwrap`/`otelwrap` internal 包）| 2 | `adapters/{prometheus,otel}/internal/`（新）|
| **#791** | Required-dep nil-guard archtest（Soft→Hard）| 3 | `tools/archtest/` + 12 service `NewXxx` |
| **#751-D.2** | Assembly Generator 并发污染 mutex | 1 | `kernel/assembly/generator.go` |

Wave 1 总 Cx ≈ 1+2×4+3 = 12。

## 2. Wave 2（W1 至少 3 项 merge 后启动，5 并行）

W2 各项与 W1 文件无重叠。#828 与 W1#655 同 cell 不同 slice/file，故挪 W2 避 PR 撞期。

| Issue | 标题 | Cx | 依赖 |
|---|---|---|---|
| **#828** | user_repo.Update 拆 use-case 专用方法（UpdateProfile / UpdateLockState）| 3 | S6 已 ship；独立 |
| **#689** | ValidationResult sealed Result types（Soft→Hard）| 3 | #687 前置 |
| **#690** | buildHTTPEndpointSpec funnel archtest（上游 Hard）| 3 | 独立 |
| **#829** | contracts/shared/cas mixin codegen `$ref` 单源 | 3 | 验 contractgen `$ref` 解析 |
| **#677** | contracts/shared/errors schema 单源 | 3 | 复用 #829 codegen mixin 形态 |

## 3. Wave 3（架构主线，4 并行）

| Issue | 标题 | Cx | 依赖 |
|---|---|---|---|
| **#704** | `kernel/healthz` 接口包 + 38 Health 收口（M1）| 3 | Wave 4-5 多条依赖 |
| **#615** | kernel/cell 包分解残留（auth_plan / mode_resolver / Registrar / health 4 项）| 3 | — |
| **#805** | ReadyProbeName typed funnel | 3 | 与 #704 协同 |
| **#613** | `BaseCell.RegisterStandard` 模板 + scaffold + 3 cell 改造 | 2 | 依 #615 Registrar 改名 |

`#615` Registrar 改名先于 `#613` BaseCell.RegisterStandard 模板落地；其它 W3 项可并行。

## 4. Wave 4（演进 + Bundle 子条，5 并行）

| Issue | 标题 | Cx | 依赖 |
|---|---|---|---|
| **#699** | RepoHealthProber 上游 Hard（sealed registration / codegen marker）| 3 | #704 |
| **#618-CELLEMITTER** | `outbox.CellEmitter` sealed marker 扩展 | 3 | #613 |
| **#618-SCAFFOLD-TYPED-ID + ASSEMBLY-META-FIELD-GUARD** | 合并 PR | 2+3 | — |
| **#836-PR441-DOC** | ISP 子接口 / sealed marker / `Wrap*ForCell` 消费者文档闭环 | 3 | — |
| **#823** | PG-REPO-AMBIENT-TX patterns funnel（6 PG repo 已过 trigger 阈值）| 3 | — |

## 5. Wave 5（M2-M4 + ContractSpec 合一，3-4 并行）

| Issue | 标题 | Cx | 依赖 |
|---|---|---|---|
| **#687** | Governance Rule Engine 数据化（M3，64 规则 → engine + YAML）| 4 | #689 |
| **#686** | Cell/Slice lifecycle field + 显式 state machine（M2）| 4 | #704 |
| **#676** | ContractSpec / ContractMeta 合一 | 3 | K#04 PR-4 codegen 窗口 |
| **#782** | 反向 coverage archtest 5 条（M4）| 3 | #687 |

## 6. Hold（条件触发，背景挂起）

不进主线推进，触发事件落地后单独拉 PR：

### 6.1 触发型 Hard 升级（等真实绕过 / 阈值事件）

| Issue | Trigger 摘要 |
|---|---|
| **#719** SERVICEOWNED-HANDLER-OWNER-CHECK | serviceOwned endpoint ≥ 3 且 guard 形态收敛 / helper 跨函数封装逃逸首现 |
| **#722** PASS-PRODUCTION-UPSTREAM | 第 2 次 production-only rule 作者遗漏 `RunTypedProduction` |
| **#732** ARCHTEST-FUNNEL-CALLSITE-LEVEL | 同文件内新 live setter 漏过 / domain-authz 主题 PR |
| **#738** PG-REPO-AMBIENT-TX 上游 Hard | 新 *_repo.go 绕过 R1/R2/R3 / adapters/postgres 包扩展 |
| **#747** CLI-TOPLEVEL-HELP-REGISTRY | 顶层命令增删 / PrintUsage↔commands 漂移 |
| **#760** board.state / lifecycle typed const | 第 2 次非法字符串绕过 / `kernel/metadata` 重构窗口 |
| **#787** MEM-TX R2b receiver | 字符串锚点绕过事件首现 / `MEM-TX-LOCK-OWNERSHIP-01` 迁 `RunTyped` 批次 |

### 6.2 fixture / 注入面

| Issue | Trigger |
|---|---|
| **#681** fixture cell-id typed builder Hard | 下次 governance fixture 大改 / 引入 metadatatest helper |
| **#682** clock injection struct-field 形态 | 下次新增 struct-field Clock adapter |

### 6.3 Bundle 余下子条

| Bundle | 余下子条 | 触发 |
|---|---|---|
| **#618** Sealed marker（7 子条）| `CELL-PUBLIC-OPTION-NAMED-IFACE-EMBED` / `ADR-CELL-RAW-INFRA-WORDING` / `SEALED-MARKER-FILE-LIST-AUTODISCOVER` | 触发型 |
| **#706** S7-PG-OBSERVABILITY（4 子条）| 全部触发型 | S8 audit query / outbox 主题 PR |
| **#751** Scaffold drift（8 子条）| D.2 已拉到 W1，余 7 子条 | K#09 ship 后批 / 触发型 |
| **#836** Interface 消费者闭环（3 子条）| E.1 已拉到 W4，E.2 (`S7-FU-ARCH-EVO`) / E.3 (`S7-FU-DOCS`) | 与 S7 接口/PG 演进同窗口 |

### 6.4 协同型

| Issue | 协同条件 |
|---|---|
| **#632** J-04 contract schema naming normalize | 与 J-03 v1→v2 演练同 PR |
| **#803** ADAPTER-FAKE-EXPORT | cell mock 扩展时 |

## 7. 节奏与产出

- **W1**：6 个 developer agent 并行；Cx1/Cx2 主导，预期 1-2 工作日全 merge。
- **W2 启动条件**：W1 任意 3 项已 merge（避免 PR 评审阻塞）。
- **W3 启动条件**：W2 至少 3 项 merge；#704 与 #615 是关键路径，单独投人。
- **W4 启动条件**：W3 #704 / #615 merge；#699 必须 wait #704。
- **W5 启动条件**：W4 半数 merge；#687 / #686 Cx4 单独成 PR，每条独立 reviewer。

## 8. 进度看板

进度回灌真值源：GitHub Project v2 #3。PR close issue 后，本文件 Wave 表格保留历史
快照，不滚动同步。需要最新状态走：

```bash
gh project item-list 3 --owner ghbvf --limit 300 --format json
```

或：

```bash
gh issue list --label backlog --state open --search "priority:P1"  # 须 Project field 查询
```

## 9. 来源与口径

- 35 条 P1 issue 拉取脚本：`gh api graphql` paginate `user("ghbvf").projectV2(number:3).items`，
  filter `priority.name=="P1" && status.name!="Done" && content.state=="OPEN"`。
- Cx / Trigger / Source 取自 issue body Migrated marker（来源 `docs/backlog/20260520/`
  历史快照），与 Project v2 fields 一致。
- Bundle 子条评级与触发条件来自 issue body sub-list；本计划不复制 7+ 行嵌套，
  仅引用 bundle 父 issue 号 + 子条 ID。

---

*Author: AI 协作落档 2026-05-21；prev 044 journey-backlog-realignment*
