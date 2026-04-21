# contracts/ 层规则

contracts/ 存放跨 Cell 边界契约，按 `{kind}/{domain-path}/{version}/` 组织。

## 修改后必做

修改 `contracts/` 下任何文件后，**必须**运行：

```bash
go run ./cmd/gocell validate
```

确认元数据一致性（TOPO-07 actor 级别、FMT 格式等）通过后再提交。

## 字段规则

| 字段类型 | 规则 |
|---------|------|
| `lifecycle`（`draft` / `active` / `deprecated`） | 治理字段，**允许**写在 contract.yaml 中 |
| 动态状态字段（`readiness` / `risk` / `blocker` 等） | **禁止**出现在 contract.yaml，只写在 `journeys/status-board.yaml` |

## contractUsages.role 对照

| contract kind | 提供方 role | 消费方 role |
|---------------|------------|------------|
| `http` | `serve` | `call` |
| `event` | `publish` | `subscribe` |
| `command` | `handle` | `invoke` |
| `projection` | `provide` | `read` |

## Deprecation 规则

- 废弃合约需同步通知所有 `contractUsages` 中声明的消费方
- Deprecation 保留期 **至少 2 个 Sprint（4 周）** 后再删除
- 不兼容变更（删除字段、修改字段类型）需版本化：升至 `v2/v3`

## 禁止的旧字段名

以下字段名已废弃，使用会被 `gocell validate` 拦截：

```
cellId / sliceId / contractId / assemblyId / ownedSlices / authoritativeData
producer / consumers / callsContracts / publishes / consumes
```

详见 `docs/architecture/metadata-model-v3.md` 迁移附录。
