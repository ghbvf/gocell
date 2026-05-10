# Azure DevOps Board ↔ GoCell 映射

把 ADO Board 的工作项概念映射到 GoCell 的 Journey / Cell / Slice 模型，用于
项目管理与开发协作的统一语言。

---

## 映射表

| ADO 概念 | GoCell 载体 | 关键属性 |
|----------|-------------|----------|
| Product | Repo + `assemblies/*.yaml` | 一个 Assembly = 一条可部署产品线；产品里程碑（如 v1.0 readiness）跨多个 Epic |
| **Epic** | **Journey** (`journeys/J-*.yaml`) 或主题 **Track** | 粒度可灵活：单条业务旅程 / 横切主题工作集合 / 产品里程碑子集 |
| **Feature** | **Cell** (`cells/*/cell.yaml`) | 自治能力单元，长生命周期；1 Cell = 1 Feature |
| **PBI / User Story** | **Slice** (`cells/*/slices/*/slice.yaml`) | 1 Slice ≈ 1 PR；`allowedFiles` + `verify.unit/contract` 即 DoD |
| **Task** | 独立工作项 | 工程粒度的实施单元，不落 yaml |
| **Bug** | 独立工作项 | 已确认的、行为偏离规格的缺陷 |
| **Issue** | 独立工作项 | 杂项登记板：Impediment / Risk / Spike / Clarification |
| Sprint | 时间盒 + **Wave** 编排 | 与 Cell/Slice 结构维度正交 |

---

## Slice 的粒度约定

`Slice` 是工作粒度的最小单元：每个 Slice 对应一个 PR，`allowedFiles` 锁文件
范围，`verify.unit` + `verify.contract` 锁验收清单。

---

## Bug

**定位**：独立工作项，自有生命周期 `New → Active → Resolved → Closed`。
Bug 是把已交付的价值修回正，与 PBI 的"新增价值"形成对照。

**边界**：
- vs PBI：PBI 增量价值，Bug 偏差修复
- vs Issue：Bug 是已确认、可复现、已定性为缺陷；Issue 是尚未定性的杂项

**立项门槛**：可复现即立 Bug，不可复现先入 Issue（Spike 调查）。

---

## Issue

**定位**：独立工作项，但角色是**输入项**——本身不产出用户价值，必须转化为
下游工作项后关闭。

**4 类形态**（统一 Issue 容器，用 tag 区分）：

| 形态 | 含义 | 典型转化路径 |
|------|------|-------------|
| Impediment | 阻碍：等外部依赖、资源、决策 | 阻碍排除后转 PBI/Bug，或外部解除直接关闭 |
| Risk | 已识别但未缓解的风险 | 设计缓解动作转 PBI；接受风险则关闭并归档 |
| Spike | 调查 / 技术研究 | 调研结论拆出 PBI/Bug；无后续直接关闭 |
| Clarification | 需求 / 契约 / 边界澄清 | 澄清结论转 PBI；澄清后无需变更则关闭 |

**链接关系**：与下游 PBI/Bug 双向链接——Issue 记录"由哪些下游工作项承接"，
PBI/Bug 记录"解决了哪些 Issue"，保证溯源链完整。

---

## 工作项挂靠层级

Task / Bug / Issue 都可挂任意层级（Slice / Cell / Journey / Product），按
**问题的影响范围或根因层级**归属。三者的分布特征不同：

| 工作项 | Slice | Cell | Journey | Product | 高层挂靠的本质 |
|--------|-------|------|---------|---------|---------------|
| Task | 多数 | 少数 | 极少 | 极少 | 切面工程（CI / 部署 / 治理基建） |
| Bug | 多数 | 较常见 | 少数 | 极少 | 集成性缺陷、产品级非功能缺陷 |
| Issue | 较少 | 较常见 | 多数 | 较常见 | 影响面未定性，范围越大越倾向高层 |

**判定原则**：

- **影响范围决定层级**：Bug 复现路径跨几个 Cell 就挂 Journey；Issue 影响面跨产品就挂 Product
- **就近原则**：能在更窄层级承接的不上提，避免高层堆积应在 Cell 解决的工作
- **反模式**：挂高层应是**问题本身跨层**，不是**分析能力不够下推**——判定标准是能否给出"为什么不能挂在更窄层级"的具体理由
