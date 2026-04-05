# PR 交付计划 — Phase 3: Adapters

> Role: 项目经理
> Date: 2026-04-05
> Input: tasks.md (90 tasks) + task-dependency-analysis.md
> Branch base: `develop`
> Feature prefix: `feat/002-phase3-adapters`

---

## 说明

- 每行为一个独立 PR 单元：可独立 `go build`、独立执行 `verify` 列命令
- `depends` 列为必须先合并的 PR（空表示可直接从 develop 开）
- Wave 组内标注 `[可并行]` 的 PR 可同时开发，无先后顺序要求
- 括号内为覆盖的 tasks.md 任务编号

---

## Wave 0: 前置准备（无外部依赖）

Wave 0 内 PR-01 与 PR-02 **串行**：PR-01 先合并，PR-02 再开。

| PR | scope | tasks | depends | verify | branch |
|----|-------|-------|---------|--------|--------|
| PR-01 | Bootstrap 接口化 + outbox doc 增强 | T01, T02 | — | `go build ./runtime/... && go test ./runtime/bootstrap/...` | `feat/phase3/w0-bootstrap-refactor` |
| PR-02 | UUID 替换 + seed 脚本 + Wave 0 回归 | T03, T04, T05 | PR-01 | `go test ./...` (全量回归) | `feat/phase3/w0-uuid-seed-regression` |

**Wave 0 完成门控**: PR-02 合并后，`go test ./...` 全量 PASS，无 Phase 2 回归。

---

## Wave 1: 基础设施 Adapter + DevOps（可并行）

Wave 1 四个 PR **互相独立**，可同时开发。PR-03/04/05/06 依赖 PR-02，无相互依赖。

| PR | scope | tasks | depends | verify | branch |
|----|-------|-------|---------|--------|--------|
| PR-03 | PostgreSQL adapter 基础 | T10, T11, T12, T13, T14, T15 | PR-02 | `go test ./adapters/postgres/... -count=1` (mock pgx) | `feat/phase3/w1-postgres-base` |
| PR-04 | Redis adapter | T16, T17, T18, T19, T20 | PR-02 | `go test ./adapters/redis/... -count=1` (mock redis) | `feat/phase3/w1-redis` |
| PR-05 | RabbitMQ adapter | T21, T22, T23, T24, T25, T26 | PR-02 | `go test ./adapters/rabbitmq/... -count=1` (mock amqp) | `feat/phase3/w1-rabbitmq` |
| PR-06 | DevOps: Docker Compose + Makefile + healthcheck | T06, T07, T08, T09 | PR-02 | `docker compose config --quiet && bash scripts/healthcheck-verify.sh` | `feat/phase3/w1-devops` |

**Wave 1 注意**:
- PR-03 内部串行顺序: T10 (pool) → T11 (tx_manager) → T12 (migrator) → T13 (migrations)，T14/T15 可在 T10 完成后并行。
- PR-05 内部串行顺序: T21 → T22 → T23 → T24 → T25，T26 在 T21 完成后可并行。
- PR-06 与 PR-03/04/05 完全独立（不含 Go 代码），可优先合并。
- PR-04 的 T18 (IdempotencyChecker) 是 PR-05 的 T24 (ConsumerBase) 的前置依赖，因此 **PR-04 须先于 PR-05 合并**。

**Wave 1 完成门控**: PR-03 + PR-04 + PR-05 全部合并后，Wave 2 方可开始。PR-06 可在 Wave 2 并行期合并。

---

## Wave 2: 应用 Adapter + Outbox 链路（部分并行）

PR-07 依赖 PR-03 + PR-05，为 Wave 2 关键路径核心。PR-08/09/10 可在 PR-07 开发同期并行启动（仅依赖 PR-02）。

