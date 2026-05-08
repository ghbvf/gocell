# PR #417 PG Core Cell 接入与框架化拆分分析报告

日期：2026-05-08

范围：PR #417 `feat/537-pg-accesscore-repo`、当前主干的 `configcore` / `auditcore` PG 接入状态、以及 access/config/audit 三类核心能力的框架化路线。

本报告不是替代 PR #417 的逐行审查结论，而是解释为什么问题集中出现、PR 是否应继续保留、以及后续如何拆分和框架化。

## 1. 结论摘要

PR #417 不建议继续作为“大而全”的最终合并 PR 修到通过。它应该保留为 spike / 素材库 / 问题清单来源，但冻结为 draft 或后续关闭。

主要原因不是单个实现质量问题，而是 PR 范围把多个高风险语义混在一起：

- PG repo / migration / wiring
- user CAS/version
- session / refresh token / JWT claim
- RBAC role revoke 与 login 并发排序
- admin 不变量
- setup first-run
- CI integration discovery
- docs / contracts / generated types

这导致前几轮 review 容易先抓到具体 bug，等这些 bug 修掉后，下一层才暴露出 session/token/RBAC/schema 这类架构不变量问题。

推荐路线：

1. 冻结 PR #417，保留为素材库。
2. 先抽 accesscore 当前最缺的薄框架能力，不要等 #417 修完后再抽。
3. 从 `origin/develop` 串行新开小 PR，每次只抽一个语义主题。
4. `configcore` 作为 PG wiring / TxManager / schema guard 的参考。
5. `auditcore` 后续单独完成 PG + ledger，不要夹在 accesscore PR 中。

## 2. 当前状态判断

### configcore：基本已接入 PG

`configcore` 已经具备比较完整的 PG 接入路径：

- `cmd/corebundle/config_module.go` 负责读取 configcore PG / key / cursor 环境配置。
- `cmd/corebundle/bundle.go` 中 `buildConfigCoreOpts` 在 postgres mode 下创建 PG pool、schema guard、outbox relay、TxManager，并返回 cell options。
- `cells/configcore/postgres/options.go` 提供 `WithPool`，把 PG-backed config repo / flag repo 注入 `configcore`。
- `cells/configcore/internal/adapters/postgres/` 下已有 config / flag repo。
- `adapters/postgres/migrations/004_create_config_entries_and_versions.sql`、`008_create_feature_flags.sql`、`010_add_config_value_cipher.sql` 等已经覆盖 config/flag 持久化。

所以 configcore 可以作为 accesscore 的 PG wiring 参考，尤其是 composition root、pool 生命周期、schema guard、outbox relay、TxManager 这些工程接入模式。

### auditcore：有 PG adapter 雏形，但未完整接入

`auditcore` 当前不是完整 PG 接入：

- `cells/auditcore/internal/adapters/postgres/audit_repo.go` 已存在 PG audit repo。
- 但主 migrations 中没有看到 `audit_entries` 表的正式 migration。
- `cmd/corebundle/audit_module.go` 在 postgres mode 下只追加 outbox writer 和 TxManager，仍然先 `WithInMemoryDefaults()`，没有注入 PG audit repo。

因此 auditcore 目前更像是“有 adapter 雏形，未完成 durable storage wiring”。它不适合作为 accesscore 这次修复的前置阻塞项。

### accesscore：PR #417 暴露的是框架缺口

PR #417 的 accesscore PG 接入不是简单缺少几处代码，而是暴露出通用能力缺口：

- session 表不应保存可重放 bearer token。
- credential / authorization 状态变化需要统一旧凭据失效协议。
- login 签发 role snapshot 与 role revoke 需要 per-user 排序点。
- PG schema 需要正确表达 admin 业务不变量。
- mem / PG repo 需要共用 conformance test。

这些能力如果继续写死在 accesscore 内，后续再抽框架会更难。

## 3. 为什么 PG 接入会暴露这么多问题

PG 接入改变的不是“存储介质”这么简单，而是把原本内存模式下隐含的系统语义变成了持久化、多实例、可备份、可并发提交的现实边界。

