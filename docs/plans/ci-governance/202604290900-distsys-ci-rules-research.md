# 分布式系统 / 数据库 CI 规则调研

> 日期：2026-04-30
> 任务：调研 TiDB/CockroachDB/Temporal/Vault/Consul/Dapr/Etcd 等分布式系统/数据库 Go 项目的 CI 规则与治理实践，对标 GoCell backlog2 中"happy path / fencing / fail-closed / errcode 单源 / multi-tenant / timing attack" 类痛点
> 调研者：explorer agent
> 关联文件：`../../backlog2.md`（70 条新增 backlog 源头）、`202604290858-backlog2-ci-governance-analysis.md`（CI 治理候选筛出）、`202604290945-ci-baseline-raw-extraction.md`（19 项目频次矩阵聚合）、`202604300430-golangci-tier12-priority-and-projection.md`（决策文档）

---

## §1 项目矩阵

| 项目 | 主语言 | lint 工具集 | 自定义 check | race CI | integration CI | 治理风格 |
|---|---|---|---|---|---|---|
| TiDB (pingcap) | Go | golangci-lint: bodyclose/errcheck/gosec/revive/staticcheck 14 个 | errdoc-gen 生成 errors.toml 并 diff 校验; check-parallel 禁 t.Parallel() | `make race` (failpoint + ut --race) | 分 part_1/part_2, lightning/br 各自独立 | errclass 分域注册，生成校验，严格认知复杂度 |
| CockroachDB | Go | 自建 pkg/testutils/lint/lint_test.go (200+ 禁令) | syncutil/timeutil/errutil 强制替代标准库; forbid os.Exit/t.Skip; colexecerror panic 路由 | 无独立 Makefile target 但 lint_test 覆盖 | testlogic + roachtest (nightly), TestNightlyLint 独立 | 最严格: 每类操作必须走 internal wrapper，违例 fail-build |
| Temporal | Go | golangci-lint + api-linter; buf breaking | 无自定义 pass | race 默认开(`TEST_RACE_FLAG=on`)，unit/integration/functional 全跑 | 支持 Cassandra/MySQL/PG 三后端集成测试；CI 强制 ensure-no-changes | shard rangeID CAS 作分布式 fencing 基石 |
| Vault (HashiCorp) | Go | golangci-lint + semgrep + deprecations | vet-codechecker + revgrep diff 只扫改动包 | `make testrace` (CGO_ENABLED=1, 60min) | integration tests 独立 INTEG_TEST_TIMEOUT=120m | FencingHABackend 接口作写操作原子守护; subtle.ConstantTimeCompare 密钥比较 |
| Consul (HashiCorp) | Go | golangci-lint: depguard/forbidigo/staticcheck | forbidigo 禁 html/template/ioutil/断言函数 | — | — | 依赖守护优先，net/rpc 要走 fork 版本 |
| Dapr | Go | golangci-lint: depguard/gosec/revive/gocritic 含所有 linter | depguard 禁旧 JWT/logrus/gogo-proto | race 未显式配置 | 集成测试注入 namespace 参数做 actor 隔离 | namespace 线程化传递，key 格式 `<appID>\|\|<actorType>\|\|<actorID>\|\|<key>` |
| etcd | Go | golangci-lint + govulncheck + shellcheck | 无自定义 pass | `--race` 默认开，unit/integration/e2e/robustness 全跑 | integration -p=2, 15min; e2e 30min；ETCD_VERIFY=all | 依赖一致性验证 + proto 生成验证 + license BOM |

---

## §2 模式提取（按 9 个问题）

### Q1 fencing / lease 模式守护

**Temporal** 在 `common/persistence/sql/shard.go:L85-L128` 实现三步 CAS fencing：

