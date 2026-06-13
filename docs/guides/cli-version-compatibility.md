# CLI ↔ Framework 版本兼容性

本文档说明 `gocell` CLI 与 `github.com/ghbvf/gocell` framework module 的版本对应关系。

## 发布模型：原子同 tag

`cmd/gocell/`（`github.com/ghbvf/gocell/cmd/gocell`）是独立 Go module，与 framework 由
release 流水线**原子同 tag 发布**（#1088 M7）。每个 stable release 的 CLI 版本号与
framework 版本号始终相同：`gocell` CLI `vX.Y.Z` 对应 framework `vX.Y.Z`。

## `gocell version` 输出说明

运行 `gocell version` 会输出三个字段：

```
cli_version:                v0.1.0
framework_version:          v0.1.0
compatible_framework_range: >=v0.1.0 <v0.2.0
```

| 字段 | 含义 |
|------|------|
| `cli_version` | 当前安装的 CLI 版本（与 framework 同 tag） |
| `framework_version` | 本 CLI 构建时链接的 framework 版本（与 `cli_version` 相同） |
| `compatible_framework_range` | 本 CLI 运行时预期兼容的 framework 版本范围（从单一注入的 version 常量派生，非手工维护） |

`compatible_framework_range` 表达的是 CLI（`gocell validate` / `gocell check` / `gocell generate`
等子命令）与外部 Cell 仓库所依赖的 framework 之间的运行时兼容窗口，用于校验版本匹配，防止
CLI 版本与项目依赖的 framework 版本差距过大导致 schema / governance 规则不一致。

### v0.x 阶段的语义

v0.x 阶段 `compatible_framework_range` 是 **best-effort intent**，而非 hard guarantee：

- GoCell 处于 pre-GA 阶段，CLI 与 framework 的 Go 公开符号（authoring schema 和 exported
  Go API）遵循 intent-level SemVer（见 ADR
  `docs/architecture/202606131200-1088-adr-go-api-authoring-schema-semver-policy.md`）。
- 如果你的 framework 版本在范围内，期望能正常工作；超出范围时，建议先升级 CLI 或锁定
  framework 版本再运行。

v1.0 GA 后，`compatible_framework_range` 的语义切换为 **SemVer hard guarantee**：CLI 与
framework 版本超出声明范围将不被支持，届时 CLI 可能在启动时主动 fail-fast。

## 版本兼容矩阵

下表记录每次 stable release 的 CLI ↔ framework 兼容关系。**每次 stable release 追加一行**；
不删除历史行（历史行是消费方升级决策的参考）。

| gocell CLI version | Compatible framework (`github.com/ghbvf/gocell`) | Notes |
|--------------------|-------------------------------------------------|-------|
| v0.1.0 | >=v0.1.0 <v0.2.0 | First stable release（co-tagged，best-effort intent） |

## 与 release checklist 的关系

每次 stable release 时，release checklist 需要：

1. 在本表追加一行，填写新版本及其 `compatible_framework_range`（从 `gocell version` 输出复制）。
2. 确认 `compatible_framework_range` 由 version 注入常量**自动派生**，无需手动修改代码中的范围字符串。

## 如何校验版本匹配

在外部 Cell 仓库中，运行以下命令校验当前安装的 CLI 与项目依赖的 framework 是否兼容：

```bash
# 查看 CLI 声明的兼容范围
gocell version

# 查看项目当前依赖的 framework 版本
go list -m github.com/ghbvf/gocell

# 确认 framework 版本在 compatible_framework_range 内
```

详细安装和使用指南见 `docs/guides/cell-external-repo-quickstart.md`。

## 相关文档

- ADR（轴 A SemVer 政策）: `docs/architecture/202606131200-1088-adr-go-api-authoring-schema-semver-policy.md`
- 外部仓库快速上手: `docs/guides/cell-external-repo-quickstart.md`
- Wire 契约版本策略（轴 B）: `.claude/rules/gocell/api-versioning.md`
