# 043 enforcement 基底进化 → 新抽象 Hard 化（能力依赖链实施计划）

**生成日期**：2026-05-19
**真值源**（只引指针）：[`042 archtest 审计`](../reviews/202605181109-042-archtest-six-agent-audit.md) · [`004 缺口分析`](framework-capability-gaps/202605131500-004-capability-gap-analysis.md) · [`005 roadmap`](framework-capability-gaps/202605162100-005-framework-capability-roadmap-plan.md)

## 0. 焊接点

004 §总判断已点名：archtest 须从守"**形态唯一**"升级到守"**关系唯一**"（wire envelope 字段集单源 / contract↔impl↔test 三方一致 / 跨多文件关系验证）。这一句把 042 与 005 焊成一个系统，不是两张待办：

- **042 = enforcement 基底现状 + 它必须怎么进化**，并固化一批 **Hard 范本**（sealed type / typed funnel / codegen 单源 / 关系验证工具）。
- **005 新抽象 = 第一批需要"关系唯一"的消费者**（W0 envelope 字段集、W5 schema registry、[7] health dep）。
- **真依赖是能力依赖，非文件碰撞**：042 把 Hard 范本与关系工具固化在前，005 新抽象**出生即 Hard**；顺序反了（先建抽象后补 seal）会留 Soft 区间。

故主轴是依赖层，冲突域降为执行期次级约束。

## 1. 范本映射（融合证据：042 不是并行项，是 005 站立的模式地基）

| 042 固化的 Hard 范本 | 模式 | 005 哪个新抽象按此出生即 Hard |
|---|---|---|
| §4#4 HandleResult unexport | sealed struct 字段全私有 + 包内 builder | **W0 envelope**：Entry.Headers 信封字段集 sealed + codegen 单源 |
| §4#3 errcode sealed Public/Internal | sealed newtype 分公开/内部诊断 | W0 envelope 内 Principal/Correlation/Time 公开 vs 内部分层 |
| §4#1 ProbeName sealed funnel | typed concept + 构造期 Validate + 注册面只收 typed | **[7] health dep**：cell.yaml 声明式依赖 + typed probe funnel |
| §4#2 fenceToken 未导出 token | 上游封口 token，包外不可表达跳过 | W3 command bus / W4 in-proc 的 dispatch/transport 上游封口 |
| §4#6 façade 单 Run + typeseval 跨文件 | 关系验证工具面 | **W5 schema registry** contract↔impl↔test 三方一致 / W0 字段集单源 archtest |
| §2c 声明式 boundary（depguard） | 声明式单源替命令式 | W4 assembly 拓扑 transport bind |

## 2. 能力依赖链（计划主轴）

### L0a — 合规（零代码、零依赖、章程违章 → 立即）
042 §3a：4 组 silent-carryover Soft 登记 backlog 升级 / godoc 显式 accept（ai-collab Review checklist 硬性，当前违章）。无代码域，与一切并行。

### L0b — 基底进化 + Hard 范本固化（005 新抽象的前置）
| 项 | 作用 |
|---|---|
| §4#6 façade 收缩 + typeseval 跨文件关系验证 | "关系唯一"工具面，W0/W5 关系 archtest 写在其上 |
| §2c 命令式 boundary → depguard/.go-arch-lint | 释放 archtest 专注关系规则 |
| §2a/§2b sub-rule 塌缩 + theme 归并 | 收 ID 面，新关系规则不被淹没 |
| §4#4 / §4#3 / §4#1 / §4#2 sealed 范本 | 固化 §1 表的四个 Hard 模式 |

### L1 — 新抽象按范本出生即 Hard（依赖 L0b）
| 项 | 依赖范本 |
|---|---|
| W0 wire envelope（sealed + codegen 单源建，非先建后 seal） | §4#4 + §4#3 + §4#6 |
| [7] health dependency 声明 | §4#1 ProbeName funnel |
| W5 schema registry（contract↔impl↔test 三方一致） | §4#6 关系工具 |

### L2 — 能力链续推（依赖 L1）
[5] after-commit → [1] HTTP 幂等 → [4] command bus；[3] in-proc contract；[27] outbound；[11] 反查；[22] authz↔contract；§4#5 sink 脱敏；§4#7 clock；§4#8 identitymanage；[20]/[25] 及第二三批余项进 backlog 池。

### 休眠
005 W6 Saga / W10 projection — 补偿原语 / 超时语义 / 声明 vs 编程式边界设计张力未收敛，scope 未知，先独立 ADR，禁凭猜。

## 3. 冲突域（执行期次级约束）

依赖层确定**何时**做；冲突域确定同层内**能否并行**。同冲突域同时仅一个 in-flight PR。

| 冲突域 | 文件/包 | 同域项 |
|---|---|---|
| D-ARCHTEST | `tools/archtest/*` | §4#6 / §2a/b/c |
| D-OBHDR | `kernel/outbox` Headers/Emit/HandleResult | §4#4 → W0 |
| D-CELLHEALTH | `kernel/cell` health 面 + `cell.yaml` deps | §4#1 → [7] |
| D-AUTHREVOKE | session/refresh Store + UserRepository | §4#2 |
| D-ERRCODE | `pkg/errcode` | §4#3 |
| D-CODEGEN | `tools/codegen` + `contract.yaml` | W5 / [3] / [4] / §4#9 |
| D-TXLIFE | `adapters/postgres` tx + lifecycle | [5] / [26] / [32] |
| D-OBSV | observability 反查 + redaction sink | [11] / §4#5 |

§2 整合的 rule-族避让：不碰正被 sealed 范本收口的 rule 族（OUTBOX/HEALTH/credential/DETAILS），否则 golden/ID 双改冲突。

## 4. 调度

- **t0（并行）**：L0a 合规登记 ∥ L0b 四 sealed 范本（§4#4/#1/#2/#3 域两两不相交）∥ L0b §4#6+§2（D-ARCHTEST 串行，rule-族避让）。
- **t1（L0b 对应范本 ship 后）**：§4#4 ship → W0（同 D-OBHDR）；§4#1 ship → [7]（同 D-CELLHEALTH）；§4#6+§2 ship → W5 关系 archtest 可写。
- **t2（L1 ship 后）**：W0 ship → [11]/[27]/[6]；[5] ship → [1] → [4]；[3] → §4#9；空档 → §4#5/#7/#8、[22]、[20]/[25]。

## 5. 纪律

1. 单 L0b/L1 项 = 单 Wave = 单实施计划 + 单 ADR。
2. **顺序不可倒**：L0b 范本未 ship，对应 L1 新抽象不得先建（避免 Soft 区间）。
3. 冲突域互斥；"释放后解锁"等前序 merge。
4. funnel 类（§4#1/#2/#3/#4 + W0/W5/[7]）立项须给上游 Hard / 下游 Hard 两栏评级，仅一侧 Hard 不立项。
5. 不留软回退：sealed token / envelope / schema check 禁 `${VAR:-default}`、禁 double-write、禁前置 status 探针。
6. W6/W10 scope 未知保持休眠。

## 6. 下一步

t0 启动 L0a + L0b（合规登记 + 四 sealed 范本 + façade/整合），各起 Wave 实施计划 + ADR。L0b 范本 ship 后按 §4 解锁对应 L1 新抽象。

> 本计划仅落编排，未做代码改动。
