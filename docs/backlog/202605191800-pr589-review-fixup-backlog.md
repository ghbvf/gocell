# GoCell Backlog — PR #589 review fix-up OOS items

> 来源：PR #589 (D3a-1 metric pack) review fix-up wave 3 cleanup + PR #593 review (6 reviewer)
> 本文件登记所有因超出当前 PR scope 推迟的事项，防止 silent carryover。

---

## 条目表

| ID | 描述 | Type | P/Cx | Flag | Files | Source |
|---|---|---|---|---|---|---|
| OUTBOX-CONSUMER-BASE-HANDLE-CLAIM-STATE-PARAMS-01 | **ConsumerBase handleClaimState 8 参数** — 现状: `kernel/outbox/consumer_base.go:462` 的内部函数持有 8 个位置参数（SonarCloud Major，存在 15 天，PR #589 前已有）。认知复杂度合规，但参数数量超出 SonarCloud threshold；修复: 将参数折叠为 request struct（e.g. `claimStateParams`），减少调用处位置依赖，提升可读性。OOS 原因: pre-existing，非 PR #589 引入，修复不属于当前 wave scope。 | refactor | P3/Cx1 | 🟡 | `kernel/outbox/consumer_base.go` | PR #589 review P2 SonarCloud Major L816 OOS |
| OUTBOX-COUNT-PENDING-BOUNDED-OR-DOWNSAMPLE-01 | **CountPending 无界 SELECT count(\*)** — 现状: `kernel/outbox` (PG store) 的 `CountPending` 在每个 reclaim tick 执行全表 `SELECT count(*)` 扫描；大 backlog 场景下对 DB 造成持续压力（扫全表 + shared lock contention）。修复方向: (a) bounded count（`SELECT count(*) FROM ... LIMIT N` + 返回 `≥N` 哨兵值）或 (b) exponential downsampling（仅在 pending > threshold 时降频采样）。OOS 原因: 需要 DB 压测数据确认 threshold 再决策，与 PR #589 metric pack 不同 scope。 | perf | P3/Cx2 | 🟡 | `kernel/outbox/` + `adapters/postgres/outbox_store.go` | PR #589 review P2 OOS justification |
| HTTP-MIDDLEWARE-LOGGER-DI-01 | **HTTP middleware logger 依赖注入** — 现状: `runtime/http/middleware/metrics.go` + `access_log.go` 调用 `observability.SafeObserve(slog.Default(), fn)`，硬编 `slog.Default()`。`SafeObserve` 已有 nil-logger 兜底，但显式传 default 掩盖了"composition root 未注入 middleware logger"的情况，也降低可测试性。修复方向: 中间件构造期接受 `*slog.Logger`（composition root 注入），call site 改为 `observability.SafeObserve(mw.logger, fn)`。OOS 原因: 跨 middleware 构造函数 + composition root + 多个 middleware 调用点（Cx3），与 PR #593 的 SafeObserve 迁移不同 scope。 | refactor | P3/Cx3 | 🟡 | `runtime/http/middleware/` + composition root (`cmd/corebundle`) | PR #593 review DX P2 OOS |
| OUTBOX-PENDING-DEPTH-E2E-PROM-SCRAPE-01 | **outbox_pending_depth Prometheus 端到端 scrape 验证** — 现状: 三段链路已被 unit-level 测试覆盖：(1) corebundle 构造 `OutboxPendingDepthCollector("configcore")` + `relay.WithPendingDepthObserver(...)` — `cmd/corebundle/outbox_wiring_test.go` PG-path 测试；(2) relay reclaim tick → `observePendingDepth` → observer 调用 — `runtime/outbox/relay_test.go:1073+` 三场景；(3) observer → metric sample with `cell="configcore"` — `runtime/observability/metrics/outbox_test.go::TestOutboxPendingDepthCollector_ObservePendingDepth_UsesConstructedCellID`。缺口: 完整 corebundle 启动 + clockmock 触发 reclaim tick + Prometheus `/metrics` HTTP scrape 验证 `gocell_outbox_pending_depth{cell="configcore"}` 真的出现在 wire 上。修复方向: 在 `cmd/corebundle/metrics_wiring_integration_test.go` 增加 PG-topology 测试 (需 testcontainers PG)，用 clockmock 加速 ReclaimInterval 触发 tick，scrape /metrics 断言。OOS 原因: Cx2-Cx3 需 PG fixture，三段 unit-level 已 connect-the-dots 验证端到端语义；reviewer 标 P2 建议 backlog。 | test | P3/Cx3 | 🟡 | `cmd/corebundle/metrics_wiring_integration_test.go` | PR #593 review 产品 P2 OOS |
