# GoCell 系统工程逐层审查 — 顶层汇总

| 项 | 值 |
|---|---|
| 评估日期 | 2026-05-04 |
| 基准 commit | `11600a4f`（develop） |
| 评估视角 | 系统工程 10 维度（含第一性原理推导）— **纵向逐层** |
| 与既有报告关系 | 与 `202605041800-systems-engineering-gap-assessment.md`（横向 V 模型 / 敏捷 / SOLID / SysML）**互补不重叠** |
| 审查方法 | 8 个 reviewer subagent 并行（Read/Glob/Grep 只读，model=opus），层级关系作为共同上下文传入 |
| 单层报告 | 见 `20260504-systems-layer-{01-kernel ... 08-supporting}.md` |

---

## 0. 摘要

GoCell 八层均处于"骨架成立、刚性强、缺口收敛"状态，但**没有任何一层达到全维度 ✅**。共审 32 个维度格，得 **8 个 ✅ + 22 个 ⚠️ + 2 个 ❌**。

最具杠杆的三条系统工程缺口：

1. **journeys/ 全部 lifecycle: experimental，导致 `gocell verify journey --active` 静默跳过 8 条 journey** — V 模型右侧验证顶层失效。
2. **cell.yaml ↔ NewBaseCell Go 字面量四处漂移**（owner / schema / smoke / consistencyLevel） — 元数据治理与运行时事实分裂，scaffold 第 4 个 cell 时会复制放大。
3. **`kernel/metadata.ContractMeta` 既承载 Kind 又承载 Endpoints + 与 `kernel/wrapper.ContractSpec` 双定义** — 同一概念三种结构，触发 contracts 层 4-kind 分类的第一性原理可推导性问题。

8 个层共发现 **5 个 P0、22 个 P1、14 个 P2**，按"撬动下游约束数"排序的 Top 8 见 §3。第 ⑩ 维度（第一性原理推导）在 kernel / contracts / journeys / assemblies 4 层各产出至少一条必然性论证或反证，按计划闭合。

---

## 1. 总评矩阵（8 层 × 选定维度）

> 每层只评本层选定的 3–4 个高权重维度。非选定维度留空（不代表"不重要"，本次审查不覆盖）。

