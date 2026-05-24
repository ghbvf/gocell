# ADR: mem-store tx 锁所有权真值化（根治 sentinel-without-lock）

> Status: Accepted
> Date: 2026-05-17
> Implementation: 238-mem-tx-lock-ownership
> Source plan: /Users/shengming/.claude/plans/https-github-com-ghbvf-gocell-actions-ru-smooth-pumpkin.md
> Amendment: 2026-05-25 (#945) — sealed lock-witness (bool→token Medium funnel
> upgraded to compile-impossible Hard). The Decisions, threat matrix, and Hard
> 范本 below are rewritten to the witness model; the Context / L3 root cause
> records the original bool flake unchanged (accurate history).

## Amendment 2026-05-25 (#945): sealed lock-witness

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

Result: "in a tx context AND holding the lock" is **compile-impossible even
inside package mem** — no code can construct a held witness without calling
Acquire (which locks). The downstream half moves from runtime-AND-check to
type-system-Hard. The archtest (`MEM-TX-LOCK-OWNERSHIP-01`, rewritten D3) is
demoted to a Medium **regression layer** over that Hard core (W1 Acquire site /
W2 txHoldsLock form / R1 token literal scope); the old R2b `.holdsLock` selector
funnel is deleted (no bool field exists). `Held` additionally has **no Release
method** (unlock is a separate closure kept local to runLocked), so a
ctx-carried witness cannot release the lock either.

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

### D1. ctx sentinel：bool → 私有 typed `*memTxToken`（持 sealed witness，#945）

`memTxKey{}` 的 value 类型由 `bool` 改为**包内私有** `*memTxToken`，token 持一个
**不可伪造的 `txlock.Held` witness**（非 bool、非 store 指针）：

```go
// cells/accesscore/internal/mem/internal/txlock（双 internal/，仅 mem 树可 import）
type Held struct{ mu *sync.Mutex }                       // 唯一字段 unexported
func Acquire(mu *sync.Mutex) (held Held, unlock func())  // 唯一铸造点，先 Lock()
func (h Held) Holds(mu *sync.Mutex) bool                 // 指针身份比较（无 Release 方法）

// cells/accesscore/internal/mem
type memTxToken struct {
    held txlock.Held // 零值 holds nothing；mutex 身份即 store 绑定（不需 store 字段）
}
```

- `memTxRunner.runLocked`（`RunInTx` 的 body）：`held, unlock := txlock.Acquire(&r.s.mu); defer unlock()`
  后注入 `&memTxToken{held: held}`。
- `WithTxContext`：注入 `&memTxToken{}`（零 witness，签名不变）。
- `func (s *Store) txHoldsLock(ctx) bool` = `tok != nil && tok.held.Holds(&s.mu)`。
- 18 处 repo guard 不变（`if !r.store.txHoldsLock(ctx)`；`txHoldsLock` 契约不变，
  仅内部由 witness 判定取代 bool）。

效果：零 witness（WithTxContext / fake）→ `Holds` 报 false → 任何 repo 方法走
per-call `store.mu.Lock()`，并发写 map **不可能**。持 witness（仅 runLocked，且
mutex 身份匹配）→ 跳 per-call 锁，整闭包持锁，跨方法原子性不变（等价 PG SELECT
FOR UPDATE-until-commit）。跨 store 由 mutex 指针身份保证（A 的 witness `Holds(&B.mu)`
为 false），故 `store *Store` 字段删除（冗余）。`sync.Mutex` 不可重入约束不变。

### D2. AI-robust 评级：Hard（type system + sealed witness），funnel 双向锁

- **上游 Hard**：`txlock.Held` 唯一字段 `mu` 不导出 → 包外（含 package mem）无法
  `txlock.Held{mu:…}` 构造；持锁 witness 的唯一来源是 `txlock.Acquire`（真 Lock）。
  `memTxToken` 与 `held` 字段亦不导出。包外任何代码无 API 表面可表达「在 tx 且
  持锁」——Go 编译器即 gate。
