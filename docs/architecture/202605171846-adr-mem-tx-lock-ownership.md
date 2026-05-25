# ADR: mem-store tx 锁所有权真值化（根治 sentinel-without-lock）

> Status: Accepted
> Date: 2026-05-17
> Implementation: 238-mem-tx-lock-ownership
> Source plan: /Users/shengming/.claude/plans/https-github-com-ghbvf-gocell-actions-ru-smooth-pumpkin.md
> Amendment: 2026-05-25 (#945) — sealed lock-witness (bool→token Medium funnel
> upgraded to compile-impossible Hard). The Decisions, threat matrix, and Hard
> 范本 below are rewritten to the witness model; the Context / L3 root cause
> records the original bool flake unchanged (accurate history).
> Amendment: 2026-05-25 (#972) — self-invalidating lease（witness 的 liveness 残留闭环）

## Amendment 2026-05-25 (#945): sealed lock-witness

> **Superseded by #972 (next section).** The symbols described in this block —
> `txlock.Held` / `Holds`, `memTxToken`, `WithTxContext`, `txHoldsLock` — were
> replaced in #972 by `txlock.Lease` / `Live`, a directly ctx-carried lease (no
> wrapper), and `inLiveTx`. This block records the #945 *forge-axis* step
> (bool → un-forgeable witness); read it as history, not as the current design.
> The live design is §"Amendment 2026-05-25 (#972)" + §Decisions (rewritten to
> the lease model).

The 238 design (D1 below, as rewritten) replaced the bool sentinel with a typed
`*memTxToken{store, holdsLock bool}`. That made *out-of-package* forgery
compile-impossible (Hard) but left the **in-package** "mint a holdsLock=true
token outside RunInTx" residue to a **Medium archtest** (old D3 R1/R2) — the
`holdsLock` bool was still assignable by any code inside package mem.

PR #558 review (issue #945) flagged that the old archtest only checked the
token literal's *lexical scope*, not its field values, so a future
`WithTxContext{holdsLock: true}` edit would pass. Rather than patch the Medium
archtest to pin the bool value, #945 **deletes the bool** and encodes
lock-ownership as an un-forgeable witness:

- New package `cells/accesscore/internal/mem/internal/txlock` (double-`internal/`
  → importable only by the mem tree). `txlock.Held` has a single unexported
  `*sync.Mutex` field; `Acquire(mu) (Held, unlock func())` is the sole way to
  obtain a Held whose `Holds(&mu)` is true, and it `Lock()`s mu first.
- `memTxToken` drops `holdsLock bool` AND `store *Store` (the witness's mutex
  identity subsumes store binding), carrying only `held txlock.Held`.
- `runLocked` mints the witness (`held, unlock := txlock.Acquire(&r.s.mu)`);
  `WithTxContext` mints a zero-witness token; `txHoldsLock` returns
  `tok != nil && tok.held.Holds(&s.mu)`.

Result (**forge axis only** — see correction below): FORGING a held witness —
expressing "in a tx context AND holding the lock" without calling Acquire — is
**compile-impossible even inside package mem** (no code can construct a held
witness through a struct literal; Acquire is the sole source and it locks). This
moves the *forge* check from runtime-AND to type-system-Hard. The archtest
(`MEM-TX-LOCK-OWNERSHIP-01`, rewritten D3) is demoted to a Medium **regression
layer** over that Hard forge core; the old R2b `.holdsLock` selector funnel is
deleted (no bool field exists). `Held` additionally has **no Release method**
(unlock is a separate closure kept local to runLocked), so a ctx-carried witness
cannot release the lock either.

