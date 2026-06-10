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
