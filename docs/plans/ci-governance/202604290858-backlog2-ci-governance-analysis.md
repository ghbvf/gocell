# Backlog2 CI 治理项分析

> 日期：2026-04-29
> 输入：`docs/backlog2.md`（70 条新增 backlog 项）
> 目的：从 backlog2 中识别可纳入 **CI 自动拦截** 的条目，制定治理 PR 打包计划
> 基线：`origin/develop @ 4e2e00ad`

---

## §0 判定原则

**可 CI 治理** 的条目需满足：能用静态扫描 / archtest / contract validate / lint / 计时器（waiver 到期）等无需人工评审的方式持续把守。

**不可 CI 治理** 的条目：需要业务实现、运行时行为决策、外部依赖运行时测试（除非已有 testcontainers harness）。

CI 治理类型：

1. **archtest / governance rule（静态规则）** — 编译期 / build 期拦截
2. **contracts 一致性扫描** — `gocell validate --strict` 或 governance 规则
3. **waiver / lifecycle 时间到期监控** — CI cron / build break
4. **lint / grep（自定义规则或 golangci-lint plugin）**
5. **测试覆盖强制（CI required job）**

---

## §1 archtest / governance rule（最高 ROI，12 条）

直接加 `kernel/governance/` 规则或 archtest 守护，0 运行时成本：

| 条目 | 治理点 | 实现方式 |
|---|---|---|
| **B2-K-02** Must* panic 残留 | 生产路径（`cells/`, `runtime/`, `adapters/`）禁止出现 `Must*` 调用，仅允许 `cmd/` + `*_test.go` | grep 规则 + archtest exception list |
| **B2-K-03** AssemblyRef 隐式类型断言 | 禁止 `asm.(assemblyWithCell)` 这类跨层 type assertion；要求接口在 kernel 显式声明 | archtest 扫 `runtime/`/`kernel/` 中的 type assertion |
| **B2-K-04** errcode mirror drift | `kernel/governance/rules_http_response_alignment.go:errcodeNameToStatus` 改 reflect 自动从 `pkg/errcode` 构建；archtest 校验两侧无差 | 单源 + governance 规则 |
| **B2-R-04** errcode classify 双源 | `expected4xxCodes` ↔ `WriteDomainError` 同样合并到单源 | 单源映射 |
| **B2-R-06** OTel TracerProvider not global | archtest 锁定 composition root 必须调 `otel.SetTracerProvider`；或禁止 `otel.GetTracerProvider()` 在 instrumentation path 出现 noop | archtest |
| **B2-A-08** PG refresh store ambient/standalone tx 混合 | archtest 锁定接口契约：所有 `refresh_store` 方法必须要求 ambient tx，或显式拆两接口 | archtest + 命名约束 |
| **B2-A-11** PG constructor error model mixed | archtest 锁定 `New*` 全 error-first，`MustNew*` 仅作 cmd 顶层 wrapper | grep + archtest |
| **B2-A-18** adapter connect timeout 不一致 | archtest 强制每个 adapter 暴露 `WithConnectTimeout` | API 签名扫描 |
| **B2-A-20** simpleTracer 在生产路径 | archtest 禁止 `cells/`, `adapters/`, `cmd/corebundle/` 引用 `runtime/observability/tracing.simpleTracer` | import path 守护 |
| **B2-A-27** Redis 多租户 key 命名空间 | archtest 锁定 redis client 构造必填 `KeyNamespace` 参数（Cell ID） | 构造期签名 + archtest |
| **B2-C-03** Cell.Init infra type 泄漏 | archtest 锁定 `cells/*/cell_init.go` 不能引用 `adapters/*` 类型；只能依赖 `ports.*` 接口 | import path 守护 |
| **B2-X-04** Health listener default loopback | archtest 锁定 real mode 必须显式 bind | 构造期校验 + archtest |

---

## §2 contracts 一致性扫描（7 条）

可挂在 `gocell validate --strict` 或单独 CI step，0 误判：

