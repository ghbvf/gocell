# API 版本策略

## 何时升级版本

以下变更必须新建版本目录和新 contract ID：

- 删除或重命名响应字段
- 改变字段类型或枚举语义
- 改变鉴权要求、幂等语义、分页语义
- 改变错误码语义或 HTTP 状态码

新增可选响应字段可以留在当前版本。新增必填请求字段必须升级版本。

## 兼容窗口

GoCell 当前 pre-GA，不保留旧 Go API shim。HTTP / event / command wire contract
仍按版本目录隔离：破坏式 wire 变更用新版本，不在旧版本上偷改语义。

## 内部 API

`/internal/v1/` 是服务间控制面，不是绕过版本策略的后门。internal contract 同样需要：

- contract.yaml 声明鉴权和 caller
- path、schema、handler、generated code 同步
- 破坏式 wire 变更新增版本

## Setup / bootstrap 路径

没有顶级 `/api/v1/setup/` 命名空间。首启动引导端点和所有业务端点一样挂在所属 Cell 的版本化
前缀下，遵循同一 `/api/v{N}/{cell}/...` 约定：

- bootstrap admin：`/api/v{N}/{cell}/setup/admin`（如 `/api/v1/access/setup/admin`）
- setup status：`/api/v{N}/{cell}/setup/status`

「setup」的特殊性在鉴权与生命周期，不在路径位置：

- 鉴权用 `auth.bootstrap:true`（HTTP Basic + 环境凭据），不是 JWT/RBAC。
- admin 创建后端点返回 410 Gone（一次性引导边界）。
- pre-auth 阶段经 `X-Tenant-ID` header 解析租户（此时还没有 JWT claim）。

bootstrap admin 路径形状由单一谓词 `metadata.IsBootstrapPath`
（`^/api/v\d+/[^/]+/setup/admin$`，强制带 cell 段）锁定；治理规则 `FMT-28` 只允许
`auth.bootstrap:true` 出现在匹配该谓词的路径上，缺 cell 段的 `/api/v1/setup/admin` 被
fail-closed 拒绝。破坏式 wire 变更照常走所属 Cell 的版本目录升级，与上文规则一致。

参考 ADR：`docs/architecture/202605061600-adr-bootstrap-admin-boundary.md`、
`docs/architecture/202606021200-1160-adr-pre-auth-tenant-header-contract.md`。
