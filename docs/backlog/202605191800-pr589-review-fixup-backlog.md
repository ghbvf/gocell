# GoCell Backlog — PR #589 review fix-up OOS items

> 来源：PR #589 (D3a-1 metric pack) review fix-up wave 3 cleanup  
> 两条均因超出当前 PR scope 推迟；本文件是其显式 backlog 登记，防止 silent carryover。

---

## 条目表

| ID | 描述 | Type | P/Cx | Flag | Files | Source |
|---|---|---|---|---|---|---|
| OUTBOX-CONSUMER-BASE-HANDLE-CLAIM-STATE-PARAMS-01 | **ConsumerBase handleClaimState 8 参数** — 现状: `kernel/outbox/consumer_base.go:462` 的内部函数持有 8 个位置参数（SonarCloud Major，存在 15 天，PR #589 前已有）。认知复杂度合规，但参数数量超出 SonarCloud threshold；修复: 将参数折叠为 request struct（e.g. `claimStateParams`），减少调用处位置依赖，提升可读性。OOS 原因: pre-existing，非 PR #589 引入，修复不属于当前 wave scope。 | refactor | P3/Cx1 | 🟡 | `kernel/outbox/consumer_base.go` | PR #589 review P2 SonarCloud Major L816 OOS |
| OUTBOX-COUNT-PENDING-BOUNDED-OR-DOWNSAMPLE-01 | **CountPending 无界 SELECT count(\*)** — 现状: `kernel/outbox` (PG store) 的 `CountPending` 在每个 reclaim tick 执行全表 `SELECT count(*)` 扫描；大 backlog 场景下对 DB 造成持续压力（扫全表 + shared lock contention）。修复方向: (a) bounded count（`SELECT count(*) FROM ... LIMIT N` + 返回 `≥N` 哨兵值）或 (b) exponential downsampling（仅在 pending > threshold 时降频采样）。OOS 原因: 需要 DB 压测数据确认 threshold 再决策，与 PR #589 metric pack 不同 scope。 | perf | P3/Cx2 | 🟡 | `kernel/outbox/` + `adapters/postgres/outbox_store.go` | PR #589 review P2 OOS justification |
