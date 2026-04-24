# GoCell 统一命名基线

这份文档只定义当前仓库需要长期遵守的最小命名规范。历史评审、阶段性 spec、归档材料中的旧叫法不自动视为现行规则；`docs/archive/`、`docs/reviews/`、`docs/plans/`、`evidence/` 下的内容仅作历史记录。

## 规范来源

发生冲突时，按以下顺序判断：

1. 当前元数据真相文件：`cells/**/cell.yaml`、`cells/**/slice.yaml`、`contracts/**/contract.yaml`、`journeys/*.yaml`
2. 当前治理代码：`kernel/governance/*`
3. 本文
4. 其他现行文档

## 1. 硬规则

### 1.1 Cell / Slice / Assembly ID

Cell ID、Slice ID、Assembly ID 统一使用 no-dash 小写格式：只允许小写字母和数字，不使用连字符、下划线、驼峰或大小写混合。

现行示例：

- Cell ID：`accesscore`、`auditcore`、`configcore`
- Slice ID：`sessionlogin`、`auditappend`、`configwrite`
- Assembly ID：`corebundle`

对应目录名必须等于 ID：

- `cells/{cell-id}/cell.yaml`
- `cells/{cell-id}/slices/{slice-id}/slice.yaml`
- `assemblies/{assembly-id}/assembly.yaml`

`gocell validate --strict` 负责阻断回流：

- `FMT-16`：目录名不得含连字符
- `FMT-17`：`allowedFiles[0]` 必须指向规范 slice 目录
- `FMT-C1`：`cell.yaml` 的 `id` 不得含连字符
- `FMT-A1`：`assembly.yaml` 的 `id` 不得含连字符
- `DOC-NAME-01`：活动文档不得出现 `docs/architecture/naming-guard.yaml` 中声明的旧 literal

### 1.2 Actor / Journey / Contract ID

Actor ID 是外部身份键，不纳入 no-dash 实体 ID 规则；例如 `platform-team` 是合法 Actor。

Journey ID 使用 `J-` 前缀加小写业务 token，例如 `J-ssologin`、`J-configrollback`、`J-ordercreate`。

Contract ID 使用小写点分格式，例如：

- `http.auth.login.v1`
- `event.config.changed.v1`
- `http.device.command.enqueue.v1`

Contract ID 是协议边界名，不得被当作 Cell/Slice/Assembly ID 的命名先例。

### 1.3 Metadata YAML 字段

现行 metadata YAML 多词字段统一使用 `camelCase`：

- `belongsToCell`
- `contractUsages`
- `consistencyLevel`
- `journeyId`
- `updatedAt`

旧字段名在现行元数据、模板、生成物、架构文档中禁用：

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

这些名称只允许出现在历史归档、评审记录、或显式测试旧兼容行为的测试夹具中。

### 1.4 生成物

`generated/boundary.yaml` 只能包含文档定义的派生字段：

- `generatedAt`
- `sourceFingerprint`
- `exportedContracts`
- `importedContracts`
- `smokeTargets`

不再生成旧 assembly 字段名。

### 1.5 Go 标识符

Go 代码遵循 Go idiom：

- 导出标识符用 `PascalCase`
- 非导出标识符用 `camelCase`
- 常见缩略词保持大写

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

- `RequestID`
- `userID`
- `JWTIssuer`
- `JWKSURI`

## 2. 文档命名治理

`DOC-NAME-01` 由 `gocell validate --strict` 执行，扫描活动文档、模板、示例 README 和 backlog。命中旧 literal 时输出文件、行号、旧值和替换值，并以 error 阻断。

旧 literal 的唯一清单在 `docs/architecture/naming-guard.yaml`。正文不重复列出旧名，避免让历史拼写继续扩散。

不纳入阻断范围：

- `docs/archive/**`
- `docs/reviews/**`
- `docs/plans/**`
- `evidence/**`

## 3. PR 检查点

每个涉及命名的 PR 至少确认以下几点：

- 新增或修改的现行文档没有引入旧 literal
- 新增或修改的模板和生成物没有产出禁用字段名
- 新增 Cell、Slice、Assembly 没有创建并行命名目录
- 新增 Go 标识符符合 Go 缩略词规则
- 文档讨论 Slice 时使用规范 Slice ID，而不是 Go package 或旧拼写