| 条目 | 治理点 | 实现方式 |
|---|---|---|
| **B2-C-02** setup 端点常驻 Public | 治理规则：`lifecycle: active` 的 contract 不允许 `Public: true` 用于 bootstrap 类端点；新增 `lifecycle: bootstrap` 状态 | metadata schema + governance |
| **B2-T-01** rollback 缺 optimistic lock | contract 层校验：写类端点必须声明 `expectedVersion`/`If-Match` 或显式标 `concurrencyControl: none`；codepath 同步 SQL CAS | schema 强制 + governance |
| **B2-T-03** v1 response `additionalProperties: false` 与演进冲突 | 扫 30 个 `response.schema.json`，统一改为 `additionalProperties: true` 或 `unevaluatedProperties: false` 加白名单；archtest 锁定 | schema 静态扫描 |
| **B2-T-04** contract 命名 userId vs userID 漂移 | governance 规则：所有 path/query/payload 字段强制 camelCase（与 CLAUDE.md 一致） | metadata parser + lint |
| **B2-T-05** internal contract 残留 external actor/bearer | 治理规则：`kind: internal` 的 contract 禁止出现 `actor` / `authentication.kind: bearer` 字段 | metadata governance |
| **B2-T-08** publish contract 失败码不全 | 治理规则：handler 中返回的 errcode 必须在 contract.responses 中声明（双向校验） | governance + archtest |
| **B2-K-04 衍生** | 通用规则：handler 实际 emit 的 errcode 集合 ⊆ contract.responses 声明的错误码集合 | governance |

---

## §3 waiver / lifecycle 时间到期监控（2 条）

CI cron / build break：

| 条目 | 治理点 |
|---|---|
| **B2-T-02** RBAC assign waiver expiry 2026-07-01 | 在 contract test 中加 `if time.Now().After(2026-07-01) { t.Fatal(...) }` 或 governance rule 扫 `waiver expiry` 日期，过期 build break |
| **B2-X-05** generate indexes 在 help 中暴露但 not implemented | governance 规则：扫 `cmd help` 输出与实际 subcommand return；返回 `not implemented` 必须不在 help 中可见 |

---

## §4 lint / grep（6 条）

自定义规则或 golangci-lint plugin：

| 条目 | 治理点 |
|---|---|
| **B2-K-07** contracttest undeclared key 静默 | lint：`MustValidateRequest` 的 key 参数必须在 declare 列表中；测试自身改 fail-fast |
| **B2-A-21** OTel messaging collector `%d` 字面量 | go vet / `govet -printf` 已能查；纳入 CI required check |
| **B2-X-01** outbox e2e 50ms fixed sleep | lint 扫 `*_test.go` 中的 `time.Sleep` 黑名单（白名单 ready signal helper） |
| **B2-A-19** OTel SetAttributes plaintext + Insecure=true | lint：构造期 `Insecure=true` 时必须配 `WithUnsafePlaintext()` 显式标记；archtest 锁定 |
| **B2-K-05** metadata parser error path leak | lint 扫 error 文本中包含 `os.PathSeparator` 字面拼接的位置 |
| **B2-A-23** Prometheus CellID label 校验 | 构造期正则 `^[a-z][a-z0-9-]*$` 强制；archtest 锁定 |

---

## §5 测试覆盖强制（4 条，非治理但属 CI 范畴）

CI required job：

| 条目 | 治理点 |
|---|---|
| **B2-A-24 / B2-A-29** Prometheus / Redis race test 缺失 | CI required：`go test -race ./adapters/prometheus/... ./adapters/redis/...` |
| **B2-A-17** RMQ EventBus 语义集成测试 | testcontainers RMQ harness 加入 CI nightly |
| **B2-A-32** S3 integration test | testcontainers MinIO 加入 CI nightly |
| **B2-C-04 / B2-C-13 / B2-C-14** L2 原子性 / 跨重启 hash-chain | testcontainers PG harness（与 `TEST-JOURNEY-ROOT-HARNESS-01` 对齐） |

---

## §6 不适合 CI 治理（排除说明）

以下条目必须靠人评审 + 业务实现，CI 只能在落地后写守护测试，无法上游拦截：

- **业务逻辑修复**：B2-A-01（PG outbox claim fencing）、B2-A-02（RMQ reconnect terminal）、B2-A-03（Redis cluster）、B2-C-01（hash-chain recovery）、B2-W-01/02（WS auth & ACL）、B2-A-26（Redis receipt race）、B2-T-06/07（runtime authz 深度）
- **ADR 决策类**：Wave A 全部 6 条
- **运行时配置策略**：B2-R-01/02/03（health/readyz/rollback）、B2-X-03（PG invalid index 策略）
- **性能与并发设计**：B2-C-10（hash-chain mutex 粒度）、B2-A-15（RMQ channel 上限）、B2-W-04/05（WS buffer & stop）
- **observability 业务接入**：B2-W-03、B2-R-05/07/08/09（OTel 业务接入）

