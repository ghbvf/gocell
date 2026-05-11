# PR #441 — DevOps 维度审查

## 总体结论

PR 441 是纯内核重构（kernel/cell ISP 拆分 + sealed marker 引入 + sweeper 大改），无 Dockerfile、docker-compose、容器化配置变更。CI 流水线不需要新增步骤，新增的 archtest 文件全部落在 `tools/archtest/` shard，构建期 sealed marker typecheck 在普通 `go build ./...` 阶段完成，与现有 CI jobs 耦合良好。

主要风险点：
1. `.venv` Python symlink 存在于 repo 根目录，导致本地 `go test ./tools/archtest/...` 全量跑时部分 scanner 类 archtest 崩溃（`ModuleScope`/`ImportBan` 的 walk 拒绝 symlink）。该 symlink 已在 `.gitignore` 中，**不影响 CI**（CI runner 做干净 checkout，无 `.venv`），但会误导本地开发者以为 archtest 存在 PR 441 引入的缺陷。
2. `rawparamfixture` 与 `wrapfixture/violation` 两个 fixture 包的 `//go:build archtest_fixture` 构建标签已正确注册到 `KnownNonDefaultTags()`，CI 中 `./tools/...` shard 不携带该标签故不会被误扫描，但此路径对未来维护者是隐性约定。
3. `runtime/command/lifecycle.go` 中 sweeper worker 使用 `context.WithCancel(context.Background())` 代替 OnStart 传入的 `ctx`，该设计决策与容器健康检查 / shutdown grace 有轻微关联，已有 lifecycle_rollback_test.go 正向契约固定，但 ADR 202605102000 的 backlog 项 `LIFECYCLE-OWNER-CTX-PROPAGATION-01` 尚未在 docs/backlog.md 显式登记。

## Finding 列表

### F1 [Cx2] .venv Python symlink 导致本地全量 archtest 崩溃，误导 PR 441 质量判断

**位置**：`/Users/shengming/Documents/code/gocell/.venv`（已 `.gitignore`）；`tools/archtest/outbox_invariants_test.go:1671`；`tools/archtest/kernel_poolstats_location_test.go:48`

**问题**：`.gitignore` 已排除 `.venv/` 目录，但本地磁盘存在该目录，其中包含 `.venv/bin/python -> python3.12` 的 symlink。`scanner.ModuleScope.Files()` 和 `scanner.ImportBan.Run` 在 walk 时遇到 symlink 会返回 `walk: .venv/bin/python is a symlink (refused; archtest scans the static repository structure, real files only)` 错误，导致 `TestMetadataLimitsSingleSource` 和 `TestKERNEL_POOLSTATS_LOCATION_01a_NoLegacyImport` 在本地全量 `go test ./tools/archtest/...` 时 FAIL。

实际验证：PR 441 直接相关的 6 条 archtest（`TestCellIfaceISP*` 三条、`TestCellRawInfraPublicOptionParam01_*` 两条、`TestCellRawInfraWrapperLocation01_*` 两条）全部 PASS，问题与 PR 441 内容无关，是环境污染导致的误判。CI runner 做干净 checkout，无 `.venv`，不受影响。

**证据**：
```
$ go test ./tools/archtest/ -run 'TestKERNEL_POOLSTATS_LOCATION_01a' -count=1 -v
--- FAIL: TestKERNEL_POOLSTATS_LOCATION_01a_NoLegacyImport (0.00s)
    kernel_poolstats_location_test.go:48: scanner.ImportBan.Run(KERNEL-POOLSTATS-LOCATION-01a): walk: .venv/bin/python is a symlink
$ go test ./tools/archtest/ -run 'TestCellIfaceISP|TestCellRawInfra' -count=1
ok  github.com/ghbvf/gocell/tools/archtest  1.471s
```

**建议**：在 `scanner.ModuleScope` 的 walk 实现中，对遇到 symlink 时从 fatal error 降级为 skip（与 `vendor/testdata/.git/node_modules` 的自动过滤一致），或在 `ModuleScope` 中额外过滤掉 `.gitignore` 所忽略的路径。两者均需评估是否改变了 scanner 的 fail-closed 语义。

**Backlog 登记建议**：在 `docs/backlog.md` 登记"archtest ModuleScope symlink 处理：降级 skip 或 .gitignore 联动过滤"，触发条件：下次 `scanner.ModuleScope` 或 `scanner.ImportBan` 有改动时一并处理。

---

### F2 [Cx1] rawparamfixture / wrapfixture 构建标签对 CI shard 边界的隐性依赖

**位置**：`tools/archtest/internal/rawparamfixture/cell.go:1`（`//go:build archtest_fixture`）；`tools/archtest/internal/wrapfixture/violation/violation.go:1`；`tools/archtest/internal/wrapfixture/violation/dotimport.go:1`；`tools/archtest/internal/typeseval/buildtags.go`