- **下游 Hard**（#945 由 runtime-AND-check 升级为 type-system）：跳 per-call 锁仅当
  `tok.held.Holds(&s.mu)`，而能让 `Holds` 为 true 的 witness 编译期不可伪造——
  即便在 package mem 内部，无 Acquire（真持锁）就拿不到。`Held` 无 `Release` 方法，
  ctx 携带的 witness 连解锁都不能（unlock 闭包留在 runLocked 本地）。

闭环成立且**双侧 Hard 由类型系统承载**（对照 ai-robust.md §Funnel 双向锁）。
原 238 设计下游靠 `txHoldsLock` 的 `holdsLock && store==s` runtime AND 校验
（Hard 但 runtime）；#945 witness 使该校验退化为 mutex 身份比较，伪造在编译期即
不可达。

### D3. archtest `MEM-TX-LOCK-OWNERSHIP-01`（Medium 回归层）

type system（D2 witness seal）是 Hard 主线；archtest 退为 **Medium 回归层**，
守 witness funnel 的纪律漂移：

- **W1**：`txlock.Acquire` 只许在 `(memTxRunner).runLocked` 内调用（witness 唯一
  铸造点；按 func 名 `Acquire` + 包路径后缀 `/txlock` typed 解析）。
- **W2**：`(*Store).txHoldsLock` return 形态 pin 为 `tok != nil && tok.held.Holds(&s.mu)`
  （扁平化 `&&` 树，恰好 2 合取项：nil-guard + `tok.held.Holds(&<recv>.mu)`）。这把
  下游 runtime 检查的形态钉死，防 silent regression（如丢 `.Holds` 合取项）。
- **R1**：`memTxToken` 复合字面量（typed 匹配当前包 `*types.Named`）只许在
  `runLocked` / `WithTxContext`。

旧 R2b（`.holdsLock` selector funnel）**删除**——无 bool 字段。旧 R2a（禁
`new(memTxToken)`）亦无必要：零 witness token 无害（`Holds` false），且 `held`
不可被赋值为持锁 witness。`Held` 字段集冻结（恰好 1 个 unexported `*sync.Mutex`）
由 `internal/txlock/txlock_test.go::TestHeldSealFrozen` reflect 守（tools/archtest
不能 import 双 internal 的 txlock，故 seal freeze 活在 txlock 包自身，镜像
`FixtureOpts` freeze 范式）。盲区（reflect/unsafe 写 `Held.mu`）由 mem + txlock
两包禁 import reflect/unsafe 的反向自检关闭；vacuous-pass 由 companion-index
`TestMemTxLockOwnership01_FindsSanctionedSites` + real-source 反向自检
`TestMemTxLockOwnership01_FixturePattern`（`internal/memtxlockfixture`）防。

const-fold note：#945 原提案拟用 go/types 常量折叠 pin `holdsLock` bool 值；witness
删除该 bool，无值可折叠、无 bool-const 替换盲区，故 const-fold 取消（typed 模式仍
用于解析 memTxToken 类型与 Acquire 包）。

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

## 威胁矩阵（#945 witness 逐行重评）

「改造后」= 238 token 设计；「#945 后」= sealed witness。三栏对照便于逐行验证
无 verdict 回退。

