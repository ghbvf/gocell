# kernel/ 层规则

kernel/ 是 Cell/Slice 运行时 + 治理工具，是整个系统的底座。

## 依赖约束

**只允许**：标准库 + `pkg/` + `gopkg.in/yaml.v3`（metadata 解析）
**严禁依赖**：`runtime/`、`adapters/`、`cells/`

## 覆盖率要求

- kernel/ 层覆盖率 **≥ 90%**
- 必须使用 table-driven test
- 关键一致性测试禁止 `t.Skip()`

## 一致性级别（L0-L4）—— Source of Truth

| 级别 | 含义 | 典型场景 | 机制 |
|------|------|---------|------|
| L0 LocalOnly | 单 slice 内部本地处理 | 纯计算、校验 | 无需事务 |
| L1 LocalTx | 单 cell 本地事务 | session 创建、审计写入 | 单事务 |
| L2 OutboxFact | 本地事务 + outbox 发布 | session.created 事件 | transactional outbox |
| L3 WorkflowEventual | 跨 cell 最终一致 | 查询投影、合规追踪 | 事件消费 + 投影 |
| L4 DeviceLatent | 设备长延迟闭环 | 命令回执、证书续期 | 应用级状态机 |

## Cell/Slice 元数据规范

### cell.yaml 必填字段

| 字段 | 说明 |
|------|------|
| `id` | Cell 唯一标识 |
| `type` | `core` / `edge` / `support` |
| `consistencyLevel` | L0-L4 |
| `owner` | `{ team, role }` |
| `schema.primary` | 权威数据表声明 |
| `verify.smoke` | 冒烟验证命令 |
| `l0Dependencies` | L0 直接 import 依赖（条件字段，仅 L0 Cell 填写） |

### slice.yaml 必填字段

| 字段 | 说明 |
|------|------|
| `id` | Slice 唯一标识，**no-dash 格式**（FMT-16） |
| `belongsToCell` | 所属 Cell |
| `contractUsages` | 契约使用声明 |
| `verify.unit` | 单元测试命令 |
| `verify.contract` | 契约测试命令 |
| `allowedFiles` | **必填**，FMT-14 治理规则强制，`gocell scaffold` 生成初始值 |
| `owner` / `consistencyLevel` | 缺省时继承 cell.yaml |

### 治理规则

- **FMT-14**：`allowedFiles` 必填
- **FMT-16**：`slice.id` 与目录名必须 no-dash 格式；kebab-case 由 `gocell validate --strict` 拦截
- **TOPO-07**：actor 级别须满足 contract kind 要求
- 动态状态字段（`readiness` / `risk` / `blocker` / `done` / `verified` / `nextAction` / `updatedAt`）只写在 `journeys/status-board.yaml`，**禁止**出现在 cell.yaml / slice.yaml / contract.yaml / assembly.yaml
- `lifecycle`（`draft` / `active` / `deprecated`）是治理字段，**允许**写在 contract.yaml 中
- cell.yaml 不维护 slices、journeys、contracts 反向索引；汇总视图由工具生成

### 禁止的旧字段名

以下字段名已废弃，使用会被 `gocell validate` 拦截：

```
cellId / sliceId / contractId / assemblyId / ownedSlices / authoritativeData
producer / consumers / callsContracts / publishes / consumes
```

详见 `docs/architecture/metadata-model-v3.md` 迁移附录。

## CLI 工具

```bash
gocell validate [--strict]                           # 验证元数据合规（FMT/TOPO 规则）
gocell scaffold {cell|slice|contract|journey}        # 生成骨架
gocell generate {assembly|indexes|boundaries}        # 从元数据生成产物
gocell check contract-health
gocell check slice-coverage --cell=<cellID>
gocell check assembly-completeness --id=<assemblyID>
gocell check journey-readiness --journey=<journeyID>
gocell check l0-imports --cell=<cellID>
gocell verify {slice|cell|journey|targets}
```

修改 `kernel/governance/` 或 `kernel/metadata/` 后，必须本地运行 `gocell validate` 确认通过再提交。