**问题**：三个 fixture 文件均以 `//go:build archtest_fixture` 标记，正确排除在 `go build ./...` 和 `go test ./...`（无特殊 tag）之外。`KnownNonDefaultTags()` 已将 `{"archtest_fixture"}` 纳入已知 tag 集合，并添加了注释说明其用途。

现有设计依赖两个隐性约定：
- CI `tools` shard 使用 `go test ./tools/...`（无 `-tags=archtest_fixture`），因此 fixture 不会被误扫描为真实 repo 文件。
- `TestCellRawInfraPublicOptionParam01_ScannerCatchesViolation` 和 `TestCellRawInfraWrapperLocation01_ScannerDetectsViolation` 显式通过 `typeseval.SharedResolver(..., []string{"archtest_fixture"}, "./tools/archtest/internal/rawparamfixture")` 加载 fixture，绕过 `restrictToCellRoots` 路径过滤。

该约定无任何 CI job 层面的文档或注释说明"为何不把这两个 fixture 加进集成测试 tag 列表"。未来维护者若想对 `KnownNonDefaultTags` 做清理，可能会误删 `archtest_fixture` 条目。

**证据**：`tools/archtest/internal/typeseval/buildtags.go:36-38` 中 `archtest_fixture` 条目仅有一行内联注释，无 backlog 引用。

**建议**：在 `KnownNonDefaultTags()` 的 `archtest_fixture` 条目注释中显式引用守护它的 archtest 测试函数名（`TestCellRawInfraPublicOptionParam01_ScannerCatchesViolation` 和 `TestCellRawInfraWrapperLocation01_ScannerDetectsViolation`），让意图自文档化，降低误删风险。Cx1，单行注释扩展即可。

**Backlog 登记建议**：无需独立 backlog，下次触及 `buildtags.go` 时顺手补充。

---

### F3 [Cx2] SweeperLifecycle worker ctx 语义与容器 shutdown grace 的关联未在运维文档中体现

**位置**：`runtime/command/lifecycle.go:80`（`context.WithCancel(context.Background())`）；`runtime/command/lifecycle_rollback_test.go:42`（`TestSweeperLifecycle_StartupFailRollback`）；ADR `docs/architecture/202605102000-adr-lifecycle-hook-ctx-semantics.md`

**问题**：`SweeperLifecycle.Start` 显式使用 `context.Background()` 衍生 worker ctx，而非 OnStart 传入的 startup-deadline ctx。这是正确的设计（与 uber-go/fx hook 语义对齐：OnStart 返回后 worker 不应随 startup ctx 取消），已有 lifecycle_rollback_test.go 固定该契约。

DevOps 关注点：在容器化部署场景中，bootstrap 的 stop budget 决定了容器收到 SIGTERM 后的 graceful shutdown 时间窗口。若 `SweeperLifecycle.Stop` 在 stop budget 内超时（`ctx.Done()` 先于 sweeper goroutine 退出），则 `Stop` 返回 `ctx.Err()`，bootstrap 记录超时但继续向上层推进（LIFO rollback 语义）。sweeper goroutine 此时仍在运行，直到 Go runtime 随容器进程退出被强制终止。

该行为在当前 Dockerfile / docker-compose 无 `stop_grace_period` 显式配置的情况下使用 Docker 默认 10s 超时，足以覆盖 sweeper 默认 30s tick 间隔中 `ScanActive` + `Queue.Ack` 单次调用的时长（通常 <1s）。但若 `ScanActive` 在高负载 DB 下延迟超过 10s，sweeper goroutine 会被强制杀死，可能导致正在处理的 AckTimeout 事务未提交。

**证据**：
- `runtime/command/lifecycle.go:80`：`runCtx, cancel := context.WithCancel(context.Background())`
- 现有 `tests/e2e/docker-compose.e2e.yaml` 中 corebundle 服务无 `stop_grace_period` 字段，使用 Docker 默认 10s
- ADR `202605102000-adr-lifecycle-hook-ctx-semantics.md` 中 backlog 项 `LIFECYCLE-OWNER-CTX-PROPAGATION-01` 已记录替代方案，但该条目未在 `docs/backlog.md` 中体现

**建议**：
1. 在 `tests/e2e/docker-compose.e2e.yaml` 的 corebundle 服务中显式设置 `stop_grace_period: 30s`（与 sweeper 默认 30s tick 间隔对齐，给 bootstrap shutdown 足够时间），避免将来 sweeper 配置延长 tick 间隔时出现无声的 grace 不足。
2. 将 `LIFECYCLE-OWNER-CTX-PROPAGATION-01` 登记到 `docs/backlog.md`，触发条件为"sweeper tick 间隔 > 10s 或 DB ScanActive P99 > 5s 时评估"。