```go
// lockShard: L107-128
rangeID, err := tx.WriteLockShards(ctx, sqlplugin.ShardsFilter{ShardID: shardID})
if rangeID != oldRangeID {
    return &persistence.ShardOwnershipLostError{
        ShardID: shardID,
        Msg: fmt.Sprintf("Previous range ID: %v; new range ID: %v", oldRangeID, rangeID),
    }
}
```

源码: https://raw.githubusercontent.com/temporalio/temporal/main/common/persistence/sql/shard.go (L85-L128)

MySQL 端用 `SELECT range_id FROM shards WHERE shard_id = ? FOR UPDATE` 取行级锁，PG 端用 `FOR UPDATE`，再在应用层对比旧值。`UpdateShard` 还检查 `rowsAffected != 1` 防静默失败。**Vault** 用 `physical.FencingHABackend.RegisterActiveNodeLock` 把 HA 锁注册进存储后端，写操作原子验证锁持有状态，防止分区后旧 leader 写入。

源码: https://raw.githubusercontent.com/hashicorp/vault/main/vault/ha.go

没有专门的 lint 守护，但通过 **错误类型强制传播**（`ShardOwnershipLostError` 必须被调用层捕获处理）和集成测试双重保障。

### Q2 构造期 fail-closed

**Vault `core.go:L2947`**：`atomic.CompareAndSwapUint32(c.sealed, 0, 1)` 保证密封状态原子转换；`Logger/MetricSink/SecureRandomReader` nil 时补安全默认值而不是 panic。

**Temporal `internal_worker.go`**：`ensureRequiredParams()` 在 worker 构造时为 Logger/MetricsHandler/DataConverter 补默认值；互斥选项（poller=1、session+versioning 冲突）立即 panic 给调用方明确信号。

源码: https://raw.githubusercontent.com/temporalio/sdk-go/master/internal/internal_worker.go

**Vault `sdk/helper/keysutil/policy.go:L46-47, L1411-1414`**：
```go
const HmacMinKeySize = 256 / 8  // 32 bytes
if p.Type == KeyType_HMAC && (len(key) < HmacMinKeySize || len(key) > HmacMaxKeySize) {
    return fmt.Errorf("invalid key size %d bytes for key type %s", ...)
}
```
源码: https://raw.githubusercontent.com/hashicorp/vault/main/sdk/helper/keysutil/policy.go (L46-47, L1411-1499)

### Q3 race / 并发测试 CI

**Temporal**：`TEST_RACE_FLAG ?= on`，嵌入 `COMPILED_TEST_ARGS`，unit/integration/functional 全量默认带 `-race`，CI 可 `make TEST_RACE_FLAG=off` 豁免。源码: https://raw.githubusercontent.com/temporalio/temporal/main/Makefile

**Vault**：`make testrace` 用 `CGO_ENABLED=1` 跑 60 分钟超时；integration test 独立 `INTEG_TEST_TIMEOUT=120m`，PR 必跑 testrace，nightly 跑 integration。源码: https://raw.githubusercontent.com/hashicorp/vault/main/Makefile

**TiDB**：`make race` 通过 `tools/bin/ut --race` 运行，排除 `unstable.txt`；专门的 `make check-parallel` 禁止任何 `t.Parallel()` 出现在测试文件中（防止非稳定并发）。源码: https://raw.githubusercontent.com/pingcap/tidb/master/Makefile

**etcd**：`--race` 在所有测试类型（unit/integration/e2e/robustness）中默认开启（amd64/arm64），通过 `COMMON_TEST_FLAGS` 统一注入。源码: https://raw.githubusercontent.com/etcd-io/etcd/main/scripts/test.sh

### Q4 integration test 编排

**Temporal**：支持 Cassandra/MySQL/PostgreSQL 三后端独立 integration target；`make ci-build-misc` 包含 `ensure-no-changes` 强制生成产物干净；breaking change 通过 `buf breaking` 在 PR 必跑。

**TiDB**：`test_part_1` 含 integration tests，`lightning_integration_test/br_integration_test` 独立 suite；race target 用 `failpoint-enable` 注入故障点。

