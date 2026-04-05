# V3 骨架 — 待定事项

> 本文档记录目录骨架生成时刻意保持松耦合的决策点。
> 每条 TODO 在实现工具或跑通第一个 journey 后再定死。

---

## 一、模型层待定

### TODO-M1: L0 Cell 定位与目录

**现状**：L0 Cell 暂时放在 `cells/` 下，与 L1+ 同级。
**待定**：是否迁到 `modules/` 或 `libraries/` 独立目录？改名为 `module` / `library-partition`？
**触发时机**：首次实际创建 L0 Cell（如 shared-crypto）时决定。
**影响范围**：`cells/` 目录结构、`cell.yaml` 的 `type` 枚举、`l0Dependencies` 引用路径。

### TODO-M2: Journey 文件拆分

**现状**：Journey 保持单文件 `journeys/J-*.yaml`，同时承载 spec / routing / plan。
**待定**：是否拆成三份文件（spec.yaml / routing.yaml / plan.yaml）？
**触发时机**：Journey 内容膨胀到单文件难以维护时。
**影响范围**：`journeys/` 目录结构、`run-journey` 工具的文件发现逻辑。

### TODO-M3: 非契约依赖图

**现状**：只有 `l0Dependencies` 一种非契约边。共享进程状态、运行时配置注入等不可建模。
**待定**：最终形态是什么？是否引入 `runtimeDependencies` 字段？依赖图放哪？
**触发时机**：`select-targets` 从 advisory 升级为 blocking 之前。
**影响范围**：`cell.yaml` / `slice.yaml` 字段、`validate-meta` 校验规则、`select-targets` 精确度。

### TODO-M4: Verify 命名约定与解析规则

**现状**：V3 仅在 slice 示例中展示了 verify ID 格式，未定义解析规则。V2.1 有完整的四类前缀解析、标准化规则、路径推导规则。
**待定**：从 V2.1 搬运并确认：前缀分发格式（smoke/unit/contract/journey）、连字符→下划线标准化、contract 类别的 v{N} 终止符解析、路径推导（verify ID → go test 命令）。
**触发时机**：Phase 1 — validate-meta / verify-slice / verify-cell 实现前。
**影响范围**：verify-slice、verify-cell、run-journey 三个工具的核心解析逻辑。
**来源**：V2.1 §Verify 命名约定（最成熟的设计之一，可直接复用）。

### TODO-M5: kind + consistencyLevel 最低要求（V2.1 C6）

**现状**：V3 未提及 contract kind 与 consistencyLevel 的领域约束。V2.1 定义了 http >= L1、event >= L2、command >= L2、projection >= L3。
**待定**：是否保留这组领域约束？是否调整阈值？
**触发时机**：Phase 1 — validate-meta 拓扑校验实现前。
**影响范围**：validate-meta 的 C 组规则。

### TODO-M6: allowedFiles 定义

**现状**：V3 未定义 `slice.allowedFiles` 字段。V2.1 将其分类为 canonical（带约定默认值 `cells/{cell-id}/slices/{slice-id}/**`），并定义了重叠检测（C8）和覆盖完整性（C9）。
**待定**：是否保留 allowedFiles 字段？缺省推导规则？是否需要 C8/C9 校验？
**触发时机**：Phase 1 后期或 Phase 2 初期 — select-targets 实现前。
**影响范围**：select-targets 文件→slice 路由、validate-meta 的文件归属校验。

### TODO-M7: Fixture 模型

**现状**：V3 完全未涉及 fixture。V2.1 定义了完整体系：`fixtures/{fixture-id}.yaml`、三种类型（service-mock/seed-data/config-override）、namespace 隔离、加载/清理顺序。
**待定**：fixture 文件位置约定、类型枚举、隔离模型、加载顺序（可比 V2.1 简化）。
**触发时机**：Phase 2 — run-journey 实现前。
**影响范围**：`fixtures/` 目录结构、run-journey 工具、journey.yaml 的 fixtures 引用。

### TODO-M8: passCriteria.assert 结构化断言

**现状**：V2.1 定义了 `passCriteria.assert: { type, expect, ... }` 结构化断言提示（如 `{ type: httpStatus, expect: 302 }`、`{ type: rowExists, table: sessions, key: session_id }`）。V3 的 passCriteria 示例中未出现 assert 字段。
**待定**：run-journey 是否需要 assert 字段来执行 auto check？还是仅靠 checkRef 解析即可？
**触发时机**：Phase 2 — run-journey 实现时，与 TODO-M7 同期。
**影响范围**：journey.yaml passCriteria 格式、run-journey 断言执行逻辑。

### TODO-M9: Slice consistencyLevel 覆盖规则

