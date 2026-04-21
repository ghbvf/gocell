# GoCell 协作说明

> 层内规则见各层目录下的 CLAUDE.md（Claude Code 自动加载）。本文件只保留全局约束。

## 工作方式

- 修改前先查看 README.md 与 docs/
- 提交信息遵循 Conventional Commits
- 涉及功能或行为变更时，同步更新对应文档
- 被 `.gitignore` 忽略的文件禁止 `git add -f`
- Review 和重构时不考虑向后兼容——当前只有 gocell 自身，没有外部调用方

## 架构目录

```
kernel/       — Cell/Slice 运行时 + 治理工具（底座灵魂）
cells/        — Cell 实现（access-core / audit-core / config-core）
contracts/    — 跨 Cell 边界契约（按 {kind}/{domain-path}/{version}/ 组织）
journeys/     — Journey 验收规格（J-*.yaml）+ status-board.yaml
assemblies/   — 物理打包配置（assembly.yaml）
fixtures/     — 测试夹具（fixture-*.yaml）
runtime/      — 通用运行时（http / auth / worker / observability）
adapters/     — 外部系统适配（postgres / redis / rabbitmq / websocket / s3 / oidc）
pkg/          — 共享工具包（errcode / ctxkeys / httputil / query）
cmd/          — CLI 入口（gocell validate / scaffold / generate / check / verify）
examples/     — 示例项目
generated/    — 工具生成产物（禁止手工编辑）
actors.yaml   — 外部 Actor 注册
```

## 层间依赖禁令（摘要）

详细规则见各层 `CLAUDE.md`。核心约束：

| 层 | 允许依赖 | 严禁依赖 |
|----|---------|---------|
| `kernel/` | 标准库 + `pkg/` + `gopkg.in/yaml.v3` | `runtime/` `adapters/` `cells/` |
| `runtime/` | `kernel/` + `pkg/` | `cells/` `adapters/` |
| `adapters/` | `kernel/` + `runtime/` + `pkg/` | `cells/` |
| `cells/` | `kernel/` + `runtime/` | `adapters/`（通过接口解耦） |
| `pkg/` | 标准库 | 所有其他层 |
| `examples/` | 所有层 | — |

## 预提交检查清单

1. `golangci-lint run ./修改的包/...` — 确认 **0 issues** 再 commit+push
2. `gofmt -w` 修格式问题，不要手动对齐
3. 改了导出签名：`go build -tags=integration ./...`

## 依赖选择原则

实现外部协议/标准（密码学、签名、OIDC、migration、可观测性导出等）**必须**优先使用官方或成熟开源库，禁止自建；实现 GoCell 领域逻辑（Cell/Slice 模型、治理规则、outbox 接口等）保留自建。

## 对标框架参考

新建或重构 `kernel/`、`cells/`、`runtime/`、`adapters/` 下的模块时，先 `WebFetch` 对标源码，提交信息注明 `ref: {framework} {file}` + 采纳/偏离理由。

| 模块 | 对标框架 | 参考路径 |
|------|---------|---------|
| Cell/Slice 声明 + 生命周期 + 校验 | Kubernetes | `staging/src/k8s.io/api/core/v1/types.go` |
| Cell 运行时 | Uber fx | `fxevent/`, `lifecycle.go` |
| 代码生成 | go-zero goctl | `tools/goctl/` |
| 中间件 | Kratos | `middleware/` |
| 配置热更新 | go-micro | `config/` |
| 事件驱动 | Watermill | `message/router.go` |

详见 `docs/references/framework-comparison.md`。

## Sandbox 提权

`git push/pull/fetch` 和 `gh` 命令须用 `dangerouslyDisableSandbox: true`。
