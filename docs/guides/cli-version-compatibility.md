# CLI ↔ Framework 版本兼容性

本文档说明 `gocell` CLI 与 `github.com/ghbvf/gocell` framework module 的版本对应关系。

## 发布模型：原子同 tag

`cmd/gocell/`（`github.com/ghbvf/gocell/cmd/gocell`）是独立 Go module，与 framework 由
release 流水线**原子同 tag 发布**（#1088 M7）。每个 stable release 的 CLI 版本号与
framework 版本号始终相同：`gocell` CLI `vX.Y.Z` 对应 framework `vX.Y.Z`。

## CLI 获取方式

### 预编译二进制（推荐）

预编译产物（linux/darwin × amd64/arm64 tarball + SHA256 checksums）自**首个搭载本发布流水线
的 stable release 起**随每个 GitHub Release 附带。从 GitHub Releases 页面下载对应平台的
tarball，解压后将 `gocell` 二进制放入 `$PATH`，并用 `checksums.txt` 校验完整性：

```bash
# 从 https://github.com/ghbvf/gocell/releases/tag/vX.Y.Z 下载对应平台 tarball + checksums.txt。
# 归档名格式：gocell_<version>_<os>_<arch>.tar.gz（version 无前导 v，例 gocell_0.2.0_linux_amd64.tar.gz）。
# checksums.txt 含全部 4 个平台行；只下载目标平台时，仅校验该文件那一行：
f=gocell_<version>_linux_amd64.tar.gz                 # 按实际 version/平台替换
grep " ${f}\$" checksums.txt | sha256sum -c -         # macOS: grep " ${f}\$" checksums.txt | shasum -a 256 -c -
tar -xzf "$f"
mv gocell /usr/local/bin/
```

### Docker（推荐，无需本地 Go 环境）

multi-arch docker 镜像（linux/amd64 + linux/arm64）同样自首个搭载本发布流水线的 stable
release 起提供：

```bash
docker pull ghcr.io/ghbvf/gocell:<tag>
docker run --rm ghcr.io/ghbvf/gocell:<tag> version
```

`latest` tag 始终指向最新 stable release；`develop` snapshot **没有**对应 docker 镜像。

### 源码安装（fallback）

当需要使用尚未发布预编译产物的代码（如本地开发、贡献代码），或所在环境无法访问 GitHub
Releases 时，可从源码安装：

```bash
git clone https://github.com/ghbvf/gocell.git
cd gocell
go install ./cmd/gocell
```

> `go install github.com/ghbvf/gocell/cmd/gocell@vX.Y.Z` **自首个搭载 #2045 发布流水线的
> release 起可用**（不是所有 v0.1.x，v0.1.0 之前或早于该流水线的 tag 不包含独立子 module
> commit，工具链无法解析）。release 流水线在发布时经 `modrelease.StripReplaceAndPin`
> （`InstallableBinaries`，#2045）剥离本地 `replace` 指令并将内部 require 固定到已发布版本，
> 再在独立子 module commit 上打 `cmd/gocell/vX.Y.Z` tag，使 Go 工具链可直接解析该版本。用法：
>
> ```bash
> go install github.com/ghbvf/gocell/cmd/gocell@vX.Y.Z
> ```
>
> go-install 路径与预编译二进制路径的 `gocell version` 输出（`cli_version`）一致：go-install
> 构建时 Go 工具链将模块版本 vX.Y.Z 写入 `debug.ReadBuildInfo().Main.Version`，`toolVersion()`
> 据此返回正确版本，与 goreleaser 预编译二进制的 ldflags 注入路径输出相同。
>
> 源码安装（本地 clone）仍作为开发 / 预发布版本的 fallback 可用。

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
- **重要**：v0.x 阶段，当 framework 版本超出 `compatible_framework_range` 时，CLI **不会**
  触发任何机器拒绝行为——不会 fail-fast、不会拒绝启动，照常运行。超出范围仅作为 best-effort
  建议提示，不构成 hard guarantee 或强制阻断。

v1.0 GA 后，`compatible_framework_range` 的语义切换为 **SemVer hard guarantee**：CLI 与
framework 版本超出声明范围将不被支持，届时 CLI 可能在启动时主动 fail-fast。

## 版本兼容矩阵

下表记录每次 stable release 的 CLI ↔ framework 兼容关系。**每次 stable release 追加一行**；
不删除历史行（历史行是消费方升级决策的参考）。

| gocell CLI version | Compatible framework (`github.com/ghbvf/gocell`) | Notes |
|--------------------|-------------------------------------------------|-------|
| v0.1.0 | >=v0.1.0 <v0.2.0 | First stable release（co-tagged，best-effort intent；预编译产物自首个搭载发布流水线的 release 起提供，此 tag 不含；`go install @version` 自首个搭载 #2045 流水线的 release 起可用，v0.1.0 本身可能早于该流水线） |

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