**etcd**：`./tests/integration/...` 15 分钟，`./tests/e2e/...` 30 分钟；`-p=2` 并行度；`ETCD_VERIFY=all` + `GOFLAGS=-mod=readonly` 保证不污染 go.sum。

### Q5 errcode 治理

**TiDB**：`tools/check/check-errdoc.sh` 运行 `errdoc-gen`，生成 `errors.toml`，然后 `diff -q` 比对；不一致则 CI 失败。ErrClass 分域注册（ClassDDL/ClassKV/ClassSession 等），`NewStd()` 从 `errno.MySQLErrName` 查标准消息，形成单源。

源码: https://raw.githubusercontent.com/pingcap/tidb/master/tools/check/check-errdoc.sh
源码: https://raw.githubusercontent.com/pingcap/tidb/master/pkg/util/dbterror/terror.go

**CockroachDB**：`pkg/sql/pgwire/pgcode/generate.sh` 从 `errcodes.txt` 生成代码，MakeCode 5 字符 regex 校验；CockroachDB 扩展码单独段落。
源码: https://raw.githubusercontent.com/cockroachdb/cockroach/master/pkg/sql/pgwire/pgcode/codes.go

**Vault**：`sdk/logical/error.go` 定义 sentinel error 变量 + `CodedError(status int, msg string)` 工厂，HTTP status 与 message 单点绑定；下层不散落 strconv。
源码: https://raw.githubusercontent.com/hashicorp/vault/main/sdk/logical/error.go

### Q6 multi-tenant key namespace

**Dapr actor**：key 格式 `<appID>||<actorType>||<actorID>||<key>`，namespace 通过 `Options.Namespace` 线程化传入 backend；但 namespace 本身不写入 key（靠 separate state store 隔离）。
源码: https://raw.githubusercontent.com/dapr/dapr/master/pkg/actors/actors.go

**CockroachDB**：`pkg/keys/constants.go` 定义 `tenantPrefixByte = '\xfe'`，`TenantPrefix = roachpb.Key{tenantPrefixByte}`；`MakeTenantPrefix(tenantID)` 生成每租户专属前缀；SQL 层生成的所有 key 必须带前缀，存储节点验证前缀合法性。lint 层通过 `lint_test.go` 禁止直接访问内部 key 字节，只允许走 `keys.*` 包 API。
源码: https://raw.githubusercontent.com/cockroachdb/cockroach/master/pkg/keys/constants.go

**TiDB**：`metaPrefix = 'm'(0x6D)`, `tablePrefix = 't'(0x74)`，不同数据类型用不同前缀字节，前缀长度加 guard 防越界。

### Q7 timing attack / 安全 lint

**Vault `vault/core.go:L2947`**（解封密钥验证）：`subtle.ConstantTimeCompare(existing, key)` 防止通过响应时间推断密钥内容。

**Vault `sdk/logical/error.go`**：sentinel error 配合统一 `CodedError` 工厂，防止不同错误路径暴露不同信息。

**TiDB `.golangci.yml`**：`revive` 禁止 `import crypto/md5` 和 `import crypto/sha1`，强制使用安全哈希算法。
源码: https://raw.githubusercontent.com/pingcap/tidb/master/.golangci.yml

CockroachDB `lint_test.go` 中 `redact.Unsafe()` 被禁止，必须走 `encoding.Unsafe()`，集中管控敏感数据脱敏路径，减少侧信道暴露面。
源码（摘要）: https://raw.githubusercontent.com/cockroachdb/cockroach/master/pkg/testutils/lint/lint_test.go

### Q8 schema / migration 守护

**CockroachDB `pkg/sql/catalog/validate.go`**：五级验证体系 (NoValidation → AllPreTxnCommit)，`ValidationErrorAccumulator` 聚合多个错误，`ValidationLevelAllPreTxnCommit` 在事务提交前运行完整校验（前向引用 + 反向引用 + namespace 表一致性）。
源码: https://raw.githubusercontent.com/cockroachdb/cockroach/master/pkg/sql/catalog/validate.go