---

## §7 治理 PR 打包计划

```
PR-CI-G1  GOVERNANCE-RULES-PHASE1（约 6h，最高 ROI）
  - B2-K-04 errcode mirror 单源
  - B2-R-04 errcode classify 单源
  - B2-T-04 contract camelCase 命名守护
  - B2-T-05 internal contract actor/bearer 禁用
  - B2-X-05 cmd help vs not-implemented 一致性

PR-CI-G2  ARCHTEST-IMPORT-AND-CONSTRUCTOR（约 8h）
  - B2-K-02 Must* 生产路径禁用
  - B2-K-03 AssemblyRef 显式接口 + 类型断言禁用
  - B2-A-11 PG 构造器 error-first 风格锁定
  - B2-A-20 simpleTracer 生产禁用
  - B2-A-27 Redis KeyNamespace 必填
  - B2-C-03 Cell.Init infra type 禁用
  - B2-A-18 adapter connect timeout 统一

PR-CI-G3  CONTRACT-SCHEMA-EVOLUTION（约 6h，与 PR-CI-3 协调）
  - B2-T-03 v1 response additionalProperties 统一
  - B2-T-01 optimistic lock 声明强制
  - B2-T-08 contract.responses ↔ handler errcode 双向校验

PR-CI-G4  LINT-AND-WAIVER（约 4h）
  - B2-A-21 fmt 字面量（go vet 已可，纳入 required）
  - B2-X-01 test sleep 黑名单
  - B2-T-02 waiver 过期 build break
  - B2-K-07 contracttest undeclared key fail-fast

PR-CI-G5  RACE-AND-INTEGRATION-CI（约 4h，调度类）
  - B2-A-24 / B2-A-29 race CI required
  - B2-A-17 / B2-A-32 testcontainers nightly
```

---

## §8 数量统计

| 类别 | 条目数 | 涉及 backlog2 项 |
|---|---:|---|
| archtest / governance rule | 12 | B2-K-02/03/04, B2-R-04/06, B2-A-08/11/18/20/27, B2-C-03, B2-X-04 |
| contracts 一致性扫描 | 7 | B2-C-02, B2-T-01/03/04/05/08, B2-K-04 衍生 |
| waiver / 时间到期 | 2 | B2-T-02, B2-X-05 |
| lint / grep | 6 | B2-K-05/07, B2-A-19/21/23, B2-X-01 |
| 测试覆盖强制 | 4 类（覆盖 7 条） | B2-A-17/24/29/32, B2-C-04/13/14 |
| **CI 治理合计** | **31 条**（约 backlog2 的 44%） | — |
| 不可 CI 治理 | 39 条 | 见 §6 |

---

## §9 落地建议

1. **先做 G1 + G2**（约 14h）—— 这两个 PR 加完后，backlog2 中约 40% 的 P1 项有静态守护，未来同类问题再现时直接 build break，无需再次人工评审
2. **G3 与现有 PR-CI-3 V1-RESPONSE-EVOLVE 协调**（batch2-k8s-verify 计划），避免 schema 改动冲突
3. **G4 / G5 可独立并行**，与 Wave B/C/D 业务 PR 不冲突
4. CI 治理 PR 落地后，对应 backlog2 条目同步追加 `✅ #PR` 标记

---

## §10 与现有 plan 的关系

- **`docs/plans/202604290500-backlog-residual-and-merge-roadmap.md`**：本文为其补充，专注 CI 治理子集；该 roadmap 的业务 wave 不重叠
- **`PR-CI-3 V1-RESPONSE-EVOLVE`**：吸收 B2-T-03，G3 与之协调统一执行
- **`PR-CFG-I AUTH-FAIL-CLOSED-AND-OPS-RESIDUE`**：B2-T-05 在 G1 内静态守护层面落地后，PR-CFG-I 处理运行时残留
- **`TEST-JOURNEY-ROOT-HARNESS-01`**：G5 的 testcontainers PG harness 复用其 framework
