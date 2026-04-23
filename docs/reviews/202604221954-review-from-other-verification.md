# Review Verification — 六层扫描专项（2026-04-22）

> 针对 `docs/reviews/202604221606-review-from-other.md` 的全量 Finding 存在性验证。
> 验证基线：develop @ 76d918a（与原报告基线一致）。
> 方法：5 路 explorer agent 并行，按层分片（pkg+contracts / runtime / adapters+auth / cmd+examples / kernel+cells），每条 finding 均提供文件:行号级证据。
>
> **2026-04-23 更新：**
> - **P2-12 NAMING-INCONSISTENT-01 → RESOLVED**：PR#220 `refactor(naming): collapse all kebab-case directories and ids to no-dash` 已完成目录重命名（`cells/access-core/` → `cells/accesscore/` 等），问题不复存在。
> - **P1-13 CMD-THICK-ENTRY-01 → PARTIALLY_CONFIRMED**：`cmd/corebundle/` 已拆出 `app.go` + `bundle.go`，主装配已分离；但 `main.go` 仍偏大，降为维护债（非原级别阻塞问题）。

---

## 1. 结论总览

原报告声称 **39 项 ~60h**，实测 36 条可辨识条目，真实分布如下：

| 判定 | 数量 | 占比 | 备注 |
|---|---|---|---|
| CONFIRMED — 值得修 | 14 | 39% | P1-13 降为 PARTIALLY |
| PARTIALLY CONFIRMED | 2 | 6% | 原有 1 条 + P1-13 |
| RESOLVED — 已修 | 4 | 11% | 原有 3 条 + P2-12（PR#220） |
| FALSE_POSITIVE — 驳回 | 13 | 36% | P2-12 移入 RESOLVED |
| UNCLEAR — 需 linter | 1 | 3% | |
| （表头/合计/重复） | 2 | — | |

**真实工作量估算 ~26h**（扣除 13 条 FP + 4 条已修 + 1 条未定后；P1-13 降权约 -1h）。原报告 60h 估算虚高约 2.3 倍。

---

## 2. 逐条判定

### A. CONFIRMED — 需要修（按 PR 主题分组）