| PR | scope | tasks | depends | verify | branch |
|----|-------|-------|---------|--------|--------|
| PR-07 | PostgreSQL Outbox Writer + Relay + 单测 | T27, T28, T29 | PR-03, PR-05 | `go test ./adapters/postgres/... -run Outbox -count=1` | `feat/phase3/w2-outbox-chain` |
| PR-08 | OIDC adapter | T30, T31, T32, T33, T34 | PR-02 | `go test ./adapters/oidc/... -count=1` (mock http) | `feat/phase3/w2-oidc` |
| PR-09 | S3 adapter（含 KS-10 间接层） | T35, T36, T37, T38 | PR-02 | `go test ./adapters/s3/... -count=1` + `grep -r "gocell/cells" adapters/s3/ \| wc -l` (须为 0) | `feat/phase3/w2-s3` |
| PR-10 | WebSocket adapter + Cell PG Repo + S3 ArchiveStore + 单测 | T39, T40, T41, T42, T43, T44, T45, T46 | PR-03, PR-09 | `go test ./adapters/websocket/... ./cells/.../adapters/... -count=1` | `feat/phase3/w2-ws-repo` |

**Wave 2 注意**:
- PR-07 是关键路径瓶颈：T27 的 context-embedded tx 模式须按 KS-01 规范实现；T27 须包含 ERR_ADAPTER_NO_TX fail-fast。
- PR-09 合并时 reviewer 须确认 `adapters/s3/` 不直接导入 `cells/`（KS-10/C-02）。
- PR-10 中的 T43/T44（Cell PG Repo）依赖 PR-03；T45（S3 ArchiveStore in Cell）依赖 PR-09；T39-T42 独立。可拆分 PR-10 为两个子 PR 若并行开发压力大。

**Wave 2 完成门控**: PR-07 合并后，Wave 3 的 Cell 重构 PR 方可开始。

---

## Wave 3: Cell 集成 + 安全 + Tech-Debt（部分并行）

PR-11 为串行核心（Cell outbox 重构），PR-12/PR-13 为并行组，可与 PR-11 同期开发，但 PR-12 中的 T63 依赖 PR-07 完成。

| PR | scope | tasks | depends | verify | branch |
|----|-------|-------|---------|--------|--------|
| PR-11 | Cell Outbox 重构 + cmd/core-bundle 接线 | T47, T48, T49, T50 | PR-07, PR-01 | `go build ./cells/... ./cmd/core-bundle/... && go test ./cells/...` | `feat/phase3/w3-cell-outbox-rewire` |
| PR-12 | 安全加固（8 条 FR-9） | T51, T52, T53, T54, T55, T56, T57, T58 | PR-02 | `go test ./... -run TestSec -count=1` | `feat/phase3/w3-security` |
| PR-13 | Tech-Debt P0+P1 + 产品修复 | T59, T60, T61, T62, T63, T64, T65, T66, T67, T68, T69 | PR-07 (T63), PR-02 (其余) | `go build ./... && go test ./... -count=1` | `feat/phase3/w3-tech-debt` |

**Wave 3 注意**:
- PR-11 内部顺序: T47/T48/T49（三个 Cell 重构）可并行开发 → T50（接线）串行收敛。
- PR-12 的 8 个安全任务互相独立，可在同一 PR 内分 commit 或拆为多 PR（若安全评审需要）。
- PR-13 的 T63 依赖 PR-07（OutboxWriter）；若 PR-07 未合并，T63 在 PR-13 分支上可暂 TODO 标记，最后再实现。
- PR-11 合并后，PR-12/13 如有冲突须 rebase。

**Wave 3 完成门控**: PR-11 合并后，Wave 4 集成测试方可启动。PR-12/13 可在 Wave 4 并行期继续合并。

---

## Wave 4: 集成测试 + 文档 + KG 验证（部分并行）

集成测试主链（PR-14）为串行，PR-15/16/17 可并行。

| PR | scope | tasks | depends | verify | branch |
|----|-------|-------|---------|--------|--------|
| PR-14 | 集成测试全链路（adapter + outbox + DLQ + Journey + Assembly） | T70, T71, T72, T73, T74 | PR-11, PR-06 | `make test-integration` (testcontainers, tags=integration) | `feat/phase3/w4-integration-tests` |
| PR-15 | 覆盖率补全 + Phase 2 回归 | T75, T76 | PR-11 | `go test -cover ./... kernel/ >= 90% && go test ./...` | `feat/phase3/w4-coverage-regression` |
| PR-16 | 文档（godoc + doc.go + 指南 + 配置参考） | T77, T78, T79, T80, T81, T82 | PR-06, PR-07 (参考) | 人工核查 godoc 渲染 + `go doc ./adapters/...` 无 error | `feat/phase3/w4-docs` |
| PR-17 | 元数据更新 + KG 全量验证 | T83, T84, T85, T86, T87, T88, T89, T90 | PR-14, PR-15 | `gocell validate && go build ./... && go vet ./... && bash scripts/kg-verify.sh` | `feat/phase3/w4-kg-verify` |