GoCell `adapters/postgres/schema_guard.go` 的 `DetectInvalidIndexes` 结果目前仅 `slog.Warn` 继续启动（B2-X-03），参考 CockroachDB 模式应升级为 readyz 503。

### Q9 Must vs error-first

**Temporal SDK `internal_worker.go`**：`NewAggregatedWorker` 是 error-first（返回 worker 对象，nil 条件下 panic 限于 reserved prefix 检查）；互斥配置用 panic 在 `main` 前暴露。worker.New 只在顶层 `main` 调用，SDK 内部无 MustNew 模式。

**CockroachDB lint_test.go**：`os.Exit()` 强制替换为 `exit.WithCode`；`t.Skip/Skipf()` 替换为 `skip.WithIssue`；CBO 包内 panic 必须用 `errors.AssertionFailedf()` 包装。这些规则锁定了"允许 panic/exit 的上下文"边界，等价于 Must 仅限 cmd 层。

**Vault**：无 forbidigo 配置，但 semgrep 规则扫描 deprecated function 调用（`make deprecations` target）。

---

## §3 对标 GoCell backlog2

| backlog2 条目 | 对标项目 | 具体做法 | 优先参考文件 |
|---|---|---|---|
| **B2-A-01** PG outbox claim fencing | Temporal | `common/persistence/sql/shard.go:L107-128` 的三步 CAS：`SELECT ... FOR UPDATE` → 应用层 compare `rangeID != oldRangeID` → `ShardOwnershipLostError`；`rowsAffected != 1` 兜底 | `common/persistence/sql/shard.go` |
| **B2-A-02** RMQ reconnect terminal | Temporal | `amqp.Error.Code` 分类：403/404/530 等永久错误触发 `ShardOwnershipLostError` 风格的 terminal error，不再重试，容器重启拉新凭证；Vault 的 HA 失败后 `c.standby = true + barrier.Seal()` 同样是 terminal 回退 | `vault/ha.go` |
| **B2-C-01** hash-chain restart recovery | Temporal | shard 启动时通过 `lockShard` 从 DB 读取当前 rangeID 作为"链头"注入，而不是从空值开始；GoCell auditcore 应在 `cell.go initSlices` 阶段 `SELECT last_hash FROM audit_entries ORDER BY id DESC LIMIT 1` 注入 `NewHashChain` | `common/persistence/sql/shard.go:L43-58` |
| **B2-A-09** PG refresh reject timing | Vault `vault/core.go:L2947` | `subtle.ConstantTimeCompare` 统一所有拒绝路径；GoCell refresh_store.go 的"token 不存在" vs "token 已用"两条路径应 dummy query 对齐耗时 + 统一 slog 字段 | `vault/core.go` |
| **B2-A-26** Redis receipt commit race | CockroachDB | 用 `FOR UPDATE` 行级锁 + 事务保证原子 CAS；GoCell Redis 端改用 Lua 脚本 `if redis.call('GET', key) == owner then ... end` 保证 Commit/Release 原子性 | `cockroach/pkg/kv/kvserver/concurrency/lock/locking.go` |
| **B2-A-27** Redis multi-tenant key namespace | Dapr + CockroachDB | Dapr: `<appID>\|\|<type>\|\|<id>\|\|<key>` 构造器强制注入 appID；CockroachDB: `tenantPrefixByte = '\xfe'` + MakeTenantPrefix 函数包装，lint_test 禁直接访问 key bytes。GoCell 应在 `adapters/redis/idempotency.go` 构造期注入 `KeyNamespace`（cell ID）并 prefix 所有 key | `pkg/keys/constants.go` |
| **B2-C-12** HMAC key min length | Vault `sdk/helper/keysutil/policy.go:L46-47` | `HmacMinKeySize = 256/8 = 32`，构造和旋转时均校验 `len(key) < 32` → 返回 error 而非 panic；GoCell `cells/auditcore/cell.go:319` 应同等加 `if len(hmacKey) < 32 { return fmt.Errorf(...) }` | `sdk/helper/keysutil/policy.go` |
| **B2-T-01** config rollback optimistic lock | Temporal shard CAS | `UpdateShard` 先读后写 + WHERE 旧值比较 + rowsAffected 校验；GoCell `UpdateForRollback` SQL 应加 `WHERE version=$expected AND status='active'`，返回 409 `ERR_CONFIG_VERSION_MISMATCH` | `common/persistence/sql/shard.go:L85-103` |
| **B2-A-10** PG readyz schema compatibility | CockroachDB | `pkg/sql/catalog/validate.go` 五级校验在事务提交前运行，参考其将 schema validity 作为服务健康前提的思路。GoCell `Checkers()` 应聚合 Ping + `schema_guard.DetectInvalidIndexes()` 结果，invalid index → 503 | `pkg/sql/catalog/validate.go` |