| # | ID | Cx | 证据 | 修复方案要点 |
|---|---|---|---|---|
| A1 | P0-2 PKG-CTXKEYS-LAYER-01 | Cx2 | `pkg/ctxkeys/keys.go` 导出 CellID/SliceID/CorrelationID/TraceID 等，55 文件导入；`kernel/outbox/observability_metadata.go:43` 直接依赖 | 迁移到 `kernel/ctxkeys/`；ctxkeys 已是 Cell 运行时基础设施，非通用工具 |
| A2 | P0-4 HARDCODED-UNIX-PATH-01 | Cx1 | **实际位于 `examples/sso-bff/main.go:150` `stateDir = "/run/gocell"`**（原报告定位到 `cmd/core-bundle/main.go` 有偏差） | `os.UserConfigDir()` fallback；`filepath.Join` 替代 `/` 拼接 |
| A3 | P1-1 WINDOWS-SIGNAL-01 | Cx1 | `runtime/shutdown/shutdown.go:58` 注册 SIGINT+SIGTERM；`shutdown_test.go:126` 只发 SIGINT | 新增 `shutdown_sigterm_unix_test.go` 用 `//go:build !windows` |
| A4 | P1-2 SYMLINK-TEST-PRIVILEGE-01 | Cx1 | `runtime/config/watcher_test.go:338,357,378,381,398` + `kernel/governance/validate_test.go:2557` | Windows skip 或 build tag |
| A5 | P1-4 DUPLICATED-VALIDATION-01 | Cx2 | `runtime/auth/*.go` 6+ 处 `if token == ""` 无 helper | 抽 `pkg/validation.RequireNotBlank` |
| A6 | P1-5 WEBSOCKET-DEPRECATED-01 | Cx1 | `go.mod` `nhooyr.io/websocket v1.8.17` 已 archived，社区推荐 `github.com/coder/websocket`（API 兼容） | 仅改 import |
| A7 | P1-8 DEPRECATED-ADAPTER-METHODS-01 | Cx1 | `Connection.Statter()`、`Subscriber.InitializeSubscription()` 仍被测试 + `kernel/outbox/outbox_test.go:464` 使用 | 移除 `Deprecated:` 标注（承认它是稳定 API）或先迁测试再删 |
| A8 | P1-13 CMD-THICK-ENTRY-01 | ~~Cx3~~ **PARTIALLY_CONFIRMED** | `cmd/corebundle/main.go` 仍偏大；但 `app.go` + `bundle.go` 已拆出主装配（PR#220 同时完成目录 rename）。主要 wiring 已分离，剩余问题降为维护债 | `main.go` 可按需继续缩减，不再是阻塞级 |
| A9 | P1-14 DEMO-KEY-IN-PROD-PATH-01 | Cx2 | **`runtime/auth/keys.go:328 LoadKeySetFromEnv()` 缺 `rejectDemoKey` 检查**；cursor/HMAC/service-secret 都有 | 加 rejectDemoKey + demo 模板值黑名单 |
| A10 | P1-15 GOVERNANCE-WINDOWS-PATH-01 | Cx2 | cmd/gocell/ 已用 `filepath.Join`；**`examples/sso-bff/main.go:150,168` 用 `"/" ` 拼接** | examples 改 `filepath.Join` |
| A11 | P1-17 GOVERNANCE-GAP-01 | Cx3 | `kernel/governance/validate.go:138-154` rules 只扫 cells/contracts/journeys/assemblies，不扫 examples/ | 新增 `rules_examples.go` 检查硬编码 key/path/URL |
| A12 | P2-1 DEPRECATED-SHIM-01 | Cx1 | `kernel/outbox/outbox.go:536,543,552` 三个 deprecated 符号无外部 caller | 直接删除 |
| A13 | P2-2 MISSING-COMMENT-01 | Cx1 | kernel/cell/ 公开项约 20-25% 缺 godoc | 按包补 godoc |
| A14 | P2-5 CLOSE-PATTERN-DEDUP-01 | Cx2 | Redis `client.go:243-265` / Postgres `pool.go:160-179` 模式完全一致；RabbitMQ 有锁+cleanup 差异 | 抽 `adapterutil.CloseWithDeadline`，只给 Redis+Postgres 用 |
| A15 | P2-7 CELL-GO-BLOAT-01 | Cx3 | access-core/cell.go **582 行**，config-core/cell.go **431 行**，audit-core 272 行 | 每 cell 拆 `cell_routes.go + cell_events.go + cell_lifecycle.go` |
| A16 | P2-8 RUN-IN-TX-DEDUP-01 | Cx2 | rbacassign/identitymanage/auditverify/ordercreate 等 5+ 处 `if txRunner != nil { RunInTx } else { fn }` 实现一致 | 在 `kernel/persistence.TxRunner` 加 helper |
| A17 | P2-9 FETCH-ROLE-NAMES-DEDUP-01 | Cx1 | sessionlogin:162 fail-closed / sessionrefresh:199 fail-open | 抽 `cells/access-core/internal/rolefetch`，拆 `FetchRolesStrict`/`FetchRolesLenient` 保留语义差异 |
| A18 | P2-10 FORMATTING-BUG-01 | Cx1 | adapters/ 混用 `"err", err`（原始 kv）/`slog.String("error", err.Error())`/`slog.Any("error", err)` | 统一 `slog.Any("error", err)` |
| A19 | P2-11 ENTROPY-DEMO-KEY-01 | Cx2 | `cmd/core-bundle/demo_keys.go:18-36` 可预测模板；`examples/sso-bff/main.go:99,116` demo key 无警告 | 加 `// DO NOT COPY TO PRODUCTION` 注释（与 A9 同 PR） |

### B. RESOLVED — 关闭即可（报告过时）

| ID | 证据 |
|---|---|
| P1-3 ERROR-CLASSIFICATION-01 | `pkg/errcode/classify.go:117-139` 已通过 `IsInfraError()` 把 `context.Canceled/DeadlineExceeded` 归为 `CategoryInfra` |
| P1-9 ENV-FALLBACK-01 | `cmd/corebundle/main.go:51-63,71-85,125-138` 三个 loader 都 `adapterMode=="real"` 时 fail-fast |
| P2-13 CORS-OPTIONS-EXPLICIT-01 | `runtime/http/router/policy_coverage.go:45-47` 已强制 OPTIONS 必须显式 `auth.Declare` + Public:true |
| P2-12 NAMING-INCONSISTENT-01 | PR#220 `refactor(naming): collapse all kebab-case directories and ids to no-dash`：`cells/access-core/` → `cells/accesscore/`，`cmd/core-bundle/` → `cmd/corebundle/` 等全量 rename 已完成（2026-04-23 核实） |

### C. FALSE_POSITIVE — 驳回

