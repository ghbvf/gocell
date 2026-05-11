# PR #442 — DevOps 维度审查

## 总体结论

CI 集成基本到位，两个新 verify 脚本已通过 governance.yml 的 glob 发现机制自动接入 `make verify`。无新外部依赖引入，`kernel/scaffold` 删除干净。主要风险点是 governance.yml 的 5 分钟 timeout 在冷缓存环境下可能被两个 heavy verify 脚本撑满；以及 `pkg/pathsafe` 的 Windows 路径没有 CI 主动验证覆盖。

## Finding 列表

### F1 [Cx2] verify-scaffold-{bundle,assembly}.sh 接入 CI 但 governance timeout 存在冷缓存风险

**位置**：`.github/workflows/governance.yml` L47 `timeout-minutes: 5`；`hack/verify-scaffold-bundle.sh` sandbox 分支；`hack/verify-scaffold-assembly.sh` sandbox 分支

**问题**：两个脚本均以 sandbox 默认模式运行，每次都执行 `git worktree add` + `go run ./cmd/gocell scaffold cell` + `go test ./cells/{id}/...`（bundle 脚本）以及额外的 `go build`（assembly 脚本）。governance.yml 的 `timeout-minutes: 5` 是原来所有 verify 脚本以静态检查为主时设定的值。现在增加了两个依赖 `go run`/`go test` 的重量级脚本，冷模块缓存环境（新 runner、缓存失效）下单脚本耗时估计 40–90s，两个串行则 80–180s，叠加原有 ~60s 的静态门总时长逼近或超过 5 分钟。

**证据**：
- `governance.yml` job `verify`：`timeout-minutes: 5`，无独立 scaffold 步骤，走 `make verify` 统一入口。
- `verify.sh` 通过 `find hack -maxdepth 1 -name 'verify-*.sh'` 按字母序串行发现并执行所有脚本，`verify-scaffold-assembly.sh` 和 `verify-scaffold-bundle.sh` 均在列，无 `VERIFY_SKIP` 配置。
- 对比 `verify-codegen-cell.sh`（sandbox 模式只跑 codegen + git diff，无 `go test`），两个新脚本额外跑 `go test ./cells/scaffoldsmoke/...` 和 `go build ./cmd/asmsmoke/...`，是现有最重的 verify 脚本。

**建议**：将 governance.yml 的 `timeout-minutes` 从 5 分钟提升至 10 分钟，或在 `make verify` 级别通过 `VERIFY_SKIP` 将两个脚本移出 governance job 并迁移至 `_build-lint.yml` 的 `others` shard（该 shard 包含 `./cmd/...` 已有 30s+ 预算且允许 `go test`）。后者更合理：scaffold smoke 本质是 CLI e2e，与 `cmd/gocell` 测试同属一个 shard。

**Backlog 登记**：需在 `docs/backlog.md` 登记"governance timeout 重新校准 / scaffold smoke 迁移至 others shard"条目，避免 CI 在冷缓存环境偶发超时被误认为 flake。

---

### F2 [Cx1] verify-scaffold-bundle.sh sandbox 分支存在冗余 EXIT trap

**位置**：`hack/verify-scaffold-bundle.sh` L70–72

**问题**：sandbox 分支先在 `mktemp -d` 后设置 `trap 'rm -rf "$SANDBOX_DIR"' EXIT`（L70），再调用 `git worktree add`（L71），然后立即用更完整的 `trap '... worktree remove ...; rm -rf ...' EXIT`（L72）覆盖。bash 的 `trap` 是替换语义，L70 的 trap 会被 L72 完全替换。如果 L71 的 `git worktree add` 失败，bash 在 `set -euo pipefail` 下立即 EXIT，此时生效的 trap 仍是 L70 版本（只有 `rm -rf`，worktree 未创建，逻辑正确）。当前逻辑功能上没有错误，但 L70 的初始 trap 是多余的——若 L71 失败，`$SANDBOX_DIR` 是一个空目录，`rm -rf` 会清理它；若成功，L72 接管完整清理。

