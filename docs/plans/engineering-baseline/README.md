# GoCell 非 lint 工程基线对标

> 状态：研究 → 决策完成，待用户对齐 6 落点优先级
> 日期：2026-04-30
> 目标：在 lint/CI 治理（已完成，见 `../ci-governance/`）之外，扫 GoCell 12 个工程维度的现状，对 K8s/kubebuilder、Temporal/fx、CRDB/Vault 三组 SoT 项目做横向对标，提炼出 6 个最高 ROI 落点
>
> **产品视角探索**见 `../product-roadmap/`（GoCell → MDM → 零信任路线可行性，与本目录 framework 视角解耦）

## 链路概览

```
现状盘点              SoT 对标（3 组并行）               交叉对照              决策
──────────────       ──────────────────────────       ──────────────       ──────────────
GoCell 12 维度        K8s + kubebuilder                                    6 个落点
（agent: 76 tool      （A 代码生成 / G API 治理         engineering-       priority-decision
 use, 详细到          / H 文档自动化）                  research-          （L1-L6 + 路线
 grep 证据）                                            cross-cut.md        图 + 三件套）
                     Temporal + fx                     （12 × 3 SoT
                     （D DI/启动 / C log+metrics       矩阵 + 横向交叉
                     / J CLI/DX）                      对照表 + 数据
                                                       底稿引用）
                     CRDB + Vault
                     （C 错误库 / B 测试 harness
                     / E 安全供应链）
```

## 文档清单

### 决策层（先读）

- **`202604300600-radical-lightweight-revision.md`** —— **修正决策（最新核心）**：基于 3 摩擦 grep 实证 + 不向后兼容路径，**10 个核心落点 E1-E10**
  - E1 二级词汇收口（HandleResult 三态枚举替代 5 词幂等模型）
  - E2 Contributor interface 5→1（IoC 30% → 10-15%，**最大 IoC 杠杆**）
  - E3 Codegen 全栈消化样板（cell_gen.go + slice_gen.go + slice.yaml 完全自动）
  - E4 marker codegen + yaml 真相源迁移（cell.yaml + slice.yaml 完全 codegen，开发者只看 Go 代码）
  - E5 positioning 文档（零代码立即做，矫正 GoCell 在谱系中的位置）
  - E6 `gocell visualize`（boundary.yaml + cell_gen.go → DOT/SVG/mermaid）
  - E7 装备期（合并旧 L1/L2/L4/L5/L6 供应链 + lint + 错误库 + API 治理 + 文档自动化）
  - E8 bootstrap 显式化（延伸：phase 拆成 8 个独立函数，IoC 残量 10-15% → 5-10%）
  - E9 一键 scaffold + 项目模板（**最大入场摩擦杠杆**：4-8 小时 → 5-10 分钟）
  - E10 contract → DTO/iface 全自动 codegen（**最大业务面摩擦杠杆**：service.go 行数减半）
  - 路线图：Batch 1（核心简化 3 PR）→ Batch 2（codegen 消化 6 PR）→ Batch 3（装备 5 PR）= 14 PR
  - 减重数学：7 文件 / 220 行 / 11+ 概念 / 18× → 2-3 文件 / ~50 行 / 5-6 概念 / **~5×**

- **`202604300700-extension-leverages.md`** —— **延伸落点评估候选（E11-E14）**：ROI 中-低，需权衡，**不在核心 10 落点承诺范围内**
  - 含 §0 编号历史（说明早期 E14/E15/E16 的去向）
  - E11 errcode 体系收敛（5 → 3 构造器，建议与 E7-3 合并）
  - E12 Actor 合并入 Cell（**不建议做**，破坏语义清晰度）
  - E13 assembly.yaml 极简（cells 一行列表，其余从 cell.yaml 推导）
  - E14 pkg/ + runtime/ 库化承诺（**grep 实证修订**：runtime/ 14 子包中 12 个零 framework 契约，加 pkg/ + kernel/ + adapter 部分子包共 **25 个独立可用包**；与 E5 协同，承认 GoCell 内部本就是 framework + 大量轻库的复合体）

- **`202604300800-final-form-capability-overview.md`** —— **最终形态能力讲解（E1-E10 + E14 完成后）**：作为 v1.0 对外宣传 + 工程团队评估材料
  - §1 Elevator Pitch（30 秒讲完 GoCell 是什么）
  - §2 双形态总览（重 framework + 25 个独立 library 矩阵架构图）
  - §3 重 framework 形态能力清单（hello world + 启动 + Cell 注册 + Service + 多形态部署 + 治理工具 + 运行时能力）
  - §4 轻库形态能力地图（25 个独立包按 Tier A/B 列出 + 类比表 + import 示例）
  - §5 选型决策流（4 种典型场景如何选）
  - §6 谱系定位（CloudWeGo 模式变体）
  - §7 与 Go 社区哲学的和解（3 个常见挑战的回应）
  - §8 API 稳定性承诺（v1.0 后 SemVer 边界）
  - §9 升级路径（轻库 → 重 framework 5 phase 渐进式采用）
  - §10 Hello world 完整代码对照
  - §11 总结：双形态产品定位

