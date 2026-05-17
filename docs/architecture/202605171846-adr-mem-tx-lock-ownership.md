# ADR: mem-store tx 锁所有权真值化（根治 sentinel-without-lock）

> Status: Accepted
> Date: 2026-05-17
> Implementation: 238-mem-tx-lock-ownership
> Source plan: /Users/shengming/.claude/plans/https-github-com-ghbvf-gocell-actions-ru-smooth-pumpkin.md

## Context

PR #552 的 CI（run 25985563480）出现 `fatal error: concurrent map writes`
@ `cells/accesscore/internal/mem/user_repo.go`，测试
`TestChangePassword_ConcurrentRequests_ExactlyOneSucceeds`。PR #552 的 diff 不
触及 `cells/accesscore/`——这是一个**先前就存在的测试缺陷**，被无关 PR 的 CI
偶然触发，本地多轮 `go test ./...` 因 goroutine 调度运气未命中。`-race`
下 100% 复现：DATA RACE @ `user_repo.go` 的 `UpdatePassword`/`BumpAuthzEpoch`
map 写路径。

### L3 根因

`cells/accesscore/internal/mem` 的并发模型：单 `sync.Mutex store.mu` 保护 4 个
map；事务边界经 ctx 传递。改造前 `memTxRunner.RunInTx` 先 `mu.Lock()` 再注入
一个 **bool sentinel** `memTxKey{}=true`；每个 repo 方法
`if !isInMemTx(ctx) { mu.Lock() }` —— 见 sentinel 即**跳过加锁**，假设
「外层 RunInTx 已持锁」（`sync.Mutex` 不可重入，重入会死锁）。

公开 API `func WithTxContext(ctx) ctx` 注入**同一 bool sentinel 但不持锁**，
供单 goroutine 测试让 `GetByXxxForUpdate` 走 in-tx 路径。bool sentinel
**无法区分**「RunInTx 真持锁」与「WithTxContext 没持锁」。6 个 fake TxRunner
（`simpleTxRunner` / `contractTxRunner` / `durableTxRunner` /
`recordingTxRunner` / 2×`stubTxRunner`）跨 4 包以此形态注入；其中 1 处多
goroutine 测试即让 repo 方法误判跳锁、无锁并发写 map → fatal。

`identitymanage_credential_race_test.go` 已为同一陷阱踩坑修过两次（注释告警），
本次是第三次复发——证明根因在 L3：危险组合「在 tx 上下文 + 不持锁 + 跳锁」
在 bool sentinel + 公开 `WithTxContext` 下**可被表达**，纯删/改测试是 L1 补丁，
不闭根因（AI 协作章程：同类问题第 N 轮复发，根因必在 L3）。

## Decisions

### D1. ctx sentinel：bool → 私有 typed `*memTxToken`

`memTxKey{}` 的 value 类型由 `bool` 改为**包内私有** `*memTxToken`：

```go
type memTxToken struct {
    store     *Store // 哪个 Store 的锁；跨 store 不得跳锁
    holdsLock bool   // 注入者当前是否在调用 goroutine 上持有 store.mu
}
```

- `memTxRunner.RunInTx`：`mu.Lock()` 后注入 `&memTxToken{store: r.s, holdsLock: true}`。
- `WithTxContext`：注入 `&memTxToken{holdsLock: false}`（签名不变）。
- 删除 `isInMemTx`；新增 `func (s *Store) txHoldsLock(ctx) bool` =
  `tok != nil && tok.holdsLock && tok.store == s`。
- 18 处 repo guard（user_repo ×7 / role_repo ×11）
  `if !isInMemTx(ctx)` → `if !r.store.txHoldsLock(ctx)`。

效果：`holdsLock=false`（WithTxContext / 6 fake）→ 任何 repo 方法走 per-call
`store.mu.Lock()`，并发写 map **不可能**发生。`holdsLock=true`（仅 RunInTx，
且 store 身份匹配）→ 跳 per-call 锁，整闭包持锁，跨方法原子性不变（等价 PG
SELECT FOR UPDATE-until-commit）。`sync.Mutex` 不可重入约束不变。

### D2. AI-rebust 评级：Hard（type system + 私有字段封装），funnel 双向锁

- **上游 Hard**：`memTxToken` 与 `holdsLock` 均不导出。构造 `holdsLock=true`
  token 的唯一 callsite 是包内 `memTxRunner.RunInTx`（私有 struct，刚
  `mu.Lock()`）。`WithTxContext` 公开 API 硬编码 `holdsLock:false`。包外
  任何代码（含测试 fake）**无 API 表面**可表达「在 tx 且持锁」——Go 编译器
  即 gate。
- **下游 Hard**：跳 per-call 锁的能力仅当 `txHoldsLock` 返回 true；该函数
  AND 校验 `holdsLock && store==s`。

闭环成立（对照 ai-collab.md §Funnel 双向锁：下游 Hard + 上游 Hard）。

### D3. archtest `MEM-TX-LOCK-OWNERSHIP-01`（Medium 双重防线）

type system 是 Hard 主线；archtest 闭包包内残留风险（未来包内 edit 在别处
mint holdsLock=true）：R1 = `memTxToken` 复合字面量只许在
`(memTxRunner).RunInTx` / `WithTxContext`；R2 = 禁 `new(memTxToken)` + 禁
`.holdsLock` 赋值 LHS。盲区（reflect/unsafe 字段写）由反向自检测试关闭；
companion-index 精度测试防 vacuous-pass。文件
`tools/archtest/mem_tx_lock_ownership_test.go`。

