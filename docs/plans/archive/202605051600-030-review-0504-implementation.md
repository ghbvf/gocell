# 030 · GoCell 0504 Review 实施计划（独立于 027/028/029）

> **🗄️ 已归档（2026-05-19）**：任务台账（原 §0/§1.A/§2/§3 Track A·C·G·J·F/§5/§6.1）已完全迁出到 active backlog 单源——已完成项进 `docs/backlog/archive/`，live 项进 `docs/backlog.md` + `docs/backlog/cap-*`。本文件仅留 §1 路径概述 / §4 Won't-do 边界 / §6 与 029 关系 / §7 验收存档，作历史脉络保存，不再更新。后续 0504-derived 工作追踪以 active backlog 为准。

> 日期：2026-05-05
> 来源：`docs/reviews/20260504-*.md`（16 份，约 120 条 finding 去重后 ≈ 80）+ `docs/reviews/202605041800-systems-engineering-gap-assessment.md`
> 形态：高杠杆主轴串行 + 防腐与工程基线并行（**2026-05-05 修订**：K-04 决「不迁移」+ K-05 决「不做」，原"架构边界仲裁前置"取消，详见 §6 决议）
> 不依赖 029 在飞 PR；与 029 共用 reviewer 容量需在飞 PR 总数 ≤ 4

---

## 1. 优先实施路径（Phase；2026-05-05 N6/N7 won't-do 后修订）

| Phase | 目标 | 主轴 | 并行 | 估时 |
|:-:|---|---|---|---|
| **0** | 决议 ADR / 备忘 + 包文档 | K-04 决议 ADR（"不迁移"，1-2h）+ K-05 决议备忘录（"不做"）+ K-03 observability pkgdoc | 与 Phase 1 完全并发，不阻塞任何实施项 | ~半天 |
| **1** | P0 race + lifecycle + governance ctx + 06.PR4 全量迁移（**所有项文件域 0 重叠，最多 6 worktree 并发**）| K-01（assembly race）/ G-01（hook dispatcher）/ G-02（rollback ctx）/ G-03（validate ctx）+ 029 06.PR4（吸收 K-06 残余 + K-05a + K-05c）| K-02 / R-03 / A-01..A-04 / F-01 / F-02 | ~2 周 |
| **2** | 事件可观测性 + cells 收敛 + K-07/K-08 | R-01..R-03 + C-01..C-05 + C-08/C-09（搭车 K-07）| G-04（kernel DAG）+ A-05..A-08 + 029 K#07/K#08 | ~3 周 |
| **3** | governance 后续 + 装备类 + 路线图扩展 | G-05..G-17（含触发条件型 G-16/G-17）| J-01..J-04 + F-03..F-10（按 reviewer 容量）| ~3 周 |

**累计**：~6-7 周单线 / ~3-4 周满并发（依赖 reviewer 容量 ≤ 4 在飞）；N6/N7 won't-do + 06.PR4 与 N1-N5 并发节省 ~1.5 周。