**Backlog 登记建议**：在 `docs/backlog.md` 登记"corebundle docker-compose.e2e.yaml 显式 stop_grace_period + LIFECYCLE-OWNER-CTX-PROPAGATION-01 backlog 登记"。

---

### F4 [Cx2] archtest tools shard 时长影响：新增 type-aware scan（`packages.Load`）开销评估

**位置**：`.github/workflows/_build-lint.yml`，tools shard（`pkgs: ./tools/...`，`timeout: 5m`）；`tools/archtest/cell_public_option_param_test.go`（`TestCellRawInfraPublicOptionParam01_RealRepoClean`，调用 `typeseval.SharedResolver(root, false, nil, "./...")`）；`tools/archtest/wrapper_location_test.go`（`TestCellRawInfraWrapperLocation01_RealRepoClean`，同样调用 `SharedResolver(root, false, nil, "./...")`）

**问题**：PR 441 新增两条 type-aware archtest（`CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01` 和 `CELL-RAW-INFRA-WRAPPER-LOCATION-01`），各自调用一次 `typeseval.SharedResolver(root, false, nil, "./...")`（全模块类型图加载）。`SharedResolver` 内部使用 `packages.Load` + `sync.Once` 缓存，**同一测试进程的两次 `nil` tag 调用只做一次真实加载**，代价复用。

但两条测试还分别通过 `SharedResolver(root, false, []string{"archtest_fixture"}, "...")` 加载 `archtest_fixture` tag 集合（检测 fixture 的 detection 测试），这是**第二个独立 SharedResolver 实例**（tag 不同 → 不同 cache key），因此实际产生两次 `packages.Load` 调用：一次 `./...`（默认 tag）+ 一次 `rawparamfixture`（archtest_fixture tag）。

本地实测（Apple M 系列）：PR 441 相关 6 条测试在 `go test ./tools/archtest/ -run 'TestCellIfaceISP|TestCellRawInfra'` 下耗时 ~1.5s，全量 tools shard 约 77-81s（其中大部分来自其他 type-graph-load 测试，已在 `slowgate/allowlist.txt` 登记）。PR 441 新增部分估计新增 <5s，在 5 分钟 shard budget 内有足够余量。

但 GHA ubuntu-latest 2-CPU runner 的实际耗时通常为 laptop 的 5-15 倍，PR 441 贡献的增量在 CI 约为 5-75s 区间。若 tools shard 已接近 5 分钟边界，此增量可能触发 slowgate 15s 单测警告（若两条 SharedResolver 加载串行执行）。

**证据**：
- `tools/slowgate/allowlist.txt` 中已有多条 `type-graph-load` 类条目（`TestClockInjectionCallsite`、`TestProdClockInjection`、`TestSVCTOKEN_CALLER_CELL_REQUIRED_01` 等），说明 tools shard 的 type-graph-load 压力已经存在。
- PR 441 未在 `slowgate/allowlist.txt` 中为 `TestCellRawInfraPublicOptionParam01_RealRepoClean` 或 `TestCellRawInfraWrapperLocation01_RealRepoClean` 预留 slowgate 豁免条目。

**建议**：CI 合并后观察 tools shard 实际耗时。若 `TestCellRawInfraPublicOptionParam01_RealRepoClean` 或 `TestCellRawInfraWrapperLocation01_RealRepoClean` 触发 slowgate 15s 告警，应按 `allowlist.txt` 规范添加豁免条目并附说明（理由：全模块 `packages.Load` 是设计性成本，已被 SharedResolver 缓存复用但首次加载仍耗时）。当前无需预防性添加，等 CI 数据。

**Backlog 登记建议**：在 `docs/backlog.md` 登记"tools shard 累积 packages.Load 测试 slowgate 校准 — 待 PR 441 首次 CI 跑出耗时后评估"。

---

### F5 [Cx1] Dockerfile / docker-compose 维度

**位置**：N/A

本 PR 不涉及 Dockerfile、docker-compose.yml、docker-compose.test.yml、CI workflow 文件变更。kernel/command 和 kernel/cell 层的接口拆分与 sealed marker 均为纯类型系统变更，无运行时环境变量引入、无新配置项、无端口/卷/健康检查变更。

无发现。

---

### F6 [Cx1] Kernel 覆盖率门（≥90%）通过确认

**位置**：`.github/workflows/_build-lint.yml` L226-249（Kernel coverage gate）；新增包 `kernel/outbox`（cell_marker.go）、`kernel/persistence`（cell_marker.go）、`kernel/command`（sweeper.go 大改）

**实测数据**（本地 `go test -cover ./kernel/...`）：
- `kernel/cell`：94.8%
- `kernel/command`：95.9%
- `kernel/outbox`：96.2%
- `kernel/persistence`：100.0%

所有受 PR 441 影响的 kernel 包均高于 90% 门槛，CI 覆盖率门不受影响。

无发现。