| ID | 驳回理由 |
|---|---|
| P0-3 PKG-CONTRACTS-LAYER-01 | `pkg/contracts/schema_types.go:2-7` 注释明确："避免 kernel/metadata 与 pkg/contracttest 模型重复，遵守分层：pkg/ 不依赖 kernel/"。设计正确 |
| P0-5 CONTRACT-INCOMPLETE-01 | 13 个契约逐一检查，全部有完整 schemaRefs 或 `noContent: true` |
| P0-6 GHOST-EVENT-01 | 13 个事件在 cell 代码中都有 publisher（session.created 8 处、user.created 9 处等） |
| P1-6 DEAD-FUNCTION-01 | 采样 runHooks/validateRSAKeySize 等都有 caller |
| P1-7 ADAPTER-ERRCODE-INCONSISTENT-01 | 前缀统一为 `ERR_ADAPTER_{PG,REDIS,AMQP}_*`，包装模式一致（`errcode.New/Wrap`） |
| P1-10 DEPRECATED-AUTH-OPTION-01 | 全仓库 grep `WithCustomSigners` 返回空，符号不存在 |
| P1-11 WINDOWS-PATH-PKG-01 | 13 个 `filepath.Join` 全用于实际 OS 文件系统（读契约/测试目录），无 URL/contract 混用 |
| P1-12 RESPONSE-GO-BLOAT-01 | 实际 373 行（<500），职责清晰（codeToStatus 映射表占 127 行属声明式） |
| P1-16 CONTRACT-WINDOWS-PATH-01 | `contracts/` 下无 `_test.go` 文件 |
| P2-3 MAP-CONSISTENCY-01 | pkg/ 无序列化场景依赖 map 遍历顺序 |
| P2-4 DECODE-DOCS-01 | `pkg/httputil/decode.go:20,36` 两个公开函数都有完整 godoc |
| ~~P2-12 NAMING-INCONSISTENT-01~~ | ~~**下文重新审视——原判误，实际 CONFIRMED**~~ → **RESOLVED**：PR#220 已完成全量 no-dash rename，见 §B 和 §4 |
| P2-14 PATH-LITERAL-HARDCODED-01 | 生产代码都走 `auth.Declare`；测试里的 `"/api/v1/..."` 是合法测试数据，`bundle_hardening_test.go:50` 还有 regex 检查防止生产代码写硬编码 |

### D. UNCLEAR

| ID | 处理 |
|---|---|
| P2-6 DEAD-VARIABLE-01 | 采样未发现；需 `golangci-lint --enable=unused,deadcode` 批量扫描才能定性 |

---

## 3. PR 划分与顺序

原则：主题内聚 + 独立 review 单元 + 风险从高到低。

| # | PR 主题 | 含 Finding | 工时 |
|---|---|---|---|
| P1 | Windows 兼容性一揽子 | A2 P0-4, A3 P1-1, A4 P1-2, A10 P1-15 | ~4h |
| P2 | 密钥加固 | A9 P1-14, A19 P2-11 | ~2h |
| P3 | ctxkeys 迁移 kernel | A1 P0-2 | ~2h |
| P4 | websocket 替换 | A6 P1-5 | ~2h |
| P5 | outbox deprecated shim 清理 + kernel godoc | A12 P2-1, A13 P2-2 | ~2h |
| P6 | adapters 卫生 | A7 P1-8, A14 P2-5, A18 P2-10 | ~3h |
| P7 | cells 去重 | A5 P1-4, A16 P2-8, A17 P2-9 | ~4h |
| P8 | cmd/core-bundle 入口拆分 | A8 P1-13 | ~3h |
| P9 | access-core cell.go 拆分 | A15 P2-7（access-core） | ~2h |
| P10 | config-core cell.go 拆分 | A15 P2-7（config-core） | ~2h |
| P11 | governance examples 覆盖 | A11 P1-17 | ~3h |
| backlog | P2-6 dead variable | — | 待 linter |

**Cx3（需人工决策）**：P8 cmd 拆分、P9/P10 cell 拆分、P11 governance 新规则 — 先输出方案再开干。
**Cx1/Cx2（可直接执行）**：P1-P7，按顺序串行或合并 worktree 并发。

---

## 4. P2-12 翻案分析 — NAMING-INCONSISTENT-01 ~~实际 CONFIRMED~~ → **RESOLVED**

见附录文档：`docs/reviews/202604221955-p2-12-naming-deep-analysis.md`。

~~摘要：`cells/` 下 5 个 Cell 均采用 kebab-case 目录 + no-dash 包名（`cells/access-core/` → `package accesscore`），**违反 Go 社区"目录名即包名"惯例**。需要独立评估修复方案。~~

**2026-04-23 更新：PR#220 已完成全量 rename**，`cells/access-core/` → `cells/accesscore/`，`cmd/core-bundle/` → `cmd/corebundle/` 等，目录名与包名现已对齐。问题不复存在，关闭。