对比 `verify-scaffold-assembly.sh` 的 sandbox 分支（L84–86）直接省略了初始 trap，写法更清晰。两个脚本模式不一致。

**证据**：`git show remotes/origin/develop:hack/verify-scaffold-bundle.sh | grep -n trap` 输出第 50、70、72 行三处 trap，其中 70/72 均绑定 EXIT。

**建议**：删除 L70 的冗余 `trap 'rm -rf "$SANDBOX_DIR"' EXIT`，仅保留 L72 的完整 cleanup trap，与 `verify-scaffold-assembly.sh` 对齐。Cx1，改一行。

**Backlog 登记**：可在下次触及 hack 脚本的 PR 顺手修复，无需独立 backlog。

---

### F3 [Cx2] pkg/pathsafe Windows 实现无 CI 主动覆盖

**位置**：`pkg/pathsafe/nofollow_windows.go`；`.github/workflows/_build-lint.yml` os-smoke job

**问题**：`nofollow_windows.go` 使用 `O_EXCL` 替代 `O_NOFOLLOW`，其注释也说明"leaf-symlink protection on Windows depends on OS-level symlink privilege restrictions"，即 Windows 路径的安全语义弱于 Unix。`_build-lint.yml` 的 `os-smoke` job 仅测试 `runtime/shutdown/...`、`runtime/config/...`、`kernel/governance/...` 三个包，不包含 `pkg/pathsafe`。即使 os-smoke 运行，`pathsafe` 的 Windows 行为差异也不会被检测到。此外 os-smoke 本身是 `continue-on-error: true`（advisory 状态），即使失败也不 block PR。

**证据**：
- `_build-lint.yml` L616–625，Cross-platform unit smoke 命令：`go test -race -count=1 -timeout 5m ./runtime/shutdown/... ./runtime/config/... ./kernel/governance/...`，无 `pkg/pathsafe`。
- `nofollow_windows.go` 无对应集成测试（go build tag 隔离）。

**建议**：将 `pkg/pathsafe/...` 加入 `os-smoke` 的跨平台 smoke 包列表。鉴于 os-smoke 本身已是 advisory，这个改动不会引入新的强 block；但至少可以在 Windows 上编译验证 + 运行 pathsafe 的单元测试，早期发现 API 漂移。

**Backlog 登记**：在 `docs/backlog.md` 登记"os-smoke 加入 pkg/pathsafe 跨平台覆盖"条目。

---

### F4 [Cx1] Docker / Dockerfile 维度

**位置**：N/A

本 PR 不涉及 Dockerfile、docker-compose、容器化配置。无发现。

---

### F5 [Cx1] 构建依赖清洁性确认

**位置**：`go.mod`、`go.sum`

`pkg/pathsafe` 和 `pkg/yamlsafe` 均为纯标准库实现（`os`、`syscall`、`strings`）。`git diff c69dd49f..31070c2f -- go.mod go.sum` 无任何变更。`kernel/scaffold` 的 5 个 Go 文件和 5 个模板文件已全部从 `--name-status D` 确认删除，无残留依赖。CLI 二进制大小变化在 scaffold 合并模板到 `cmd/gocell/app/scaffold.go` 后理论上持平或略增，但未引入新外部依赖，不存在意外膨胀风险。

无需额外处理。

---

### F6 [Cx1] archtest CI 位置确认

**位置**：`tools/archtest/scaffold_bundle_test.go`、`tools/archtest/scaffold_write_funnel_test.go`；`_build-lint.yml` tools shard

两个新 archtest 文件位于 `tools/archtest/`，属于 `_build-lint.yml` tools shard（`pkgs: ./tools/...`）覆盖范围，在每次 PR 和 push 时均会运行。archtest 本身使用 `go/packages` 的类型信息，不依赖外部服务，5 分钟 shard timeout 足够。INVARIANT ID（`SCAFFOLD-BUNDLE-MARKER-01`、`SCAFFOLD-BUNDLE-NO-CODEGEN-LITERAL-01`、`SCAFFOLD-WRITE-FUNNEL-01`）在文件头 CommentGroup 声明，符合 ai-collab.md 命名规范。

无发现。