---

## §4 G1-G5 PR 升级建议

以下 5 个"可直接对照升级"的优先级 1 项目+文件清单：

### P1: Temporal `common/persistence/sql/shard.go`
- 用途: B2-A-01 fencing 实现蓝本
- 具体做法: 在 `outbox_store.go` 增加 `lease_id UUID` 字段，`markPublished` 改为 `UPDATE ... SET status='published' WHERE id=$1 AND status='claiming' AND lease_id=$2`，`rowsAffected=0` 视为 fencing 失败
- 文件: `https://github.com/temporalio/temporal/blob/main/common/persistence/sql/shard.go`

### P2: Vault `sdk/helper/keysutil/policy.go:L46-47`
- 用途: B2-C-12 HMAC key 长度校验 + B2-A-09 timing 防御的 `subtle.ConstantTimeCompare` 模式
- 具体做法: auditcore cell.go 构造期加 `if len(hmacKey) < 32 { return nil, fmt.Errorf("hmac key must be at least 32 bytes") }`；refresh_store 三条拒绝路径统一用 `hmac.Equal()` 或 `subtle.ConstantTimeCompare`
- 文件: `https://github.com/hashicorp/vault/blob/main/sdk/helper/keysutil/policy.go`

### P3: TiDB `tools/check/check-errdoc.sh` + `Makefile` (errdoc target)
- 用途: B2-K-04 errcode mirror drift 治理
- 具体做法: 参考 `errdoc-gen` 生成 errors.toml 并 `diff -q` 的模式，在 GoCell 的 `kernel/governance/rules_http_response_alignment.go` 用 reflect 从 `pkg/errcode` 自动构建 `errcodeNameToStatus` 映射，CI 加 `go generate` + `git diff --exit-code` 守护
- 文件: `https://github.com/pingcap/tidb/blob/master/tools/check/check-errdoc.sh`

### P4: CockroachDB `pkg/testutils/lint/lint_test.go`
- 用途: B2-K-02 Must* 限制 + B2-A-11 constructor error model + B2-A-28 fail-closed 守护
- 具体做法: 参考 CockroachDB "os.Exit 必须走 exit.WithCode" 的禁令模式，在 GoCell `.golangci.yml` 加 `forbidigo` 规则：`MustNew` 禁止出现在非 `cmd/` 包、`Password` 字段在 real mode 必须非空（用 `depguard` 或自定义 archtest）
- 文件: `https://github.com/cockroachdb/cockroach/blob/master/pkg/testutils/lint/lint_test.go`