### D4. 被删测试改为活体回归（不删除）

`TestChangePassword_ConcurrentRequests_ExactlyOneSucceeds` 保留并接
`simpleTxRunner`（holdsLock=false 路径），断言重构为竞争安全不变量：无 fatal/
race；`successes==1`；loser 是 `ErrVersionConflict` **或**
`ErrAuthOldPasswordIncorrect`（per-call 锁无跨方法原子性，两者皆合法竞争结局；
仅断言 ErrVersionConflict 本身就是 latent flake）；最终 version==1。强
exactly-once-CAS-conflict 属性（真 MVCC）由
`..._PG`（`//go:build integration`）覆盖。

### D5. 相邻优化登记 backlog（不混入本 PR）

client-go ThreadSafeStore 用 RWMutex 让 outside-tx 读并发。
`store.mu sync.Mutex` → `sync.RWMutex` 是有效优化但**正交于 flake 根因**
（需 18 方法读写分类 + 正确性逐一论证），混入扩大本 P0 修复 review 面。
登记 `docs/backlog/cap-14-tooling.md` `MEM-STORE-RWMUTEX-READ-CONCURRENCY`，
store.go 包 godoc 点名（不 silent carryover）。

## 开源对标

| 框架 | 事实 | 结论 |
|------|------|------|
| ent/ent `ent.go` | ctx 存强类型 `*Tx` 指针非 bool；不持连接者无法伪造 | 与 `*memTxToken` 同构；`holdsLock` 字段是 GoCell 特有（ent 无"假 tx"场景）非反模式 |
| go-gorm/gorm `finisher_api.go` | in-tx 靠 `ConnPool.(TxCommitter)` type assertion 非 bool | "对象类型而非 bool 判断 in-tx"同构 |
| go-kratos/examples `data.go` | ctx 存 `*queries.Queries` 非 bool | 同向背书 |
| kubernetes/client-go `tools/cache/thread_safe_store.go` | 单 RWMutex 无条件加锁，无 sentinel-skip；无跨方法事务需求 | 纯 RWMutex-无条件锁方案被否（GoCell 需 RunInTx 跨方法原子性）；`*Locked` 内部约定 GoCell 已遵循 |
| golang/go `database/sql` + Go 官方 | Mutex 故意不可重入；`*sql.Tx` 显式持有者 | "外持锁+内不重入"是唯一正确路径；bool-不持锁是设计哲学偏离点 |

```
ref: ent/ent examples/o2o2types/ent/ent.go (typed *Tx in context, no bool sentinel)
ref: go-gorm/gorm finisher_api.go (in-tx via type assertion)
ref: go-kratos/examples transaction/sqlc/internal/data/data.go
ref: kubernetes/client-go tools/cache/thread_safe_store.go (RWMutex, *Locked convention)
ref: golang/go database/sql (explicit Tx ownership; non-reentrant Mutex rationale)
```

## 威胁矩阵

| 威胁 | 改造前 | 改造后 | 机制 |
|------|--------|--------|------|
| sentinel 在场 + 不持锁 → 并发写 map（本 flake） | ❌ fatal（偶发，调度运气） | ✅ 消除 | holdsLock=false → 强制 per-call 锁 |
| 包外 fake 伪造「在 tx 且持锁」 | ❌ bool 任意可注入 | ✅ 不可表达 | memTxToken/holdsLock 不导出，编译器 gate（上游 Hard） |
| 包内未来 edit 在 RunInTx 外 mint holdsLock=true | ⚠️ 无防护 | ✅ archtest 拦截 | MEM-TX-LOCK-OWNERSHIP-01 R1/R2（Medium） |
| 构造 token 后 reflect/unsafe 改 holdsLock | ⚠️ — | ✅ 反向自检 | mem 包禁 import reflect/unsafe（archtest） |
| 跨 store token 混用（A 的 token 让 B 跳锁） | ⚠️ bool 无 store 维度 | ✅ 拒绝 | txHoldsLock 校验 `tok.store == s` |
| RunInTx 跨方法原子性丢失（PG FOR UPDATE 等价） | ✅ 持锁全程 | ✅ 不变 | holdsLock=true 跳 per-call 锁，闭包持锁 |
| 单 goroutine fake 测试退化 | ✅ | ✅ 不变 | per-call 锁串行天然原子（无并发） |

无格子从 ✅ 变 ⚠️/❌。

## contract-fanout 回灌（5 载体）

- 接口/语义定义：store.go 包 godoc + WithTxContext godoc 重写（锁所有权真值）。
- 全部实现：mem 单实现；user_repo/role_repo godoc lock contract 段同步。
- 各层 test：并发回归（活体）+ 6 fake/4 包单 goroutine 回归 + archtest + PG integration。
- 测试夹具：6 fake 注释订正；`identitymanage_credential_race_test.go` 两处
  「causes concurrent map writes under -race」**已失真**注释重写（store-bound
  TxRunner 现因 cross-method atomicity 而选，非防 corruption）。
- 公开 docs/ADR：本 ADR + backlog 条目 + store.go 引用本 ADR。

## Rollback

回退 store.go（token→bool）+ 18 处 guard + 删 archtest。flake 复现（已知
`-race -count` 必现），不建议。

## Hard 范本登记

「unexported typed token + unexported ownership 字段，唯一 true-构造点为
持锁入口」——context-carried 资源所有权真值的 Hard funnel 范本（上游
type-system Hard + 下游 txHoldsLock AND 校验 + Medium archtest 包内残留闭包）。
对标 ent `*Tx` / GORM ConnPool type assertion。