- **`202604300500-engineering-priority-decision.md`** —— 初版 6 落点决策（保留作为历史；本版未删除是为了保留 grep 实证修正前的判断脉络）
  - L1 供应链安全武装（govulncheck + Semgrep + CodeQL + race 独立 job）
  - L2 codegen 隔离沙箱化（git worktree verify）
  - L3 **Lifecycle 对称清理 + 依赖图可视化**（不引 fx 依赖，吸收设计语义自建实现，2 PR 闭环）
  - L4 错误库增强 + log tag 静态约束（WithSafeDetails / AssertionFailedf / pkg/logtag，吸收 cockroachdb/errors + temporal tag.Tag 设计）
  - L5 API 治理升级（storageVersion + 弃用窗口 + kube-api-linter 评估）
  - L6 文档自动化起步（gen-crd-api-reference-docs + KEP frontmatter）
  - 路线图：Batch 1（L1 + L2 + L3）→ Batch 2（L4 + L5）→ Batch 3（L6）

> **核心原则**：CLAUDE.md「参考框架」表中所有映射（Cell 运行时 → fx 等）都是**吸收设计语义**，**不是引入实现**。GoCell 领域逻辑（DI/Lifecycle/错误库/Cell 模型）保留自建，外部协议工具（govulncheck/Semgrep/cobra/gen-crd-api-reference-docs）才直接引依赖。详见决策文档 L3 节 + 研究底稿 §5「吸收设计 vs 引入实现」准则。

### 数据底稿层

- **`202604300430-engineering-research-cross-cut.md`** —— 研究交叉对照
  - §1 GoCell 12 维度现状（成熟 3 / 半成品 5 / 缺位 4）
  - §2 K8s + kubebuilder（A 代码生成 / G API 治理 / H 文档自动化）含 raw URL
  - §3 Temporal + fx（D DI/启动 / C log+metrics / J CLI/DX）含 raw URL
  - §4 CRDB + Vault（C 错误库 / B 测试 harness / E 安全供应链）含 raw URL
  - §5 横向交叉对照表（GoCell × 3 SoT × 12 维度）
  - §6 4 个 agent transcript 路径

## 推荐阅读顺序

1. **快速理解**：先读 `202604300500-engineering-priority-decision.md` §1 总览表
2. **质疑某条落点**：去 `202604300430-engineering-research-cross-cut.md` 对应 §2/§3/§4 看 SoT 原始证据
3. **看 GoCell 当前哪些维度成熟可省**：`202604300430-engineering-research-cross-cut.md` §1（12 维度逐项）
4. **质疑跨 SoT 综合判断**：`202604300430-engineering-research-cross-cut.md` §5 横向对照表

## 决策状态

| Batch | 落点 | 状态 |
|---|---|---|
| 1 | L1 供应链安全 + L2 codegen 沙箱化 + L3 Lifecycle/visualize（不引 fx） | 待用户对齐启动 |
| 2 | L4 错误库增强 + L5 API 治理 | 待 Batch 1 完成 |
| 3 | L6 文档自动化 | 待 L4 godoc 完整 |

## 与 ci-governance 的关系

| 维度 | ci-governance（已完成） | engineering-baseline（本目录） |
|---|---|---|
| 内容 | golangci-lint 17 条规则 + Tier 1/2/3 分级 + Batch 1-4 切片 | 12 工程维度对标 + 6 落点 + Batch 1-3 路线 |
| 焦点 | 静态代码风格 / 安全规则 / 错误链 | DI 框架 / 错误库 / 供应链 / 文档自动化 / API 治理 |
| Batch 时间线 | 4 个 batch（lint 启用） | 3 个 batch（结构性 + 装备性） |
| 交叉点 | L1 supply-chain 含 govulncheck / Semgrep（与 ci-governance Batch 3 gosec 互补，不重叠） | — |

两条线相互独立但有时序耦合 —— ci-governance Batch 1（goimports/misspell/unconvert）应该先于本目录 Batch 1 启动，避免 lint cleanup 在 fx 重构期间撞 PR。

## 维护规则

- **新增对标项目**：补到 `202604300430-engineering-research-cross-cut.md` 对应 §2/§3/§4 或新增 §
- **新增落点**：在 `202604300500-engineering-priority-decision.md` §1 总览表新增 L7+，并在 §2 详述
- **PR 实施完成**：在决策文档对应落点段加链接到 PR + 更新本 README 决策状态表
- **跨文件链接**：本目录内用纯文件名；引用 `../ci-governance/` 用 `../ci-governance/xxx.md` 相对路径