### P5: Temporal `Makefile` (TEST_RACE_FLAG + ensure-no-changes)
- 用途: B2-A-24/29 Prometheus/Redis race test 缺失 + GoCell CI 补 `-race` 全局开关
- 具体做法: GoCell Makefile 加 `TEST_RACE_FLAG ?= on`，`go test -race ./adapters/redis/... ./adapters/prometheus/...`；CI workflow 加 `make test-race` step，对应 B2-A-24/B2-A-29 两条 backlog
- 文件: `https://github.com/temporalio/temporal/blob/main/Makefile`

---

## §5 引用摘要（供 PR/commit 使用）

```
ref: temporalio/temporal common/persistence/sql/shard.go@main  (fencing CAS)
ref: hashicorp/vault sdk/helper/keysutil/policy.go@main        (HMAC min-length, ConstantTimeCompare)
ref: pingcap/tidb tools/check/check-errdoc.sh@master           (errcode single-source generate+diff)
ref: cockroachdb/cockroach pkg/testutils/lint/lint_test.go@master (Must* restriction, forbidigo pattern)
ref: temporalio/temporal Makefile@main                          (TEST_RACE_FLAG, ensure-no-changes)
ref: cockroachdb/cockroach pkg/keys/constants.go@master        (tenantPrefixByte namespace isolation)
ref: dapr/dapr pkg/actors/actors.go@master                     (appID||type||id||key prefix pattern)
ref: cockroachdb/cockroach pkg/sql/catalog/validate.go@master  (schema validation tiered system)
```

**Source URLs**:
- [temporalio/temporal common/persistence/sql/shard.go](https://github.com/temporalio/temporal/blob/main/common/persistence/sql/shard.go)
- [hashicorp/vault sdk/helper/keysutil/policy.go](https://github.com/hashicorp/vault/blob/main/sdk/helper/keysutil/policy.go)
- [hashicorp/vault vault/ha.go](https://github.com/hashicorp/vault/blob/main/vault/ha.go)
- [hashicorp/vault vault/core.go](https://github.com/hashicorp/vault/blob/main/vault/core.go)
- [hashicorp/vault sdk/logical/error.go](https://github.com/hashicorp/vault/blob/main/sdk/logical/error.go)
- [pingcap/tidb tools/check/check-errdoc.sh](https://github.com/pingcap/tidb/blob/master/tools/check/check-errdoc.sh)
- [pingcap/tidb .golangci.yml](https://github.com/pingcap/tidb/blob/master/.golangci.yml)
- [pingcap/tidb pkg/util/dbterror/terror.go](https://github.com/pingcap/tidb/blob/master/pkg/util/dbterror/terror.go)
- [cockroachdb/cockroach pkg/testutils/lint/lint_test.go](https://github.com/cockroachdb/cockroach/blob/master/pkg/testutils/lint/lint_test.go)
- [cockroachdb/cockroach pkg/keys/constants.go](https://github.com/cockroachdb/cockroach/blob/master/pkg/keys/constants.go)
- [cockroachdb/cockroach pkg/sql/catalog/validate.go](https://github.com/cockroachdb/cockroach/blob/master/pkg/sql/catalog/validate.go)
- [cockroachdb/cockroach pkg/sql/pgwire/pgcode/codes.go](https://github.com/cockroachdb/cockroach/blob/master/pkg/sql/pgwire/pgcode/codes.go)
- [temporalio/temporal Makefile](https://github.com/temporalio/temporal/blob/main/Makefile)
- [temporalio/sdk-go internal/internal_worker.go](https://github.com/temporalio/sdk-go/blob/master/internal/internal_worker.go)
- [etcd-io/etcd scripts/test.sh](https://github.com/etcd-io/etcd/blob/main/scripts/test.sh)
- [dapr/dapr pkg/actors/actors.go](https://github.com/dapr/dapr/blob/master/pkg/actors/actors.go)
- [dapr/dapr .golangci.yml](https://github.com/dapr/dapr/blob/master/.golangci.yml)
- [hashicorp/consul .golangci.yml](https://github.com/hashicorp/consul/blob/main/.golangci.yml)