**为什么不再需要"Phase 0 ADR 仲裁"前置**（2026-05-05）：
- K-04 决「不迁移」— accesscore/auditcore/configcore 留 framework，产品定位 = 框架自带认证/配置/审计能力（与 v1.1 product roadmap 对齐），落 ADR 锁定决策（1-2h）
- K-05 决「不做」— (a) 保留 4-kind 含 projection（v1.1 CQRS 后端会用，删了重做）；(b) `EndpointsMeta` 10 字段森林 cost > benefit（K#06 contractgen 已让消费侧类型隔离）；(c) ContractSpec 双定义由 029 06.PR4 完成时自动闭环
- K-04/K-05 决议落 ADR/备忘是文档动作（半天）而非实施 PR，与 Phase 1 任何实施项并发
- 029 06.PR4 与 N1-N5 文件域 0 重叠（cells/`slice_gen.go` + contract.yaml + generated/contracts vs kernel/{assembly,governance,observability}/* + runtime/bootstrap/options.go），可立即并发，无前置等待

---

## 4. Won't-do / 触发条件待达

| 来源 | 项 | 理由 / 触发条件 |
|---|---|---|
| summary §5 | CD 链路 / 镜像 / SBOM / staging / canary | GoCell 是嵌入式框架，不拥有运行时与持久层；CD 是客户应用职责（CLAUDE.md + ADR `202605041430` §3.1）|
| summary §5 | 性能 / SLO / 容量基准（如 p99 < 100ms） | 框架不知客户负载特征；SLO 在客户应用层定义，框架只提供接入点（F-09 引入 schema 字段，不预设具体值）|
| summary §5 | 微服务化拆分 / 服务网格集成 | 形态层冲突，N=每个客户不同部署形态 |
| summary §5 | journey 改 Gherkin | passCriteria + checkRef 比 Gherkin 更工程化（直接驱动 go test，不需 step definition 翻译层）|
| summary §5 | K8s CRD / etcd / informer / controller-runtime | K8s 是同范式参照而非同形态搬运 |
| summary §5 | 业务正确性审查 | accesscore/auditcore/configcore 是框架自带能力（K-04 决「不迁移」，归 framework 仓）|
| gap-assessment §7.4 | runtime topology API（实际请求 trace 拓扑） | 由 OTel + Jaeger/Tempo 生态承接 |
| gap-assessment §6 R-10 | examples 多 cell 协作样例 | `examples/ssobff` 已示范，触发条件 = 客户反馈"现有 ssobff 不足以演示 L2/L3 跨 cell 协作"|
| kernel-group1 G1-16/G1-17/G1-19/G1-20 | AfterStop 超时测试 / 并发 race detector / Level 注释 / Worker 命名 Run/Shutdown | P3 测试与命名调整，搭车 G-10 / G-01 同 PR 修；不单独立 PR |
| kernel-group2 G2-05/G2-15/G2-16/G2-17/G2-18/G2-20 | 命令终态写授权 / persistence 零测试 / inmem 并发 race / HandleResult 零值降级测试 / Redis 幂等 key 容量规划 / AdvanceCommand internal | P2/P3 加固，搭车 G-07 / G-08 / G-09 / R-01 同 PR；不单独立 PR |
| kernel-group3 G3-12/G3-14/G3-17/G3-21/G3-22/G3-23/G3-24/G3-25 | parser 无缓存 / CurrentKeyIDProvider 静默降级 / depgraph 互环测试 / clockmock 默认时间 / Catalog ListByStatus / nonce fake / metadata off-by-one / closure 无记忆化 | P2/P3 触发条件型；YAML 文件 < 100 / depgraph 互环 0 实例 / nonce fake 与 G-12 一起做；不单独立 PR |
| supporting-08 §3 cross-layer | `example-cells-isolation-ssobff` depguard 规则 | 触发条件：ssobff/ 出现 cells/ 子目录；当前无该子目录则规则缺失合理 |
| kernel-group3 G3-01 | YAML anchor bomb (HIGH 已降级 LOW) | yaml.v3 内置 `allowedAliasRatio()` Phase 2 已激活 + 1 MiB 文件大小限制 = 与 K8s CVE 修复等价；GoCell 是 CLI 工具非网络暴露 API server，可选加固（节点数 ≤10000）触发条件 = 出现 anchor bomb 实际报告 |
| 0504 综合 | 真冗余清零后所有"伪冗余" | software-review §2.2 已论证 6 项表面冗余实为分层意图（runtime 同名 alias / depgraph 双层 / pkg vs 顶层 contracts / pkg vs kernel ctxkeys / runtime/eventbus vs adapter / auth 三种 token），永不消除 |
| backlog T6 | `CONTRACT-EVENT-PAYLOAD-CODEGEN-01` event subscriber decode/validate codegen | 当前 2 cell consumer（accesscore/configreceive + configcore/configsubscribe），触发条件 = ≥5 cell consumer；规模未到 codegen 收益不及手写成本，对标 oapi-codegen 模式；1-2d |

---

## 6. 与既有 029 roadmap 的关系

- **不依赖 029 在飞 PR**：本计划所有条目独立可起，但与 029 K#05 PR-V1-CODEGEN-MARKER-MIGRATE 在 cell.yaml 字段层有重叠 — 若 K#05 PR-A2/PR-B 先于本计划 K-06 ship，K-06 改造需 rebase 到 markergen 之上（工时不变，路径换）
- **与 029 K#04/K#06 codegen 协同**：本计划 K-08 ASSEMBLY-SCHEMA-SCAFFOLD 派生 `cmd/{id}/modules_gen.go` 应基于 K#04 framework，复用 `tools/codegen/` 的 render/writer/verify pipeline
- **与 029 R-04 R 路线图重叠**：029 文档未直接给 R-04 PR，本计划 R-01 EVENT-OBSERVABILITY-METRIC-PACK 是 R-04 的具体落地
- **不跟 029 Lane A/B/C/D 抢 reviewer**：建议本计划 Phase 0-1 与 029 K#05 PR-A2/PR-B 互斥时间窗，Phase 2 起可与 029 K Phase 4 装备类并行

---

## 7. 验收清单

- ✅ 16 份 0504 报告 finding 原映射 36 PR（11 关键路径 + 25 并行轨道）；P0 5 条全部进 Phase 0-1。**2026-05-19：原 §2 关键路径表已整节退役回 backlog 单源**（11 项已完成 + K-04/K-05 决议不做 + K-06/K-07/R-01/R-02/A-03/A-04 live 项均在 active backlog 有落点），**§3 并行轨道（Track A/C/G/J/F 全部）已整节退役**——已完成项归档、live 项（C-06/08/09·G-08/10/15/16·J-02/03/04·F-01~F-10）均转 active backlog（`backlog.md` + `cap-*`）单源续追，0504 计划任务台账完全迁出，本 plan 仅留 §1 路径概述 / §4 Won't-do / §6 029 关系 / §7 验收存档
- ✅ Top 8（summary §3）逐条覆盖：#1→K-02 / #2→K-06 / #3→K-05 / #4→A-01..A-04 / #5→R-01 / #6→K-08 / #7→K-07 / #8→K-02
- ✅ R-01..R-10 路线图条目：R-01→F-06 / R-02→K-05 / R-03→J-02 / R-04→R-01 / R-05→F-07 / R-06→F-08 / R-07→F-09 / R-08→F-01 / R-09→F-10 / R-10→Won't-do（触发条件型）
- ✅ Won't-do 区列出 7 大类边界 + 26 条 P2/P3 搭车项（含 T6），避免单独 PR 噪声

---

## 参考

- **0504 review 报告原文**：`docs/reviews/20260504-*.md`（16 份）+ `docs/reviews/202605041800-systems-engineering-gap-assessment.md`
- **形态层口径**：`docs/architecture/202605041430-adr-architecture-optimization-via-engineering-thinking.md` §3
- **既有 roadmap**：`docs/plans/202605011500-029-master-roadmap.md`（本计划 §6 给出协同方式）
- **CLAUDE.md 与 rules**：`CLAUDE.md` + `.claude/rules/gocell/{api-versioning,error-handling,eventbus,observability}.md`
- **相关 ADR**：`docs/architecture/202605031600-adr-v1-schema-evolution.md` / `202605031900-adr-handler-vocabulary-collapse.md` / `202605021500-adr-kernel-clock-injection.md` / `202605040030-adr-wire-format-out-of-kernel.md`
