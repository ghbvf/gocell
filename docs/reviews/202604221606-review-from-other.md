# GoCell Backlog — 六层扫描专项

> 仅含 2026-04-22 六层代码扫描（kernel/cells/runtime/adapters/pkg/cmd+examples/contracts+metadata）发现的 Finding。
> 基线: develop@76d918a
> 总计: 8 P0 + 17 P1 + 14 P2 = **39 项，约 60h**

---

## P0 阻塞项（7 项）

| # | 任务 | 工时 | 文件 | 来源 |
|---|------|------|------|------|
| SCAN-P0-2 | **PKG-CTXKEYS-LAYER-01** (Cx2): `pkg/ctxkeys` 含 Cell 运行时级别的 context key 定义，应归属 `kernel/`。**修复**：迁移至 `kernel/ctxkeys/`，更新所有引用。 | 2h | `pkg/ctxkeys/` + 所有 import 点 | pkg/P0 |
| SCAN-P0-3 | **PKG-CONTRACTS-LAYER-01** (Cx2): `pkg/contracts` 含 Cell 间契约类型定义，应归属 `kernel/`。**修复**：迁移至 `kernel/contracts/`，更新所有引用。 | 2h | `pkg/contracts/` + 所有 import 点 | pkg/P0 |
| SCAN-P0-4 | **HARDCODED-UNIX-PATH-01** (Cx1): `cmd/core-bundle/main.go` 硬编码 `/run/gocell` Unix 路径，Windows 启动直接 panic。**修复**：用 `os.TempDir()` 或配置项，Windows fallback 到 `%LOCALAPPDATA%\gocell\run`。 | 1h | `cmd/core-bundle/main.go` | cmd/P0 |
| SCAN-P0-5 | **CONTRACT-INCOMPLETE-01** (Cx2): `contracts/http/access/session/create/` 关键 schema 缺失，消费者无法验证 payload。**修复**：补全 request/response schema 字段声明。 | 1h | `contracts/http/access/session/create/v*/contract.yaml` | contracts/P0 |
| SCAN-P0-6 | **GHOST-EVENT-01** (Cx2): `contracts/events/` 存在未在任何 cell 注册的事件声明，误导消费者。**修复**：删除无发布者的事件或在对应 cell 补齐。 | 2h | `contracts/events/` 下未注册事件 | contracts/P0 |

> 注：P0 审计裸路由 L1（AUDIT-ROUTE-POLICY-01）已存在于主 backlog，此处不重复。

---

## P1 待办（17 项）