**现状**：V3 在 slice 定义中提到"owner / consistencyLevel 继承自 Cell"，但未明确：(1) slice 是否可以覆盖 cell 的 consistencyLevel？(2) 覆盖时是否必须 ≤ cell 值（V2.1 C5）？
**待定**：是否保留 slice 级 consistencyLevel 覆盖？约束规则？
**触发时机**：validate-meta 实现时（与 TODO-V3 中的 C5 联动）。
**影响范围**：slice.yaml 字段定义、validate-meta 校验规则。

### TODO-M10: G3 护栏 — consistencyLevel 不是语义代理

**现状**：V2.1 护栏 G3 明确声明 consistencyLevel 不定义也不暗示交付语义、重放保证、幂等性范围、运行时行为——这些是独立字段。V3 "六条真相"吸收了 G1/G2/G4/G5，但 **G3 未显式声明**。
**待定**：是否需要在 V3 或 consistency.md 中补充 G3 语义？还是由 contract 上的独立字段（replayable / idempotencyKey / deliverySemantics）隐式表达？
**触发时机**：consistency.md 完善时。
**影响范围**：文档层面，防止 consistencyLevel 被误用为语义代理。

---

## 二、运营层待定

### TODO-O1: status-board 位置

**现状**：`journeys/status-board.yaml` 与 journey 同目录。
**待定**：是否迁到独立的 `operations/` 目录？或作为 CI 产物不入库？
**触发时机**：团队协作流程稳定后。
**影响范围**：`status-board.yaml` 路径、`validate-meta` 的 advisory warning 逻辑。

### TODO-O2: Assembly boundary 生成策略

**现状**：`assemblies/{id}/generated/boundary.yaml` 由工具生成，禁止手编。
**待定**：生成时机（CI only / local dev 可选）、是否入库、fingerprint 校验策略。
**触发时机**：`generate-assembly` 工具实现时。
**影响范围**：`.gitignore` 规则、CI pipeline、`assembly.yaml` 是否需要版本锁。

---

## 三、校验层待定

### TODO-V1: 值解析模型（五类分类）

**现状**：V2.1 定义了 canonical / derived-anchor / inherited / generated / delivery-only 五类分类，每个字段严格归属一类。V3 仅在示例中提到 derived-anchor，未系统定义。
**待定**：是否需要文档化？还是内化到工具代码中即可？
**触发时机**：validate-meta 实现时 — 需要决定每个字段缺省时"推导还是报错"。
**影响范围**：validate-meta 对可选字段的处理逻辑。

### TODO-V2: `["*"]` 通配消费方规则

**现状**：V2.1 定义了 `["*"]` 的语义（任何已注册 actor 可消费）和约束（不得与具名 actor 混合、skip 消费方成员检查但仍要求 actor 注册）。V3 未提及。
**待定**：是否支持 `["*"]`？约束规则是否沿用 V2.1？
**触发时机**：validate-meta endpoints 校验实现时。
**影响范围**：validate-meta 的 B4/C3 规则实现。

### TODO-V3: V2.1 隐含但 V3 未显式列出的校验规则

**现状**：以下 V2.1 规则在 V3 "附：校验规则精简版"中未显式出现，但逻辑上可能仍需保留：
- B6：boundary.yaml 中的 exportedContracts/importedContracts 引用校验
- B7：contract.id 版本片段与目录路径版本目录一致性（V3 隐含在"目录约定"中但未显式）
- C5：slice consistencyLevel 不超过 cell（联动 TODO-M9）
- C8：allowedFiles 不重叠（依赖 TODO-M6）
- C9：allowedFiles 覆盖完整性（依赖 TODO-M6）
- C10：journey.contracts 中每个契约的提供方/消费方 actor 必须出现在 journey.cells 中
- C11：boundary.yaml exported/imported 与实际 assembly 边界一致性
- C14：active 契约必须有 slice 声明提供方
- C16：角色-verify 方向一致性
- D2：consistencyLevel 与最低验证要求
- D4：schema 目录 kind 片段 = contract.kind（V3 隐含在目录约定中）
- D10：生成物 fingerprint 校验（generatedAt + sourceFingerprint 新鲜度检查）
**待定**：逐条确认是否保留、修改或删除。
**触发时机**：validate-meta 实现时。
**影响范围**：validate-meta 的校验规则集。

### TODO-V4: `migrations` 字段格式

**现状**：V3 提到"已弃用契约不得被新引用（除非有 migrations 声明）"，但未定义 migrations 字段。V2.1 定义了 `slice.migrations: [{contract, target, deadline}]`。
**待定**：是否沿用 V2.1 格式？是否需要简化？
**触发时机**：第一个契约 deprecation 时。
**影响范围**：slice.yaml 字段、validate-meta 的 C17 规则。

---

## 四、工具层待定

### TODO-T1: 校验规则编号体系