### 3.1 accesscore 是认证授权边界

accesscore 管理用户、角色、登录、session、refresh token、锁定、删除、强制改密。接 PG 后，下面这些都变成安全边界：

- token 是否会进入 DB / backup / readonly replica；
- JWT claim 是否会在 role revoke 后继续有效；
- password reset / lock / delete 是否撤销旧 access / refresh；
- login 与 role revoke 谁先提交；
- admin 是否允许交接、是否允许多管理员、是否防止最后一个管理员被移除。

这些不是普通 repo bug，而是认证授权协议的一部分。

### 3.2 configcore 是配置控制面

configcore 的风险集中在：

- CAS/version 防止并发覆盖；
- 配置发布与 rollback 的版本语义；
- sensitive value 加密与 key rotation；
- watcher / outbox 事件传播；
- 手工 SQL 是否破坏 version 语义。

它高风险，但风险形态不同于 accesscore。它能提供 PG 工程模板，不能直接解决 access token / JWT / RBAC revoke 问题。

### 3.3 auditcore 是证据链边界

auditcore 的核心风险是：

- append-only；
- hash/HMAC chain；
- restart 后链头恢复；
- 并发 append 顺序；
- archive / retention；
- 失败重试与幂等。

它适合单独做 `runtime/audit/ledger`，不应和 accesscore PG 接入混在一个 PR 中。

## 4. 审查加深还是架构设计问题

两者都有，但核心是架构/设计问题。

之前的审查更多是 targeted regression：

- 并发 revoke 是否修掉；
- login lock 测试是否通过；
- setup race 是否返回正确状态；
- HMAC fixture 是否有效；
- integration 测试是否能跑。

这类审查回答的是“已知失败点是否修好”。

后续审查上升到系统不变量：

- DB 泄露后是否可重放 token；
- role revoke 后是否还能签出旧 role claim；
- requirePasswordReset 是否让旧凭据失效；
- PG admin unique index 是否表达了真实产品不变量；
- FOR UPDATE API 是否在无 tx 时给出虚假的锁承诺；
- CI discovery 是否按 build tag 语义发现 integration 包。

这类问题不会因为 `go test ./...` 通过而自动消失。现有测试只能证明已有断言成立，不能证明凭据状态机和授权不变量完整。

## 5. 如果一开始参考 configcore，会减少哪些问题

会减少一部分工程接入问题：

- PG pool 生命周期；
- schema guard；
- TxManager 注入；
- outbox writer / relay wiring；
- durable mode fail-fast；
- cell adapter 通过 port 隔离；
- corebundle 组合方式。

但它不能自动解决 accesscore 的 P1：

- bearer token 明文落库；
- JWT role claim stale；
- login vs role revoke 排序；
- password reset / lock / delete 的旧凭据失效；
- admin 业务不变量；
- session + refresh revoke 的统一协议。

因此 configcore 是 PG wiring 模板，不是 access 安全协议模板。accesscore 还需要额外的 auth/session/RBAC 状态机设计。

## 6. PR #417 是否继续保留

建议保留，但角色改变：

- 保留为 spike / 素材库。
- 保留已有测试、migration 雏形、失败案例、审查 findings。
- 不再作为最终合并入口。
- 不继续向里面堆修复。
- 拆分 PR 合并后，#417 最终关闭。

不建议从头做。现有分支已经暴露了边界问题，很多代码和测试可以复用。真正需要重做的是提交边界、设计边界和框架归属，而不是所有实现。

## 7. 是否先做 configcore / auditcore

不建议把 accesscore 挂起，转而先完整做 auditcore。

更合适的判断是：

- `configcore` 已经基本 PG 化，可以作为 CAS / wiring 的参考。
- `auditcore` 还没完整 PG 化，但它的 ledger 问题和 accesscore 的 credential 问题不同，不应成为 accesscore 的前置。
- accesscore 当前 P1 暴露的是 runtime/auth/session 与授权状态失效协议缺口，应优先抽薄框架。

推荐顺序不是“先 config/audit，再 access”，也不是“access 在 #417 里继续修完后再抽”，而是：