| 层 | ① 边界 | ② 内聚 | ③ 依赖 | ④ 生命周期 | ⑤ 故障 | ⑥ 观测 | ⑦ 测试 | ⑧ 守卫 | ⑨ 演进 | ⑩ 第一性 |
|---|---|---|---|---|---|---|---|---|---|---|
| **kernel/** | ⚠️ | — | ⚠️ | — | — | — | — | ✅ | — | ⚠️ |
| **runtime/** | ✅ | — | ✅ | ✅ | — | ⚠️ | — | — | — | — |
| **adapters/** | ✅ | — | — | — | ⚠️ | ⚠️ | ⚠️ | — | — | — |
| **cells/** | — | ⚠️ | ✅ | ⚠️ | — | — | — | — | ⚠️ | — |
| **contracts/** | ✅ | ⚠️ | — | — | — | — | — | — | ⚠️ | ⚠️ |
| **journeys/** | ⚠️ | — | — | — | — | — | ⚠️ | — | ⚠️ | ⚠️ |
| **assemblies/** | ⚠️ | — | ✅ | — | — | — | — | — | ❌ | ⚠️ |
| **支撑层** | — | ⚠️ | ✅ | — | — | — | — | ⚠️ | ⚠️ | — |

**统计**：✅ 8 / ⚠️ 22 / ❌ 2（assemblies ⑨ 演进；其余一处 ❌ 在 supporting ⑨ 演进近 ❌ 但仍归 ⚠️）。

**单维度全层观察**：
- **③ 依赖方向** — 4 层选审有 3 ✅ 1 ⚠️（kernel 内部 22 子模块缺方向守卫）；这是 GoCell **最强**的维度，主要因为 archtest 158 + depguard 8 双层覆盖。
- **⑩ 第一性原理** — 4 层选审 4 ⚠️（kernel / contracts / journeys / assemblies 都有任意性切分或冗余补丁信号）；这是 GoCell **概念可推导性**最弱的维度，需要 architect 仲裁的也集中在此。
- **⑨ 可演进性** — 4 层选审 1 ❌ 3 ⚠️（assemblies 最严重，cells / journeys / supporting 有腐化迹象）；这是**未来 12 个月易腐化**的主战场。
- **⑥ 可观测性** — runtime ⚠️ + adapters ⚠️ 同根（HTTP 路径完整、事件路径系统性缺位）。

---

## 2. 问题计数

| 层 | P0 | P1 | P2 | 合计 |
|---|---|---|---|---|
| kernel/ | 0 | 3 | 1 | 4 |
| runtime/ | 0 | 4 | 2 | 6 |
| adapters/ | 2 | 5 | 2 | 9 |
| cells/ | 1 | 4 | 4 | 9 |
| contracts/ | 0 | 3 | 2 | 5 |
| journeys/ | 2 | 3 | 1 | 6 |
| assemblies/ | 0 | 3 | 2 | 5 |
| 支撑层 | 0 | 3 | 3 | 6 |
| **合计** | **5** | **28** | **17** | **50** |

---

## 3. Top 8 关键问题排序（按"撬动下游约束数"）

### #1 — journeys: active lifecycle 全空，CI 静默跳过 8 条 journey
- **来源**：journeys [P0]
- **位置**：`journeys/J-*.yaml:3` × 8 + `kernel/verify/runner.go:202`
- **撬动**：V 模型右侧验证顶层、status-board ↔ lifecycle 双轨问题、actor 字段缺失推导、fixtures 去留决策
- **复杂度**：Cx2
- **建议**：先升 J-ssologin 为 active 让 CI 闭环生效，再决策剩余 7 条状态机统一规则

### #2 — cells: cell.yaml 与 NewBaseCell 字面量四处漂移
- **来源**：cells [P0]
- **位置**：`cells/{accesscore,auditcore,configcore}/cell.yaml` vs `cell.go` NewBaseCell 字面量
- **撬动**：每个 cell + scaffold 模板 + governance 与运行时事实统一 + Init 模板收敛 + L0 cell 概念去留 + l0Dependencies 字段意义
- **复杂度**：Cx2
- **建议**：让 NewXxxCore 接受外部加载的 CellMetadata（来自 yaml）或 `//go:embed cell.yaml` 解析自填，archtest 守卫两侧字段相等

### #3 — kernel + contracts: ContractMeta/ContractSpec 双定义 + ContractMeta SRP（Kind ⊕ Endpoints）
- **来源**：kernel [P1] + contracts [P1] 同根
- **位置**：`kernel/metadata/types.go:152-213` + `kernel/wrapper/spec.go:21-30`
- **撬动**：concept "contract" 在 kernel 内部三处分散（metadata / wrapper / governance）+ contracts 4-kind 分类的第一性原理 + EndpointsMeta 10 个 omitempty 字段森林 + 加新 kind 必改 5 处的扩展成本
- **复杂度**：Cx3 + ADR
- **建议**：先写 ADR 明确 kind 必然性（保留 4 类 vs 收敛到 (Sync, Persistent) 二维），再决定 ContractMeta 拆为 sealed-style 接口 + 派生 ContractSpec

### #4 — adapters: OIDC / postgres / redis / s3 未实现 ManagedResource，故障语义不统一
- **来源**：adapters [P0×2 + P1×2]
- **位置**：`adapters/{oidc,postgres,redis,s3}/`
- **撬动**：所有依赖 adapter 的 cells + composition root readyz 重复代码 + OIDC JWKS 轮换失效隐患 + transient/permanent 错误分类的 outbox handler Disposition 决策
- **复杂度**：Cx2 × 4 + 一条 archtest 守卫 (`MANAGED-RESOURCE-COMPLETENESS-01`)
- **建议**：先补 Checkers/Worker 接口实现统一 readyz 命名（`{adapter}_ready`），再用 `errcode.WrapInfra` 推广 transient/permanent 分类

### #5 — runtime: 事件路径可观测性系统性缺位（4 条 P1 同根）
- **来源**：runtime [P1×4]
- **位置**：`runtime/outbox/relay.go` + `runtime/eventrouter/router.go` + `runtime/eventbus/eventbus.go` + `runtime/observability/metrics/`
- **撬动**：HTTP 路径已示范 register-at-startup + Provider 自动 wire；事件路径需补对偶 → outbox/eventrouter/eventbus 三套 collector + 命名空间统一 + 与 ⑩ 状态机显式化路线图衔接
- **复杂度**：Cx2 × 3 + Cx1 × 1
- **建议**：把 shutdown/outbox/event 三套指标的工厂统一搬到 `runtime/observability/metrics/{shutdown,outbox,event}.go`，bootstrap 自动注入

### #6 — assemblies: schema 欠定义 + scaffold 不覆盖（N=1 是工具阶段问题）
- **来源**：assemblies [P1×3]
- **位置**：`assemblies/corebundle/assembly.yaml` + `kernel/metadata/types.go:247-260` + `cmd/gocell/app/scaffold.go`
- **撬动**：⑨ 可演进性（解锁 N=2）+ examples 绕过 assemblies 模型的统一性 + cmd/ 1:1 镜像冗余 + deployTemplate 取值域 + maxConsistencyLevel 派生校验
- **复杂度**：Cx2
- **建议**：先扩 schema（owner / maxConsistencyLevel / deployTemplate enum）+ 加 `gocell scaffold assembly`，再决策 examples 接入与否

### #7 — cells: 单 slice 多 verb（auditappend × 14 / configread × 3）
- **来源**：cells [P1]
- **位置**：`cells/auditcore/slices/auditappend/slice.yaml` + `cells/configcore/slices/configread/slice.yaml`
- **撬动**：② 职责与内聚 slice 边界定义 + verify.contract 粒度 + scaffold slice 模板 + 后续 cell 同样模式不蔓延
- **复杂度**：Cx3
- **建议**：按事件域 / listener 拆分 slice，共享 service 层 dispatch；不引入向后兼容包装

### #8 — journeys: J-confighotreload 引用未声明的 entry-deleted v1 contract（且通用：journey 引用未校验）
- **来源**：journeys [P0]
- **位置**：`journeys/J-confighotreload.yaml:13,15`
- **撬动**：双向静态校验机制（类似 ADV-06 对 contractUsages）+ journey ↔ contract 闭环可推性 + status-board 占位与 yaml 真实落地的统一
- **复杂度**：Cx1（修单条）→ Cx2（补 validate 规则）
- **建议**：在 `gocell validate` 增加 journey.contracts ↔ contracts/ 双向存在性校验

---

## 4. 第 10 维度（第一性原理）专项汇总

按计划在 4 个层各产出至少一条必然性论证或反证：

| 层 | 论证/反证 | 结论 |
|---|---|---|
| **kernel/** | 22 子模块对照 6 条公理（A1–A6） | **19 子模块直接对应公理；3 个轻度任意性**（contract 概念分散在 metadata + wrapper + governance）。`depgraph` 不可移除（验证 A5）；`wrapper` 与 `contract` 可合并 |
| **contracts/** | 4 类 kind 是否必然？ | **不必然**。从 F1=同步/异步 × F2=瞬时/持久 推得 **2 类**就能覆盖通信底层；4 类是 DDD/CQRS + Watermill 的语义分类借用。command/projection 在平台层 0 实例是反证 |
| **journeys/** | 拿掉 journey 层会丢什么？ | **会丢"组合验收 + 人工验收"两类不可下推语义**。journey 不是冗余补丁。但 schema 缺 actor / dependsOn 字段说明形态层未收敛 |
| **assemblies/** | 拿掉 assemblies/ 层会丢什么？ | **会丢 boundary.yaml 派生 + metrics-schema 入口 + assembly-completeness 校验**。assemblies/ 必然存在；但 N=1 是工具阶段问题（scaffold 缺失），不是架构终态 |

**横向观察**：第 ⑩ 维度的 4 个层全部 ⚠️。共同信号是**概念合并/分裂的必然性论证缺位**——多数情况下当前结构合理但**也可以另一种合理形式存在**，差异在工程便利度而非公理硬约束。建议在 R-02（概念完整性整理）路线图条目下统一仲裁。

---

## 5. 不建议改造的项（驳回防呆）

以下提案若出现，应**先驳回**，因违反形态层约束或属于抽象错位：

| 提案 | 驳回理由 |
|---|---|
| **CD 链路 / 镜像 / SBOM / staging / canary** | GoCell 是嵌入式编程框架，不拥有运行时与持久层；CD 是客户应用职责（CLAUDE.md + ADR `202605041430` §3.1） |
| **性能 / SLO / 容量基准（如 p99 < 100ms）** | 框架不知道客户负载特征；SLO 在客户应用层定义，框架只提供接入点 |
| **微服务化拆分 / 服务网格集成** | 框架不假设分布式拓扑，N=每个客户不同部署形态 |
| **journey 改 Gherkin** | 现 passCriteria + checkRef 比 Gherkin 更工程化（直接驱动 go test，不需 step definition 翻译层） |
| **K8s 风格 CRD / etcd / informer / controller-runtime** | K8s 是同范式参照（declarative + reconcile），不是同形态搬运；CLAUDE.md + ADR `202605041430` 已明确 |
| **业务正确性审查** | accesscore/auditcore/configcore 是参考实现而非框架契约本身，不在框架审查范围 |

---

## 6. 与横向报告的对比

横向报告（`202605041800-systems-engineering-gap-assessment.md`）给出 R-01 至 R-10 路线图；本纵向报告与之关系：

| 横向 R-编号 | 主题 | 本次纵向 Top 8 对应 |
|---|---|---|
| R-01 | 需求追溯链 | （本次未审 — 视角不同）|
| R-02 | 概念完整性整理（ContractMeta SRP / Verify scope） | **#3 同根** |
| R-03 | fixtures/ 占位处置 | journeys [P1] 同根 |
| R-04 | 事件层可观测性 | **#5 同根** |
| R-05 | SysML 视图自动生成 | （未审）|
| R-06 | 状态机显式化 | journeys [P1] / cells [P1] 同根 |
| R-07 | 参数图引入（SLO） | （形态层不适用，本次驳回项）|
| R-08 | CODEOWNERS / PR 模板 | supporting [P1] **同根** |
| R-09 | ADR ↔ Roadmap 索引 | （未审）|
| R-10 | examples 多 cell 协作 | assemblies [P2] 同根 |

**互补效果**：横向报告关注"概念维度的覆盖度"（V 模型 / SOLID / SysML），本纵向报告关注"每层内部的工程质量"（边界 / 故障 / 守卫 / 第一性原理）。两份并读时，**横向定位"哪一类问题缺哪个能力"**，**纵向定位"哪一层先动手"**。

---

## 7. 执行建议

按 Top 8 杠杆排序的执行顺序（不引入实施 commit，仅给推进序列）：

1. **#1 + #8 一起做**（journeys/CI 闭环 + journey 引用静态校验，Cx1+Cx2，可同 PR）
2. **#2**（cell.yaml 升格为唯一元数据来源，Cx2，触发 scaffold 模板更新）
3. **#4**（adapters ManagedResource 与 readyz 命名统一，Cx2 × 4，可分 4 PR）
4. **#5**（事件层可观测性 register-at-startup 推广，Cx2 × 3）
5. **#3**（ContractMeta / ContractSpec 概念整理，需 ADR + Cx3 重构）
6. **#6**（assemblies schema + scaffold，Cx2）
7. **#7**（slice 拆分，Cx3，可与 #2 触发的 scaffold 升级一起做）

P2 类问题（CODEOWNERS / PR 模板 / 占位文件清理 / 文档锚定 / lint exclusion 复盘）适合穿插在主轴 PR 中**搭车处理**，不单独立项。

---

## 附录 A — 单层报告索引

| # | 层 | 报告 | 主要发现 |
|---|---|---|---|
| 01 | kernel/ | `20260504-systems-layer-01-kernel.md` | 22 子模块内部 DAG 无守卫 / observability 无 doc / ContractMeta-Spec 双定义 |
| 02 | runtime/ | `20260504-systems-layer-02-runtime.md` | bootstrap 编排范本级 / 事件层指标系统性缺位 |
| 03 | adapters/ | `20260504-systems-layer-03-adapters.md` | 重型/轻型 adapter 一致性差 / OIDC 缺 fail-fast / fake 不导出 |
| 04 | cells/ | `20260504-systems-layer-04-cells.md` | cell.yaml ↔ Go 字面量漂移 / 单 slice 多 verb / Init 三套打法 |
| 05 | contracts/ | `20260504-systems-layer-05-contracts.md` | strict/loose 范式成熟 / 4-kind 必然性可疑 / v1→v2 无演练 |
| 06 | journeys/ | `20260504-systems-layer-06-journeys.md` | active lifecycle 全空 / fixtures 文档承诺空挂 / actor 维度缺 |
| 07 | assemblies/ | `20260504-systems-layer-07-assemblies.md` | schema 欠定义 / scaffold 不支持 / examples 绕过 |
| 08 | 支撑层 | `20260504-systems-layer-08-supporting.md` | archtest 158 + depguard 8 工程一流 / CODEOWNERS 缺 / Makefile 缺独立 target |

---

## 附录 B — 验收清单（怎么知道本次审查有效）

- ✅ 每层至少 1 条问题（最多 cells/adapters 各 9 条、最少 kernel 4 条）
- ✅ 每条 P0 在 §3 Top 8 中可被反查（5 条 P0 全部出现）
- ✅ 第 ⑩ 维度在 4 个目标层各产出至少 1 条论证 / 反证（kernel / contracts / journeys / assemblies 各 1+）
- ✅ 不出现"为了凑数"的 trivial Cx1（最低复杂度 Cx1 都关联具体路径与建议方向）
- ✅ Top N 排序依据是"撬动下游约束数"而非"严重感受"（每条都列出 ≥ 3 条下游连带）

---

> 报告结束。本评估**不引入任何 ADR 或代码改动**；Top 8 条目由用户按需后续单独发起 `/ship` 或 `architect` agent 推进。