**现状**：V3 文档列出核心校验规则但未编号（V2.1 的 C1-C20 / D-W1 等已废弃）。
**待定**：新编号格式（REF-01 / TOPO-01 / VERIFY-01？）和完整清单。
**触发时机**：`validate-meta` 实现时确定。
**影响范围**：CI 输出格式、waiver 引用方式、文档索引。

### TODO-T2: 字段名和目录名最终约定

**现状**：核心字段已在 V3 定义。部分细节字段（如 `allowedFiles`、`migrations`）尚未定死。
**待定**：
- `allowedFiles` 缺省推导规则的精确定义
- `migrations` 声明格式（deprecated 契约的迁移计划）
- contract `schemaRefs` 是否支持多版本并行
**触发时机**：首批 slice 实现并跑通 `verify-slice` 后。

### TODO-T3: CLI 子命令划分

**现状**：`cmd/gocell/` 已建目录，CLAUDE.md 提到 `validate / scaffold / generate / check / verify`。
**待定**：子命令参数、输出格式、exit code 约定。
**触发时机**：实现第一个子命令（建议从 `validate` 开始）时。

### TODO-T4: select-targets 路由矩阵

**现状**：V3 仅提到 select-targets 是 advisory 级别，未定义路由规则。V2.1 定义了 11 种文件变更模式→影响范围的详细映射，以及粗粒度/契约推断两种模式。
**待定**：路由矩阵是否沿用 V2.1？journey-slice-map 索引结构和新鲜度校验？
**触发时机**：select-targets 实现时。
**影响范围**：select-targets 工具、`generated/indexes/journey-slice-map.yaml`。
**来源**：V2.1 §select-targets 影响路由（设计成熟，可直接搬运）。

---

## 五、契约层待定

### TODO-C1: compatibilityPolicy 默认值

**现状**：V2.1 定义了 `{ breaking: [remove_field, change_field_semantics], nonBreaking: [add_optional_field] }` 默认值和覆盖机制。V3 未提及。
**待定**：是否保留？是否有更简单的替代方案？
**触发时机**：首次 schema 变更时。
**影响范围**：contract.yaml 字段、schema 版本演进流程。

### TODO-C2: Event/Projection 推荐可选字段

**现状**：V3 已定义 event 必填字段（replayable/idempotencyKey/deliverySemantics）和 projection 必填字段（replayable）。V2.1 还推荐了 event 的 `orderingSemantics`。
**待定**：是否需要 orderingSemantics？其他推荐字段？
**触发时机**：事件消费或投影实现时。
**影响范围**：contract.yaml event/projection 模板。

### TODO-C3: 轻量级契约指南

**现状**：V2.1 提供了"何时建模轻量级契约"的决策指南（同 assembly、简单同步调用、无外部消费方）。V3 未提及。
**待定**：是否需要文档化？还是作为团队实践自然形成？
**触发时机**：团队实际建模跨 Cell 交互时。
**影响范围**：文档/指南，不影响工具实现。

---

## 六、运维/可选层待定

### TODO-P1: 跨 Assembly 依赖图（V2.1 C18）

**现状**：V2.1 的 C18 要求 validate-meta 输出 `generated/indexes/assembly-dependency-graph.yaml`。V3 未提及。当前仅一个 assembly。
**待定**：多 assembly 场景下的依赖图格式和生成策略。
**触发时机**：第二个 assembly 出现时。
**影响范围**：validate-meta、generated/ 目录结构。

### TODO-P2: Status Board 补充字段

**现状**：V3 定义了 status-board 基础字段（journeyId/state/risk/blocker/updatedAt）。V2.1 还有 `evidenceRefs`（标注为必填）、`targetDate`、`updatedBy`。
**待定**：是否补充 evidenceRefs？是否调整为可选？
**触发时机**：交付管理流程需要时。
**影响范围**：status-board.yaml 格式、validate-meta advisory warning。

### TODO-P3: 各模型推荐可选字段

**现状**：V2.1 中多个推荐可选字段在 V3 中未提及：
- Cell：`noSplitReason`、`schema.tables`
- Slice：`traceAttrs.extra`
- Contract：`summary`、`semantics`、`traceRequired`
- Assembly：`killSwitches`、`flags`
- Actor：`description`
- Journey：`primaryActor`
**待定**：按需逐个决定是否纳入 V3。
**触发时机**：各自相关功能实现时。
**影响范围**：各 yaml 模板，纯文档性质。

---

## 七、示例层待定

### TODO-E1: examples/ 示范项目

**现状**：README 列出 `sso-bff / todo-order / iot-device` 三个示例。目录为空。
**待定**：是否每个示例独立 go.mod？还是共享根 module？是否包含完整 cell/contract/journey 元数据？
**触发时机**：核心 kernel 跑通后。

---

## 决策记录格式

当上述 TODO 被解决时，在此处追加决策记录：

```
### DECIDED: TODO-XX — 标题
**日期**：YYYY-MM-DD
**决策**：...
**原因**：...
```