**Correction (#972, do not read the above as closing liveness):** #945 closed
only the *forge* axis. It did NOT close the *liveness* axis — `Held.Holds` is
pointer-identity only and **never expires**, so a witness that escapes its
runLocked frame still reports held after `unlock()`. That residual (threat-matrix
row 6) is closed by the #972 self-invalidating lease below. "Downstream Hard"
without qualification was an over-claim; the accurate split is forge-Hard (#945)
+ liveness-Hard (#972).

## Amendment 2026-05-25 (#972): self-invalidating lease

#945 witness 使 forge（包外/包内均无法在不调 `Acquire` 的情况下构造持锁 proof）
达到编译 Hard；但遗留了一个 liveness 残留：`Held.Holds` 只做 mutex 指针比较、
**永不过期**。当 `runLocked` 返回、`unlock()` 执行后，凡先前通过 inner ctx 逃逸
（如被 after-commit hook 闭包捕获）的 `Held` 仍让 `Holds` 返回 true——即
stale-skip：持有逃逸 ctx 的调用方会跳过 per-call 加锁、无锁并发写 map。这正是
威胁矩阵第 6 行 ⚠️ 的根源。

#972 将 witness 演进为**自失效 lease**，结构性消除该残留：

**结构变化**

- `txlock.Held` → `txlock.Lease`，字段：`{mu *sync.Mutex; live *atomic.Bool}`（均不导出）。
- `Acquire(mu *sync.Mutex) (Lease, unlock func())`：锁 mu 并 `live.Store(true)`，返回
  的 unlock 闭包执行 `live.Store(false); mu.Unlock()`。`live` 的翻转**封在 Acquire
  返回的 unlock 闭包内，外部不可绕过**（无 setter、无 Release 方法、字段不导出）。
- `Holds(mu) bool` → `Live(mu *sync.Mutex) bool` = `l.mu != nil && l.mu == mu && l.live != nil && l.live.Load()`。

**Ordering 保证**：`runLocked` 中 `defer unlock()` 在函数返回时触发，先于
`RunInTx` drain after-commit hooks 执行。即便未来 accesscore 注册 after-commit hook
并捕获 inner ctx，`inLiveTx`（原 `txHoldsLock`）也会因 lease 已失效返回 false，
fail-closed 退化 per-call 加锁，并发写 map 不可能。

**同步删除的死抽象**

- `memTxToken` 包装器删除（单字段冗余包装，ctx 直接携带 `txlock.Lease`）。
- `WithTxContext` 删除（注入零 lease，行为与不注入等价；无测试场景需要该 API）。
- `txHoldsLock` → `inLiveTx`，调用 `l.Live(&s.mu)`，`l` 来自 ctx.Value 类型断言。

对标 `database/sql` 的 `Tx.done`（`atomic.Bool`）→ `ErrTxDone`：commit/rollback 后
资源自失效，后续操作 fail-closed。`txlock.Lease` 同形态：proof 绑定资源存活窗口
而非永久 token，窗口结束由 seal 独占翻转，调用方不可续期、不可绕过。

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
不闭根因（AI-robust 治理章程：同类问题第 N 轮复发，根因必在 L3）。

## Decisions

### D1. ctx sentinel：bool → ctx 直接携带 `txlock.Lease`（#972）

`memTxKey{}` 的 value 类型由 `bool` 改为**包内私有**的 `txlock.Lease`（值类型，
非指针包装），持有**不可伪造且自失效**的锁所有权证明：

```go
// cells/accesscore/internal/mem/internal/txlock（双 internal/，仅 mem 树可 import）
type Lease struct {
    mu   *sync.Mutex  // 不导出
    live *atomic.Bool // 不导出；翻转封在 unlock 闭包内，外部无 setter
}
func Acquire(mu *sync.Mutex) (lease Lease, unlock func())  // 唯一铸造点，先 Lock()，live.Store(true)
func (l Lease) Live(mu *sync.Mutex) bool                   // mu 指针身份 + live.Load()（无 Release 方法）

// cells/accesscore/internal/mem
// ctx 直接携带 txlock.Lease（不再有 memTxToken 包装器）
```

- `memTxRunner.runLocked`（`RunInTx` 的 body）：`lease, unlock := txlock.Acquire(&r.s.mu); defer unlock()`
  后注入 `lease` 到 ctx。`defer unlock()` 在 `runLocked` 返回时执行：`live.Store(false); mu.Unlock()`。
- `WithTxContext` 已删除（零 lease 行为与不注入等价）。
- `func (s *Store) inLiveTx(ctx) bool`：从 ctx 取出 `txlock.Lease` 做类型断言，调用
  `l.Live(&s.mu)`。
- 18 处 repo guard 不变（`if !r.store.inLiveTx(ctx)`；契约不变，仅内部由
  live-lease 判定取代 witness Holds 判定）。

效果：零 lease（ctx 无 lease）→ `Live` 报 false → 任何 repo 方法走 per-call
`store.mu.Lock()`，并发写 map **不可能**。持 live lease（仅 `runLocked` 路径，且
`runLocked` 未返回、mutex 身份匹配）→ 跳 per-call 锁，整闭包持锁，跨方法原子性
不变（等价 PG SELECT FOR UPDATE-until-commit）。`runLocked` 返回后逃逸 inner ctx
的 lease：`Live` 因 `live=false` 返回 false → fail-closed 强制 per-call 加锁。跨
store 由 mutex 指针身份保证（A 的 lease `Live(&B.mu)` 为 false）。`sync.Mutex`
不可重入约束不变。

### D2. AI-robust 评级：forge=Hard + liveness=Hard（type system + sealed lease），funnel 双向锁

- **上游 Hard（forge）**：`txlock.Lease` 两字段均不导出 → 包外（含 package mem）无法
  `txlock.Lease{mu:…, live:…}` 构造；持锁 + live lease 的唯一来源是
  `txlock.Acquire`（真 Lock + `live.Store(true)`）。ctx value 亦不导出（私有 key 类型）。
  包外任何代码无 API 表面可表达「在 tx 且持锁」——Go 编译器即 gate。
- **下游 Hard（liveness，#972 新增）**：跳 per-call 锁仅当 `l.Live(&s.mu)`，而
  `Live` 的 `live.Load()` 在 `unlock()` 执行后（`runLocked` 返回时 defer 触发）
  永远返回 false。`live` 的翻转**封在 `Acquire` 返回的 unlock 闭包内，外部无
  setter、无 Release 方法、字段不导出**——seal 独占翻转，调用方不可续期。
  逃逸 inner ctx 的 lease 自动失效，fail-closed 退化 per-call 加锁。

闭环成立且**forge 与 liveness 均由 seal 承载（双侧 Hard）**（对照 ai-robust.md
§Funnel 双向锁）。原 #945 witness 中，下游 liveness 是残留 ⚠️（proof 不随锁释放
过期）；#972 lease 将其升为结构性 Hard，与 forge 侧对称。

### D3. archtest `MEM-TX-LOCK-OWNERSHIP-01`（Medium 回归层）

type system（D2 lease seal）是 Hard 主线；archtest 退为 **Medium 回归层**，
守 lease funnel 的纪律漂移：

- **W1**：`txlock.Acquire` 只许在 `(memTxRunner).runLocked` 内调用（lease 唯一
  铸造点；按 func 名 `Acquire` + 包路径后缀 `/txlock` typed 解析；实参须
  `&r.s.mu`，拒嵌套 func-lit 内的调用形态）。
- **W2**：`(*Store).inLiveTx` return 形态 pin 为单值 `return l.Live(&s.mu)`，其中
  `l` 来自 ctx.Value 类型断言（扁平化单 return，无 `&&` 多合取项）。这把下游
  runtime 检查的形态钉死，防 silent regression（如丢 `.Live` 调用或引入额外
  条件）。
- **R1 删除**：原 R1（`memTxToken` 复合字面量范围 pin）随 `memTxToken` 包装器
  一并删除——ctx 直接携带 `txlock.Lease` 值类型，无复合字面量构造点。

旧 R2b（`.holdsLock` selector funnel）已在 #945 删除。`Lease` 字段集冻结（恰好
2 个 unexported 字段：`*sync.Mutex` + `*atomic.Bool`）由
`internal/txlock/txlock_test.go::TestLeaseSealFrozen` reflect 守（tools/archtest
不能 import 双 internal 的 txlock，故 seal freeze 活在 txlock 包自身，镜像
`FixtureOpts` freeze 范式）。盲区（reflect/unsafe 写 `Lease` 字段）由 mem +
txlock 两包禁 import reflect/unsafe 的反向自检关闭；vacuous-pass 由
companion-index `TestMemTxLockOwnership01_FindsSanctionedSites` + real-source
反向自检 `TestMemTxLockOwnership01_FixturePattern`（`internal/memtxlockfixture`）
防。

const-fold note：#945 原提案拟用 go/types 常量折叠 pin `holdsLock` bool 值；
witness/lease 删除该 bool，无值可折叠、无 bool-const 替换盲区，故 const-fold
取消（typed 模式仍用于解析 `Lease` 类型与 `Acquire` 包）。

### D4. 被删测试改为活体回归（不删除）

`TestChangePassword_ConcurrentRequests_ExactlyOneSucceeds` 保留并接
`simpleTxRunner`（零 lease 路径），断言重构为竞争安全不变量：无 fatal/
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
| ent/ent `ent.go` | ctx 存强类型 `*Tx` 指针非 bool；不持连接者无法伪造 | 与 `txlock.Lease` 同构；lease 自失效是 GoCell 特有（ent 无"假 tx"场景）非反模式 |
| go-gorm/gorm `finisher_api.go` | in-tx 靠 `ConnPool.(TxCommitter)` type assertion 非 bool | "对象类型而非 bool 判断 in-tx"同构 |
| go-kratos/examples `data.go` | ctx 存 `*queries.Queries` 非 bool | 同向背书 |
| kubernetes/client-go `tools/cache/thread_safe_store.go` | 单 RWMutex 无条件加锁，无 sentinel-skip；无跨方法事务需求 | 纯 RWMutex-无条件锁方案被否（GoCell 需 RunInTx 跨方法原子性）；`*Locked` 内部约定 GoCell 已遵循 |
| golang/go `database/sql` + Go 官方 | Mutex 故意不可重入；`*sql.Tx` 显式持有者；`Tx.done atomic.Bool` + `ErrTxDone`：commit/rollback 后资源自失效（done 翻转封在 commit/rollback 内，调用方不可续期） | "外持锁+内不重入"是唯一正确路径；bool-不持锁是设计哲学偏离点；`Tx.done` self-invalidation 形态是 `txlock.Lease.live` 的直接对标 |

```
ref: ent/ent examples/o2o2types/ent/ent.go (typed *Tx in context, no bool sentinel)
ref: go-gorm/gorm finisher_api.go (in-tx via type assertion)
ref: go-kratos/examples transaction/sqlc/internal/data/data.go
ref: kubernetes/client-go tools/cache/thread_safe_store.go (RWMutex, *Locked convention)
ref: golang/go database/sql (explicit Tx ownership; non-reentrant Mutex rationale)
ref: golang/go database/sql sql.go (Tx.done atomic.Bool + ErrTxDone, self-invalidation-on-completion)
```

## 威胁矩阵（逐行重评，#972 lease 第 4 栏）

「改造后」= 238 token 设计；「#945 后」= sealed witness；「#972 后」= self-invalidating lease。
四栏对照便于逐行验证无 verdict 回退。

| 威胁 | 改造前 | 改造后（238 token） | #945 后（witness） | #972 后（lease） | 机制（#972） |
|------|--------|--------------------|--------------------|-----------------|------|
| sentinel 在场 + 不持锁 → 并发写 map（本 flake） | ❌ fatal（偶发） | ✅ 消除（runtime） | ✅ 消除（不变） | ✅ 消除（不变） | 零 lease → `Live` false → 强制 per-call 锁 |
| 包外 fake 伪造「在 tx 且持锁」 | ❌ bool 任意可注入 | ✅ 不可表达 | ✅ 不可表达（不变） | ✅ 不可表达（不变） | `Lease` 字段不导出，编译器 gate（上游 Hard forge） |
| **包内** edit 在 `runLocked` 外 mint 持锁 lease | ⚠️ 无防护 | ⚠️→Medium archtest（bool 仍可在包内赋值） | ✅✅ 编译不可表达 | ✅✅ 编译不可表达（不变） | `txlock.Lease` 字段不导出 + 无 `Acquire` 无 live lease（下游 Hard；W1/W2 archtest 仅守纪律漂移） |
| 构造 lease 后 reflect/unsafe 改持锁字段 | ⚠️ — | ✅ 反向自检（mem 禁 reflect/unsafe） | ✅ 反向自检（扩至 txlock 包） | ✅ 反向自检（不变） | mem + txlock 两包禁 import reflect/unsafe（archtest） |
| ctx 携带的 lease 被用来**解锁**（liveness 写路径） | n/a | ⚠️ token 含可解锁句柄（理论） | ✅ 不可表达 | ✅ 不可表达（不变，加固） | `Lease` 无 Release 方法；unlock 闭包留 `runLocked` 本地；`live` 亦不可在 unlock 闭包外写——liveness 双重结构性（无 Release + live seal 独占） |
| after-commit hook 闭包捕获 inner ctx（含 lease），在锁释放后调 repo → stale-skip | ⚠️ 同属性（bool 不随释放过期） | ⚠️ 同属性 | ⚠️ 同属性（proof 永不过期） | ✅ **结构性消除** | `unlock()` 在 `runLocked` 返回时 defer 触发，先于 `RunInTx` drain hooks；`live.Store(false)` 封在 seal unlock 闭包内，调用方不可绕过 → 逃逸 inner ctx 的 `inLiveTx` 返回 false → fail-closed per-call 加锁 |
| 跨 store lease 混用（A 的 lease 让 B 跳锁） | ⚠️ bool 无 store 维度 | ✅ 拒绝（`tok.store == s`） | ✅ 拒绝（不变） | ✅ 拒绝（不变） | `Lease.Live(&s.mu)` mutex 指针身份（删 store 字段后同等保证） |
| RunInTx 跨方法原子性丢失（PG FOR UPDATE 等价） | ✅ 持锁全程 | ✅ 不变 | ✅ 不变 | ✅ 不变 | 持 live lease 跳 per-call 锁，闭包持锁；`runLocked` 返回前 live=true 全程成立 |
| 单 goroutine fake 测试退化 | ✅ | ✅ 不变 | ✅ 不变 | ✅ 不变 | per-call 锁串行天然原子（无并发） |

无格子从 ✅ 回退到 ⚠️/❌：#945 把第 3 行 ⚠️/Medium 升为 ✅✅（编译 Hard），第 5 行
（lease 解锁 liveness）保持 ✅，`live` 亦不可在 unlock 闭包外写，liveness 双重
结构性（无 Release + live seal 独占）进一步加固。#972 把第 6 行
（after-commit hook 闭包捕获 inner ctx → stale-skip）从 ⚠️ 改善为 ✅，结构性消除该残留
（lease 自失效，`unlock()` 在 drain 前翻 `live=false`，逃逸 inner ctx fail-closed）。
原文「gh issue #972 跟踪未来 accesscore 引入 hook 时需补的静态守卫……属诚实登记的残留
⚠️」**已由 #972 结构性消除，非延迟**——任何时刻 accesscore 注册 after-commit hook 并
捕获 inner ctx，`inLiveTx` 均因 lease 失效返回 false，无需额外静态守卫。

## contract-fanout 回灌（5 载体）

本次改动（#972）局限 `cells/accesscore/internal/mem` 内部实现，**不触发
contract-fanout**：无 Go interface 方法签名变化、无 error sentinel 变化、无返回值
metadata 变化、无 contract.yaml / DB schema 变化、无 errcode 新 Kind/Category/Sentinel。

- 接口/语义定义：store.go 包 godoc + `inLiveTx` godoc 重写（锁所有权 + 自失效语义）。
- 全部实现：mem 单实现；user_repo/role_repo godoc lock contract 段同步（`inLiveTx` 取代 `txHoldsLock`）。
- 各层 test：并发回归（活体）+ 6 fake/4 包单 goroutine 回归 + archtest + PG integration。
- 测试夹具：6 fake 注释订正；`identitymanage_credential_race_test.go` 两处
  「causes concurrent map writes under -race」**已失真**注释重写（store-bound
  TxRunner 现因 cross-method atomicity 而选，非防 corruption）。
- 公开 docs/ADR：本 ADR + backlog 条目 + store.go 引用本 ADR。

## Rollback

回退 store.go（lease→witness→token→bool）+ 删 txlock 包演进 + 删 archtest。flake 复现（已知
`-race -count` 必现），不建议。

## Hard 范本登记

#972 后落入 ai-robust.md §Hard 范本「**sealed construction**」+「**single sanctioned
holder**」：context-carried 锁所有权真值经 `internal/` 子包 + unexported 字段 + 私有
构造（`Acquire` 真持锁 + `live.Store(true)`）+ sealed unlock 闭包（唯一可翻 `live`
的路径）使「在 tx 且持锁」与「proof 在锁持有期内有效」**双重编译不可表达，即便在
owning 包内**。capability/proof 分离对标：

- **`context.WithCancel`**：`cancel()` 闭包是 capability，`ctx.Done()` 是 proof；
  proof（`Done`）只读取状态，cancel 能力与 proof 分离。`Lease` 同形态：`unlock()`
  是 capability（改变 live 状态），`Live()` 是 proof（只读取）；调用方持 proof 但
  不持 capability。
- **`database/sql` `Tx.done` → `ErrTxDone`**：commit/rollback 后 `done` 翻转，后续
  操作 fail-closed（`ErrTxDone`）；proof（"是否在活跃 tx 内"）与资源存活窗口绑定，
  完成后自动失效。`txlock.Lease.live` 同形态：`unlock()` 翻 `live=false`，后续
  `Live()` fail-closed，proof 绑定 `runLocked` 执行窗口而非永久 token。

范本本身已在 ai-robust.md 封闭集内，本条仅登记落地实例，不扩目录。
对标 ent `*Tx` / GORM ConnPool type assertion / context.WithCancel / database/sql Tx.done。