1. 从 #417 中抽取最小通用框架能力；
2. 新 PR 接入 accesscore；
3. 再用 configcore 沉淀 CAS；
4. auditcore 后续单独做 ledger + PG。

## 8. 目标框架能力

### 8.1 Credential / Session 框架

位置建议：

```text
runtime/auth/session/
  types.go
  store.go
  fingerprint.go
  revoke.go
  storetest/
    suite.go
```

职责：

- session metadata；
- access token 不可重放引用或 HMAC fingerprint；
- session revoke；
- subject-level revoke；
- credential state change 后的旧凭据失效协议；
- mem / PG store conformance test。

不应包含 accesscore 的产品语义，例如 admin、role name、password policy。

### 8.2 CAS / Mutable State 框架

位置建议：

```text
runtime/state/cas/
  version.go
  conflict.go
  storetest/
    suite.go
```

职责：

- version / etag；
- compare-and-swap update；
- conflict error 标准化；
- manual SQL / migration 必须 bump version 的规则；
- configcore 和 accesscore user version 可复用。

### 8.3 Audit / Ledger 框架

位置建议：

```text
runtime/audit/ledger/
  entry.go
  chain.go
  store.go
  storetest/
    suite.go
```

职责：

- append-only ledger；
- hash/HMAC chain；
- restart 链头恢复；
- idempotency key；
- verify / gap detection；
- PG append-only conformance。

### 8.4 PG 实现层

位置建议：

```text
adapters/postgres/
  session_store.go
  cas_helpers.go
  audit_ledger_store.go
  migrations/
    0xx_sessions.sql
    0xx_audit_ledger.sql
```

注意：`adapters/postgres` 放 PG 实现，不放 access/config/audit 的产品语义。

### 8.5 Cell 业务层

```text
cells/accesscore/
  internal/domain/      # user / role / admin / password reset 等业务不变量
  internal/ports/       # accesscore 需要的窄接口
  internal/mem/         # mem 实现，跑 conformance
  slices/               # login/logout/refresh/identity/rbac 编排

cells/configcore/
  internal/domain/      # config entry / feature flag / publish 语义

cells/auditcore/
  internal/domain/      # audit event / actor / retention 语义
```

Cell 只声明业务语义和用例编排，不再各自手写通用安全/一致性机制。

## 9. 串行拆分路线

### PR 0：CI integration discovery

目的：先修验证基础设施。

内容：

- 按 `//go:build integration|e2e` 文件发现 package；
- 不把“有任意测试文件”误判为 integration package；
- 补 archtest 防止规则退化。

这是低耦合 PR，应优先合并。

### PR 1：Credential / Session ADR

目的：先把 access 凭据状态协议写清楚。

内容：

- access token 不落明文；
- session 表存不可重放引用或 HMAC fingerprint；
- password reset / lock / delete / role revoke 对旧凭据的失效语义；
- login 与 role revoke 的排序方案；
- refresh chain 与 session revoke 的边界。

这可以是纯文档 + 测试计划 PR，也可以和薄接口一起提交。

### PR 2：runtime/auth/session 框架

内容：

- `runtime/auth/session` 接口与类型；
- mem implementation 或 test fake；
- `storetest` conformance；
- token fingerprint helper。

目标：先不接 accesscore，只提供可审查的通用协议。

### PR 3：PG session store

内容：

- PG migration；
- PG implementation；
- conformance integration test；
- no plaintext bearer token archtest。

目标：证明 PG store 满足框架协议。

### PR 4：accesscore session/login 接入

内容：

- 从 #417 抽 accesscore session repo / login / validate / logout 相关代码；
- 改为消费 `runtime/auth/session`；
- requirePasswordReset / lock / delete / change password 统一 revoke；
- 补并发 login vs role revoke 测试。

目标：解决 accesscore 当前最关键的 P1。

### PR 5：admin 不变量

内容：

- 决策 admin 是“至少一个”还是“只能一个”；
- PG / mem / tests / docs / contracts 一次统一；
- 不和 session/token 混。

目标：把产品不变量显式化。

### PR 6：state/cas 框架

