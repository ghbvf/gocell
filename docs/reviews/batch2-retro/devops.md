# Batch2 Retrospective — DevOps 席位

## CI 主张兑现

| Plan 主张 | 现状 | 证据 |
|---|---|---|
| PR-CFG-D: unconditional-skip 门禁 | 已兑现。`hack/verify-unconditional-skip.sh` 存在，`make verify` 通过 glob 自动发现执行，Governance workflow 每次 PR/push 触发 | `hack/verify-unconditional-skip.sh:10`, `governance.yml:28`, `hack/make-rules/verify.sh` glob discover |
| PR-CFG-J: docker-compose e2e harness 真跑（非仅 build） | 已兑现。CI `e2e` job 完整 4 步：`up -d --wait` postgres+redis → migrate `--exit-code-from` → `up -d --wait` corebundle → `go test -tags=e2e -json ... | e2egate`，e2egate 零执行即 fail | `_build-lint.yml:306-338`, `tools/e2egate/cmd/e2egate/` |
| PR-CFG-M: boundary regenerate-and-diff CI 门禁 | **实现路径偏离计划**。Plan 描述 `_build-lint.yml` 遍历 `assemblies/*/` 执行 `generate --boundary-only` + `git diff --exit-code`；实际落地为 `go run ./cmd/gocell verify generated`（调用 `generatedverify.Verify`），语义等价（drift 检测）但 `--boundary-only` flag 从未实现（`main_test.go:274-281` 显式测试该 flag **不存在**）。门禁本身有效 | `_build-lint.yml:165-167`, `cmd/gocell/app/main_test.go:274-281`, `verify.go:233-246` |
| PR-CI-5 (verify-panic-registered) + PR-CI-6 (verify-prod-duration) 纳入 lint shard | 已兑现。两脚本均在 `hack/` 下，`make verify` 自动发现运行；`governance.yml` 每次 PR/push 触发 `make verify` | `hack/verify-panic-registered.sh`, `hack/verify-prod-duration.sh` |
| integration-test `-tags=integration` 覆盖 batch2 新增包 | **部分缺口**：PR-CFG-I 新增 `runtime/auth/...` 和 `cells/accesscore/slices/sessionlogin/...` 未纳入 integration-test scope | `_build-lint.yml:255` scope 列表无这两个路径 |

## DevOps Findings

| ID | Severity | Cx | Evidence | Root cause | Fix direction |
|---|---|---|---|---|---|
| DO-F1 | P2 | Cx1 | `tests/e2e/scripts/bootstrap-admin.sh:21-24` 四处 `${VAR:-default}` 软回退（BASE_URL / E2E_ADMIN_USERNAME / E2E_ADMIN_EMAIL / E2E_ADMIN_PASSWORD）；CI workflow (`_build-lint.yml:320`) 调用时未设置任何这四个变量，全部命中默认值 | 违反 `feedback_no_soft_fallback.md`：CI/e2e 内部基建不写 `${VAR:-default}`，软回退掩盖 mis-configuration | 将 USERNAME / EMAIL / PASSWORD 改为 `${VAR:?must be set}`；CI 在 `env:` 块显式注入值；`BASE_URL` 可保留（localhost 是 host-network 下语义固定值，但加注释说明） |
| DO-F2 | P3 | Cx2 | `--boundary-only` flag 从未实现（`main_test.go:274-281` 断言该 flag 报 "flag provided but not defined"）；plan 描述 CI 通过 `generate assembly --id X --boundary-only` 遍历的路径实际由 `verify generated` 替代 | Plan 叙述与实际落地实现路径不一致；门禁等价有效，但 plan 文档和测试行为产生混淆 | 更新 plan 段落说明 `--boundary-only` 已由 `verify generated` 机制替代；或删除 `TestRunGenerateAssemblyBoundaryOnlyRejected` 测试（其存在是为了护卫 flag 不存在，但这本身是文档混淆根因） |
| DO-F3 | P2 | Cx1 | `integration-test` job scope (`_build-lint.yml:255`) 缺少 `./runtime/auth/...` 和 `./cells/accesscore/slices/sessionlogin/...`；PR-CFG-I 引入的 fail-closed 构造期测试（`authenticator_test.go` 12 处 error-first 调用 + 5 个新测试；`service_test.go` / `outbox_test.go` 4 个新测试）只在 `cells` shard 普通 unit 路径覆盖，不含 `-tags=integration` 路径 | PR-CFG-I 合入时未同步扩展 integration scope；plan 验证矩阵 `go test -tags=integration ./runtime/auth/... ./cells/accesscore/...` 仅在本地约定，未写入 CI | 将 `./runtime/auth/...` 和 `./cells/accesscore/slices/sessionlogin/...` 追加到 `_build-lint.yml:255` integration-test scope |

## 工具结果

- `golangci-lint`: 0 issues（`golangci-lint run ./...` on develop @ 1958a5a8）
- CI workflow 文件清单与覆盖项：

| 文件 | 触发 | 覆盖 |
|---|---|---|
| `.github/workflows/ci.yml` | push:develop | 调用 `_build-lint.yml`，full lint + integration + sonar + os-smoke + examples-smoke 全开 |
| `.github/workflows/pr-check.yml` | PR to develop/main/release | 同上，lint-mode=pr（`--new-from-merge-base`） |
| `.github/workflows/_build-lint.yml` | reusable | 5-shard build-test + integration-test + e2e(docker-compose) + os-smoke + examples-smoke + sonarcloud |
| `.github/workflows/governance.yml` | push/PR to develop/main/release | `make verify`（glob 发现全部 `hack/verify-*.sh`，含 unconditional-skip / panic-registered / prod-duration） |

关键观测：
- e2e job 的 `go test -json | e2egate` 门禁已生效（零执行即 fail，PR-CFG-D/J 主张兑现）
- `outbox_emit_failopen_dropped_total{cell,topic}` counter 在 `kernel/outbox/emitter.go:133` 注册，`assemblies/corebundle/generated/metrics-schema.yaml:259-267` 已 regen 反映，标签 `cell` + `topic` 与 plan 一致
- readyz probe 命名规范：`rabbitmq_ready`（`adapters/rabbitmq/connection.go:759`）、`postgres_ready`（`adapters/postgres/pool_resource.go:57`）、`vault_transit_ready`（`adapters/vault/transit_provider.go:1117`）均符合 `observability.md` `_ready` 后缀约束；batch2 期间无新破坏
- RMQ flake `PR333-RMQ-CLOSE-DEADLINE-FLAKE` 已登记于 `docs/backlog.md:94`，本次不重复登记
- kernel shard / tools shard 拆分（`_build-lint.yml:58-86` 注释引用 `commit 16d907a7`）已落地；tools shard 单独跑 `go/packages` 静态分析 (~130s)，与 kernel shard 并行，关键路径 ~340s → ~170s

## Seat Digest

CI 主承诺基本兑现：unconditional-skip 门禁、e2e docker-compose 真执行、boundary drift 检测、PR-CI-5/6 archtest 均在 `governance.yml` + `_build-lint.yml` 有效覆盖，`golangci-lint 0 issues`。主要残项是 `bootstrap-admin.sh` 三处 `${VAR:-default}` 软回退（DO-F1，CI 调用时未注入变量，违反 feedback_no_soft_fallback），以及 PR-CFG-I 引入的 `runtime/auth/...` 和 `cells/accesscore/slices/sessionlogin/...` 未纳入 integration-test `-tags=integration` scope（DO-F3）。`--boundary-only` flag 计划描述与实际实现不一致属文档噪音（DO-F2），门禁功能本身等价有效。
