# GoCell 统一命名基线

这份文档只定义当前仓库需要长期遵守的最小命名规范。

历史评审、阶段性 spec、归档材料中的旧叫法不自动视为现行规则。`archive/`、`reviews/`、`evidence/` 下的内容仅作历史记录。

## 规范来源

发生冲突时，按以下顺序判断：

1. 当前元数据真相文件：`cells/**/cell.yaml`、`cells/**/slice.yaml`、`contracts/**/contract.yaml`、`journeys/*.yaml`
2. 当前治理代码：`kernel/governance/*`
3. 本文
4. 其他现行文档

## 1. 硬规则

### 1.1 实体 ID

以下 ID 统一使用 `kebab-case`：

- Cell ID：`access-core`
- Slice ID：`session-login`
- Assembly ID：`core-bundle`
- Actor ID：`platform-team`

Journey ID 统一使用 `J-{kebab-case}`：

- `J-session-refresh`
- `J-config-rollback`

Contract ID 统一使用小写点分格式：

- `http.auth.login.v1`
- `event.config.changed.v1`

不使用：

- `access_core`
- `sessionlogin` 作为 Slice ID
- `http/auth/login/v1`
- `J_session_refresh`

### 1.2 Metadata YAML 字段

现行 metadata YAML 多词字段统一使用 `camelCase`：

- `belongsToCell`
- `contractUsages`
- `consistencyLevel`
- `journeyId`
- `updatedAt`

不使用：

- `belongs_to_cell`
- `contract_usages`
- `journey_id`

### 1.3 禁用旧字段名

以下字段名在现行元数据、模板、生成物、架构文档中禁用：

- `cellId`
- `sliceId`
- `contractId`
- `assemblyId`
- `ownedSlices`
- `authoritativeData`
- `producer`
- `consumers`
- `callsContracts`
- `publishes`
- `consumes`

这些名称只允许出现在：

- 历史归档文档
- 评审记录中对旧设计的引用
- 显式测试旧兼容行为的测试夹具

### 1.4 Slice 目录

一个 Slice 只能有一个规范目录：

- `cells/{cell-id}/slices/{slice-id}/`

规则：

- 目录名必须等于 `slice.id`
- 目录名使用 `kebab-case`
- `slice.yaml` 与该 Slice 的 Go 实现应收敛到同一规范目录
- 不允许继续新增并行兄弟目录，例如 `session-login/` 与 `sessionlogin/` 并存

### 1.5 生成物

`generated/boundary.yaml` 只能包含文档定义的派生字段：

- `generatedAt`
- `sourceFingerprint`
- `exportedContracts`
- `importedContracts`
- `smokeTargets`

不再生成旧字段 `assemblyId`。

### 1.6 Go 标识符

Go 代码遵循 Go idiom：

- 导出标识符用 `PascalCase`
- 非导出标识符用 `camelCase`
- 常见缩略词保持大写

本节只适用于 Go 代码中的类型名、变量名、函数名、字段名。
不适用于 metadata YAML 字段名；metadata 字段仍遵循本文 1.2 和 1.3。

统一缩略词：

- `ID`
- `URL`
- `URI`
- `HTTP`
- `JWT`
- `OIDC`
- `RBAC`
- `JSON`
- `YAML`
- `SQL`
- `IP`
- `TTL`
- `HMAC`
- `JWKS`

示例：

- `RequestID`，不是 `RequestId`
- `userID`，不是 `userId`
- `JWTIssuer`，不是 `JwtIssuer`
- `JWKSURI`，不是 `JwksUri`

## 2. 建议

以下是推荐约定，不作为 blocking 规则；若外部协议或现有兼容性要求不同，可按外部约束处理。

### 2.1 外部字段

- JSON / Query / Path 字段优先使用 `camelCase`
- DB 字段、表列名、轻量 key-value key 优先使用 `snake_case`
- 环境变量使用 `SCREAMING_SNAKE_CASE`，项目自有变量优先 `GOCELL_*`
- 错误码值使用 `ERR_*` + `SCREAMING_SNAKE_CASE`

### 2.2 Go 包与文件

- Go package 名使用小写连续词，例如 `sessionlogin`
- Go 文件名优先 `lower_snake_case.go` / `*_test.go`

注意：

- `session-login` 是 Slice 名和目录名
- `sessionlogin` 只适用于 Go package 名，不能反过来替代 Slice 名

## 3. 当前仓库迁移项

以下内容是当前仓库的收敛任务，不是永久规则本身：

- 合并双目录 Slice 结构，消除 `session-login/` 与 `sessionlogin/` 并存
- 将现行文档中的 `sessionlogin` / `sessionrefresh` / `sessionvalidate` 收敛为规范 Slice 名
- 将现行文档中的 `audit-write` 收敛为 `audit-append`
- 将现行文档中的 `config-manage` 收敛为 `config-read` + `config-write`
- 更新 `boundary.yaml` 生成模板与测试，移除 `assemblyId`
- 给治理工具补一条检查，阻止双目录和旧字段名回流

## 4. PR 检查点

每个涉及命名的 PR 至少确认以下几点：

- 新增或修改的现行文档没有引入旧字段名
- 新增或修改的模板和生成物没有产出禁用字段名
- 新增 Slice 没有创建并行命名目录
- 新增 Go 标识符符合 Go 缩略词规则
- 文档里讨论 Slice 时使用规范 Slice ID，而不是 package 名