内容：

- 从 configcore 现有 PG/version 行为提炼 CAS helper / storetest；
- accesscore user version 接入；
- 手工 SQL 文档补 `version = version + 1`；
- conflict response 标准化。

### PR 7：audit ledger + PG

内容：

- `runtime/audit/ledger`；
- `audit_entries` migration；
- PG audit repo wiring；
- restart 链头恢复；
- append-only / verify / query integration tests。

目标：完成 auditcore durable storage，不阻塞 accesscore。

## 10. 从 PR #417 抽代码的方法

每个新 PR 都应从 `origin/develop` 开，不从 PR #417 分支直接开。

整文件属于当前主题时：

```bash
git fetch origin
git worktree add worktrees/200-ci-integration-discovery -b fix/200-ci-integration-discovery origin/develop
git -C worktrees/200-ci-integration-discovery restore --source=feat/537-pg-accesscore-repo -- .github/workflows/_build-lint.yml Makefile
```

文件里混了多个主题时，必须按 hunk 抽：

```bash
git -C worktrees/200-ci-integration-discovery restore -p --source=feat/537-pg-accesscore-repo -- Makefile
```

原则：

- 整文件属于一个主题：`restore --source`。
- 文件混主题：`restore -p`。
- hunk 还混主题：手工编辑 hunk 或重新实现这一小段。
- 每个 PR 合并后，再从新的 `origin/develop` 开下一个 worktree。

## 11. 合并门禁

后续每个拆分 PR 应至少满足：

- `go test ./...` 通过；
- 相关 integration tests 通过，或明确记录外部依赖失败；
- mem / PG 跑同一套 conformance test；
- migration 带 lock timeout / down safety；
- security archtest 覆盖不可回退规则；
- 文档、contract、generated types 与行为一致；
- PR 描述列明是否改变外部行为。

access/session/RBAC 类 PR 还应额外覆盖：

- DB/backup 泄露不能得到可重放 bearer token；
- password reset / lock / delete 后旧 access / refresh 不可继续使用；
- role revoke 与并发 login 不会签出旧 role claim；
- last-admin guard 与 admin handoff 行为一致；
- refresh revoke 与 session revoke 在事务边界上明确。

## 12. 需要先决策的问题

### admin 不变量

必须明确选择之一：

- 至少一个 admin：允许多个 admin，禁止删最后一个；
- 只能一个 admin：禁止新增第二个 admin，需要显式交接流程。

当前倾向应是“至少一个 admin”，因为更符合交接、应急管理员、mem 行为和常见运维需求。但这是产品/架构决策，不应由 PG unique index 隐式决定。

### login 与 role revoke 排序点

需要明确机制：

- per-user advisory lock；
- user authz epoch / role version；
- session creation 与 role snapshot 在同一事务边界；
- role revoke 更新 epoch 并 revoke 旧 session；
- validate 时检查 epoch。

如果选择 pure revoke sweep 而没有 epoch/fence，需要证明并发 login 不会落入 sweep 之后。

### access token 状态模型

需要明确：

- access token 是否有 `jti`；
- session 表存 `jti` 还是 HMAC fingerprint；
- validate 是只看 sid，还是 sid + fingerprint / epoch；
- DB 泄露后的安全假设是什么。

## 13. 最终建议

推荐立即执行的决策：

1. PR #417 转 draft 或冻结，不继续堆修复。
2. 先拆 `CI integration discovery`，提升后续验证质量。
3. 写一份 `Credential / Session ADR`，明确 token/revoke/order 不变量。
4. 抽 `runtime/auth/session` 薄框架和 conformance test。
5. 再从 #417 抽 accesscore PG 接入到小 PR。
6. `configcore` 作为 CAS 和 PG wiring 参考，不作为 access 安全协议替代。
7. `auditcore` 单独排期完成 ledger + PG，不阻塞 accesscore。

这样做会比继续在 #417 里修慢一些，但能显著降低“修完又冒出一批 P1”的概率。问题会从“review 发现系统不变量缺失”转变为“每个 PR 在既定框架协议下补齐一个小能力”。