**Wave 4 注意**:
- PR-14 内部串行顺序: T70（每 adapter 独立集成测试）→ T71（Outbox 全链路）→ T72（DLQ）→ T73（Journey）→ T74（Assembly）。
- PR-17 是 Phase 3 Gate 最终 PR，须在所有其他 PR 合并后执行；T86-T90 的 5 个 grep 验证脚本须全部返回预期结果（0 匹配或白名单匹配）。
- PR-16 可在任何时间并行合并，不阻塞 PR-14/15/17。
- PR-17 合并即代表 Phase 3 正式完成，须触发 `develop` 分支 CI 全量通过。

**Phase 3 完成门控**: PR-17 合并 + CI 绿灯 = Phase 3 PASS。

---

## 汇总视图

| Wave | PR | 可并行 | 关键约束 |
|------|----|--------|---------|
| Wave 0 | PR-01 → PR-02 | 无（串行） | PR-01 是全局 blocker |
| Wave 1 | PR-03, PR-04, PR-05, PR-06 | 可并行（PR-04 须先于 PR-05） | PR-03+05 是 Wave 2 门控 |
| Wave 2 | PR-07; [PR-08, PR-09, PR-10 并行] | PR-08/09/10 可并行 | PR-07 是 Wave 3 门控 |
| Wave 3 | PR-11; [PR-12, PR-13 并行] | PR-12/13 可并行 | PR-11 是 Wave 4 门控 |
| Wave 4 | PR-14 → PR-17; [PR-15, PR-16 并行] | PR-15/16 可与 PR-14 并行 | PR-17 是 Phase 3 Gate |

**总 PR 数**: 17 个
**最长串行链**: PR-01 → PR-02 → PR-03 → PR-07 → PR-11 → PR-14 → PR-17（7 跳）
**最大并行度**: Wave 1（4 PR 并行）/ Wave 4（PR-14+15+16 并行）

---

## PR 依赖图（精简版）

```
PR-01 → PR-02 ─┬→ PR-03 ─┬→ PR-07 ─→ PR-11 ─→ PR-14 ─→ PR-17
                │          │                      │          ↑
                ├→ PR-04 ─┘→ PR-05               └→ PR-15 ─┘
                │                                           ↑
                ├→ PR-06 ──────────────────────→ PR-14 ────┘
                ├→ PR-08
                ├→ PR-09 ─→ PR-10
                ├→ PR-12
                └→ PR-13 (partial: T63 deps PR-07)

PR-16 (docs) → PR-17  [可任意时间并行]
```

---

## Kernel Guardian 验证快查表（PR-17 执行）

PR-17 合并前须确认以下验证命令全部返回预期结果：

| 验证 | 命令 | 预期结果 |
|------|------|---------|
| C-02 分层 | `grep -r "gocell/cells" adapters/` | 0 匹配 |
| C-03 分层 | `grep -r "gocell/adapters" kernel/` | 0 匹配 |
| C-04 分层 | `grep -r "gocell/adapters\|gocell/cells" runtime/` | 0 匹配 |
| C-05 分层 | `grep -r "gocell/runtime" kernel/` | 0 匹配 |
| C-12 Close | 每 adapter 含 `var _ io.Closer` 编译断言 | build PASS |
| C-18 go.mod | go.mod diff 仅含 5 个白名单直接依赖 | 人工确认 |
| C-19/20 errcode | `grep -rn "errors\.New\|fmt\.Errorf" adapters/` (排除 _test.go) | 全部为 errcode 包装 |
| C-25 禁用字段 | `grep -rn "cellId\|sliceId\|contractId" . --include="*.go" --include="*.yaml"` | 0 匹配 |
| C-16 metadata | `gocell validate` | 0 error |
| C-24 vet | `go vet ./...` | 0 warning |