| 威胁 | 改造前 | 改造后（238 token） | #945 后（witness） | 机制（#945） |
|------|--------|--------------------|--------------------|------|
| sentinel 在场 + 不持锁 → 并发写 map（本 flake） | ❌ fatal（偶发） | ✅ 消除（runtime） | ✅ 消除（不变） | 零 witness → `Holds` false → 强制 per-call 锁 |
| 包外 fake 伪造「在 tx 且持锁」 | ❌ bool 任意可注入 | ✅ 不可表达 | ✅ 不可表达（不变） | `Held`/`memTxToken`/字段不导出，编译器 gate（上游 Hard） |
| **包内** edit 在 runLocked 外 mint 持锁 token | ⚠️ 无防护 | ⚠️→Medium archtest（bool 仍可在包内赋值） | ✅✅ **编译不可表达** | `txlock.Held.mu` 不导出 + 无 Acquire 无 witness（下游 Hard 升级；W1/W2/R1 archtest 仅守纪律漂移） |
| 构造 token 后 reflect/unsafe 改持锁字段 | ⚠️ — | ✅ 反向自检（mem 禁 reflect/unsafe） | ✅ 反向自检（扩至 txlock 包） | mem + txlock 两包禁 import reflect/unsafe（archtest） |
| ctx 携带的 witness 被用来**解锁**（liveness） | n/a | ⚠️ token 含可解锁句柄（理论） | ✅ 不可表达 | `Held` 无 Release 方法；unlock 闭包留 runLocked 本地（type system） |
| after-commit hook 闭包捕获 inner ctx（含 witness），在锁释放后调 repo → stale-skip 锁 | ⚠️ 同属性（bool 亦不随释放过期） | ⚠️ 同属性 | ⚠️ 同属性（**当前不可利用**） | accesscore 零 `RegisterAfterCommit`；框架用 outer ctx drain hooks；gh issue #972 跟踪 accesscore 引入 hook 时的静态守卫 |
| 跨 store token 混用（A 的 token 让 B 跳锁） | ⚠️ bool 无 store 维度 | ✅ 拒绝（`tok.store == s`） | ✅ 拒绝（不变） | `Held.Holds(&s.mu)` mutex 指针身份（取代 store 字段比较） |
| RunInTx 跨方法原子性丢失（PG FOR UPDATE 等价） | ✅ 持锁全程 | ✅ 不变 | ✅ 不变 | 持 witness 跳 per-call 锁，闭包持锁 |
| 单 goroutine fake 测试退化 | ✅ | ✅ 不变 | ✅ 不变 | per-call 锁串行天然原子（无并发） |

无格子从 ✅ 回退到 ⚠️/❌：#945 把第 3 行 ⚠️/Medium 升为 ✅✅（编译 Hard），第 5 行
（witness 解锁 liveness）由「`Held` 无 Release」结构性消除。第 6 行（hook 闭包捕获 inner
ctx）是 witness 与原 bool 共有的 proof-不随释放过期属性，**当前不可利用**（accesscore 无
after-commit hook 调用，框架 drain 路径用无 witness 的 outer ctx），由 gh issue #972 跟踪
未来 accesscore 引入 hook 时需补的静态守卫——属诚实登记的残留 ⚠️，非本 PR 引入的回退。

## contract-fanout 回灌（5 载体）

- 接口/语义定义：store.go 包 godoc + WithTxContext godoc 重写（锁所有权真值）。
- 全部实现：mem 单实现；user_repo/role_repo godoc lock contract 段同步。
- 各层 test：并发回归（活体）+ 6 fake/4 包单 goroutine 回归 + archtest + PG integration。
- 测试夹具：6 fake 注释订正；`identitymanage_credential_race_test.go` 两处
  「causes concurrent map writes under -race」**已失真**注释重写（store-bound
  TxRunner 现因 cross-method atomicity 而选，非防 corruption）。
- 公开 docs/ADR：本 ADR + backlog 条目 + store.go 引用本 ADR。

## Rollback

回退 store.go（witness→token→bool）+ 删 txlock 包 + 删 archtest。flake 复现（已知
`-race -count` 必现），不建议。

## Hard 范本登记

#945 后落入 ai-robust.md §Hard 范本「**sealed construction**」+「**single sanctioned
holder**」：context-carried 锁所有权真值经 `internal/` 子包 + unexported 字段 + 私有
构造（`Acquire` 真持锁）使「在 tx 且持锁」**编译不可表达，即便在 owning 包内**——比
238 的「unexported typed token + runtime AND 校验」更强（下游从 runtime-Hard 升为
type-system-Hard）。capability/proof 分离（`Held` 只证不解锁，unlock 闭包另返）对标
`context.WithCancel`。范本本身已在 ai-robust.md 封闭集内，本条仅登记落地实例，不扩目录。
对标 ent `*Tx` / GORM ConnPool type assertion / context.WithCancel。