| # | 任务 | 工时 | 来源 |
|---|------|------|------|
| SCAN-P1-1 | **WINDOWS-SIGNAL-01** (Cx1): `runtime/shutdown/` 仅测 `syscall.SIGTERM`，Windows 不递送。**修复**：build tag 分离 SIGTERM/Interrupt 测试。 | 2h | runtime |
| SCAN-P1-2 | **SYMLINK-TEST-PRIVILEGE-01** (Cx1): 测试使用 `os.Symlink`，Windows 非管理员提权失败。**修复**：skip on Windows 或 copy fallback。 | 1h | runtime |
| SCAN-P1-3 | **ERROR-CLASSIFICATION-01** (Cx1): `ctx.Canceled` / `ctx.DeadlineExceeded` 未单独归类，caller 无法区分超时 vs 业务失败。**修复**：新增 `ErrContextCanceled` / `ErrContextDeadline` 错误码。 | 2h | runtime+adapters |
| SCAN-P1-4 | **DUPLICATED-VALIDATION-01** (Cx2): runtime/ 和 cells/ 多处重复参数校验逻辑。**修复**：抽取 `pkg/validation/` 公共函数。 | 3h | runtime+cells |
| SCAN-P1-5 | **WEBSOCKET-DEPRECATED-01** (Cx1): `nhooyr.io/websocket` 已废弃，社区推荐 `github.com/coder/websocket`。**修复**：替换 import 验证兼容。 | 2h | runtime |
| SCAN-P1-6 | **DEAD-FUNCTION-01** (Cx1): runtime/ 和 pkg/ 中未调用函数。**修复**：go vet 确认后删除。 | 1h | runtime |
| SCAN-P1-7 | **ADAPTER-ERRCODE-INCONSISTENT-01** (Cx2): postgres/redis/rabbitmq 各自用不同 errcode 前缀和包装方式。**修复**：统一 `ERR_ADAPTERS_` + 子模块后缀。 | 2h | adapters |
| SCAN-P1-8 | **DEPRECATED-ADAPTER-METHODS-01** (Cx1): adapters/ deprecated 方法标记仍在使用。**修复**：确认迁移后删除。 | 1h | adapters |
| SCAN-P1-9 | **ENV-FALLBACK-01** (Cx2): `adapterMode=real` 时不应静默 env fallback，应 fail-fast。**修复**：real 模式返回缺失错误。 | 1h | adapters |
| SCAN-P1-10 | **DEPRECATED-AUTH-OPTION-01** (Cx1): `auth.WithCustomSigners` deprecated 未移除。**修复**：确认无引用后移除或去 deprecated 标记。 | 0.5h | auth |
| SCAN-P1-11 | **WINDOWS-PATH-PKG-01** (Cx2): `pkg/` 中 `filepath.Join` 生成 URL/contract 路径，Windows 下 `\` 破坏语义。**修复**：HTTP 路径统一用 `path.Join`。 | 1h | pkg |
| SCAN-P1-12 | **RESPONSE-GO-BLOAT-01** (Cx3): `pkg/httputil/response.go` 超 500 行多职责。**修复**：拆为 `response.go` + `pagination.go` + `error_response.go`。 | 3h | pkg |
| SCAN-P1-13 | **CMD-THICK-ENTRY-01** (Cx3): `cmd/core-bundle/main.go` 和 `cmd/gocell/main.go` 入口 >200 行。**修复**：抽取 wiring.go / cmd_*.go 子命令。 | 4h | cmd |
| SCAN-P1-14 | **DEMO-KEY-IN-PROD-PATH-01** (Cx2): demo/test 密钥与真实密钥共用加载路径，生产误配风险。**修复**：signing key 也加 `rejectDemoKey`。 | 1h | cmd |
| SCAN-P1-15 | **GOVERNANCE-WINDOWS-PATH-01** (Cx2): `cmd/gocell/` 治理工具 Unix 路径拼接，Windows 下找不到文件。**修复**：统一 `filepath.FromSlash` / `filepath.ToSlash`。 | 1h | cmd |
| SCAN-P1-16 | **CONTRACT-WINDOWS-PATH-01** (Cx2): `contracts/` 测试 Unix 路径硬编码。**修复**：用 `filepath.FromSlash` 或 `path.Join`。 | 1h | contracts |
| SCAN-P1-17 | **GOVERNANCE-GAP-01** (Cx3): `gocell validate` 不校验 `examples/*/main.go` 运行时配置。**修复**：新增 `check_examples.go` 静态检查。 | 2h | cmd |

---

## P2 待办（14 项）

| # | 任务 | 工时 | 来源 |
|---|------|------|------|
| SCAN-P2-1 | **DEPRECATED-SHIM-01** (Cx1, 🟡): kernel/ deprecated shim 无外部消费者。**修复**：删除。 | 0.5h | kernel |
| SCAN-P2-2 | **MISSING-COMMENT-01** (Cx1, 🟡): kernel/ 公开导出缺 godoc。**修复**：补充。 | 1h | kernel |
| SCAN-P2-3 | **MAP-CONSISTENCY-01** (Cx1, 🟡): pkg/ map range 顺序依赖用于序列化。**修复**：先 sort keys 再遍历。 | 1h | pkg |
| SCAN-P2-4 | **DECODE-DOCS-01** (Cx1, 🟡): pkg/httputil/decode 缺 godoc。**修复**：补充。 | 0.5h | pkg |
| SCAN-P2-5 | **CLOSE-PATTERN-DEDUP-01** (Cx2, 🟡): 3 个 adapter `Close(ctx)` 模式重复。**修复**：抽取 `closeutil`。 | 1h | adapters |
| SCAN-P2-6 | **DEAD-VARIABLE-01** (Cx1, 🟡): kernel/ 和 pkg/ 未使用变量/常量。**修复**：go vet 后删除。 | 0.5h | kernel |
| SCAN-P2-7 | **CELL-GO-BLOAT-01** (Cx3, 🟡): cells/*/cell.go 超 300 行。**修复**：拆为 cell.go + cell_routes.go + cell_events.go + cell_lifecycle.go。 | 2h/cell | cells |
| SCAN-P2-8 | **RUN-IN-TX-DEDUP-01** (Cx2, 🟡): runInTx 在 5 个 service 文件重复。**修复**：抽取 `pkg/txutil/txrunner.go`。 | 3h | cells |
| SCAN-P2-9 | **FETCH-ROLE-NAMES-DEDUP-01** (Cx1, 🟡): fetchRoleNames 在 sessionrefresh/sessionlogin 重复。**修复**：抽取 access-core internal/ 共享。 | 1h | cells |
| SCAN-P2-10 | **FORMATTING-BUG-01** (Cx1, 🟡): adapters/ slog 格式化不规范。**修复**：统一 `slog.Any("err", err)` 格式。 | 1h | adapters |
| SCAN-P2-11 | **ENTROPY-DEMO-KEY-01** (Cx2, 🟡): examples/ demo key 可预测，可能被 copy-paste 到生产。**修复**：随机生成或标注 `// DO NOT COPY`。 | 0.5h | examples |
| SCAN-P2-12 | **NAMING-INCONSISTENT-01** (Cx2, 🟡): kebab-case（sso-bff）和 no-dash（deviceregister）混用。**修复**：统一 no-dash（见 NAMING-UNIFORM-01）。 | — | 全局 |
| SCAN-P2-13 | **CORS-OPTIONS-EXPLICIT-01** (Cx1, 🟡): 跨域端点未显式 `auth.Declare` OPTIONS + `Public: true`。**修复**：补充声明。 | 1h | runtime |
| SCAN-P2-14 | **PATH-LITERAL-HARDCODED-01** (Cx1, 🟡): cmd/ 和 examples/ 硬编码业务路径字面量。**修复**：通过 `auth.Declare` 集中声明。 | 1h | cmd |

---

## 按层速查

| 层 | P0 | P1 | P2 | 工时 |
|----|----|----|----|-----|
| kernel/ | — | — | 3 | ~2h |
| cells/ | 1 | 1 | 4 | ~10h |
| runtime/ | — | 5 | 2 | ~8h |
| adapters/ | — | 4 | 2 | ~5.5h |
| pkg/ | 2 | 2 | 2 | ~7.5h |
| cmd/examples | 1 | 4 | 3 | ~7h |
| contracts+metadata | 3 | 2 | 1 | ~5h |
| **合计** | **7** | **17** | **14** | **~45h + cell 拆分 2h/cell** |
