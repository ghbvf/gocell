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

## 三、工具层待定

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

---

## 四、示例层待定

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
