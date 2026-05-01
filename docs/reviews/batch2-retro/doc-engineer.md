# Batch2 Retrospective — 文档工程师席位

> 基线：`develop @ 1958a5a8`（PR-CFG-I 合入后）
> 范围：batch2 11 PR — D#276 / E#278 / F#281 / H#280 / J#286 / K'#287 / G1#292 / G2#291 / L#331 / M#321 / I#338

---

## 文档漂移 Findings

| ID | Severity | Cx | Evidence（doc:line ↔ code:line） | Root cause | Fix direction |
|---|---|---|---|---|---|
| DRF-01 | High | Cx2 | `docs/archive/specs/201-wm2-key-rotation/quickstart.md:46-60` 展示 `auth.ServiceTokenMiddleware(secret []byte)` 旧签名 + 无 `WithServiceTokenNonceStore`；实际 `runtime/auth/authenticator.go:146` 已是 `NewServiceTokenAuthenticator(ring, ...opts) (Authenticator, error)` fail-closed 签名 | PR-CFG-I 改签名未同步 archive spec | 顶部加 deprecation banner "本节已过时，参见 `cell.MustNewAuthServiceToken`"，或删去 HMAC service token 章节保留 JWT 部分。已登记 `PR338-FU-AUTH-FAIL-CLOSED-DOC-CLEANUP`，不重复登记，核实确认漂移存在。 |
| DRF-02 | Medium | Cx2 | `docs/backlog.md:3-5` 链接 `plans/202604301129-027-release-v1.0-readiness.md` 和 `plans/202604301204-028-post-v1.0-leverage-and-v1.1-roadmap.md`；git status 显示这两个文件已被 `R` rename 到 `docs/plans/archive/`，原路径不再存在 | 027/028 归档后 backlog.md header 链接未同步 | 将两处链接改为 `plans/archive/202604301129-027...` 和 `plans/archive/202604301204-028...` |
| DRF-03 | Medium | Cx1 | `runtime/bootstrap/doc.go:10-11` package-level 示例写 `WithListener(cell.PrimaryListener, ":8080", []cell.ListenerAuth{cell.AuthNone{}})` — 按 `.claude/rules/gocell/runtime-api.md` "SEC-FAIL-CLOSED" 规则，PrimaryListener 应使用 JWT auth；`AuthNone{}` 适用于 HealthListener。示例引导方向错误。 | doc.go 示例未反映三 listener 安全分工 | 改为 `cell.MustNewAuthJWTFromAssembly(asm)` 或加注释说明这是"最小骨架示例，生产应替换为 JWT auth" |
| DRF-04 | Low | Cx1 | `tests/e2e/` 目录（PR-CFG-J 引入 `docker-compose.e2e.yaml`、`Dockerfile.corebundle`、`Dockerfile.migrate`、`scripts/`）无 `README.md`；使用说明只散落在 `docker-compose.e2e.yaml` 头部注释（约 30 行）中，无系统性文档说明 Linux-only 限制、CI 触发条件、本地运行步骤 | e2e harness 上线未配套 README | 新建 `tests/e2e/README.md` 说明：Linux-only（host networking）、前置条件（Docker）、本地运行命令、CI 环境变量（`GOCELL_E2E_PG_AVAILABLE=1`）、清理步骤 |
| DRF-05 | Low | Cx1 | `docs/plans/archive/202604260058-l4-virtual-taco.md:347,525` 仍包含旧错误码 `ERR_AUTH_INSUFFICIENT_ROLE`（"普通用户 GET `/api/v1/config/` 返回 403 `ERR_AUTH_INSUFFICIENT_ROLE`"）；archive plan 属历史归档，但与 PR-CFG-L 纠正后的 `ERR_AUTH_FORBIDDEN` 不一致，未来检阅时产生误导 | PR-CFG-L 纠正错误码未回填 archive（archive 文件应为只读快照，此条低优先级） | 可接受现状（archive 为历史快照）；如维护成本允许，在 archive 文件头加注 "错误码已于 PR-CFG-L #331 纠正为 ERR_AUTH_FORBIDDEN" |

---

## Plan/Backlog 一致性核实

| 检查项 | 状态 | 备注 |
|---|---|---|
| `l4-virtual-taco.md` 进度速览表 11 个 PR 均标 ✅ | 通过 | 代码侧核实：PR-CFG-L #331 `TestHttpConfigGetV1_AuthzNegative` 存在且使用 `ERR_AUTH_FORBIDDEN`；PR-CFG-M #321 `tools/metricschema/`、`tools/archtest/internal/typeseval/`、`kernel/assembly/generator_fingerprint_test.go`、`docs/observability/metrics-migration-acks.yaml` 均存在；PR-CFG-I #338 `NewServiceTokenAuthenticator` 返回 `(Authenticator, error)` 已落地；PR-A66 #333 `runtime/bootstrap/` 已拆为 40+ 文件 |
| `docs/plans/later/202604232330-025-architecture-pr-implementation-plan.md` Wave 2.5 清零 | 通过 | 文件第 4 行明确"Wave 1 / Wave 2 / Wave 2.5 全部清零"；CFG-4 迁入 PR-CFG-L、CFG-6 迁入 PR-CFG-M，引用路径清晰 |
| `docs/plans/202605011500-029-master-roadmap.md` archive 引用路径 | 通过 | 029 roadmap 第 184 行引用 `docs/plans/archive/202604301129-027...` 和 `docs/plans/archive/202604301204-028...`，路径正确 |
| `docs/backlog.md` 顶部 header 引用 027/028 路径 | **漂移** | `backlog.md:3-5` 链接 `plans/202604301129-027...` 和 `plans/202604301204-028...`，这两个文件已 rename 到 `plans/archive/`（git status 显示 R），链接失效。DRF-02 已记录 |
| `docs/backlog.md` PR-CFG-4 状态 | **过期** | backlog P1 表（第 39 行）和工时汇总（第 220 行）仍将 `PR-CFG-4` 列为开放项；但代码核实 PR-CFG-L #331 已落地其全部修复目标（handler admin gate、schema sensitive 字段、contract_test AuthzNegative）。backlog 未将 PR-CFG-4 标为已关闭或删除 |
| `docs/backlog.md` V-A6/V-A7 状态 | **过期** | backlog P1 表保留 V-A6（websocket 迁 coder/websocket）和 V-A7（deprecated adapter methods）条目；`backlog.md:11` 的"最近完成"已注明 #340 PR-A64a 完成 websocket fork + V-A5/A7 清理，但 P1 表本体和工时汇总第 220 行仍列 V-A6/A7 为未完成项 |
| `docs/plans/202604260058-l4-virtual-taco.md` 自身错误码纠错 | 通过 | plan:54,66 明确写明 `ERR_AUTH_FORBIDDEN` 而非 `ERR_AUTH_INSUFFICIENT_ROLE`；纠错记录清晰 |

---

## API/godoc 缺口

| ID | Severity | Evidence | Fix |
|---|---|---|---|
| GOD-01 | Medium | `runtime/auth/nonce.go:68-75` `NoopNonceStore` godoc 写 "every code path operates on a non-nil implementation, and dev-mode opt-out is explicit rather than accidental"；PR-CFG-I 后 `NewServiceTokenAuthenticator`（`authenticator.go:160-163`）在构造期拒绝 `NonceStoreKindNoop`，dev-mode opt-out 通道已不存在，此注释描述语义已失效 | 已登记 `PR338-FU-AUTH-FAIL-CLOSED-DOC-CLEANUP`（不重复登记）。修复方向：将 godoc 改为"sentinel only — signals Noop intent; rejected at construction time by NewServiceTokenAuthenticator" |
| GOD-02 | Low | `runtime/bootstrap/doc.go:9-14` package-level 示例展示 `WithListener(PrimaryListener, ..., []cell.ListenerAuth{cell.AuthNone{}})` — 实际 `AuthNone{}` 用于 HealthListener；PrimaryListener 用 AuthNone 会导致 JWT 校验不生效。doc.go 示例引导方向与架构规范矛盾（已在 DRF-03 记录，此处标 godoc 视角） | 示例改为加注释 "replace with MustNewAuthJWTFromAssembly for authenticated primary listener" 或展示完整三 listener 结构 |
| GOD-03 | Low | `tools/archtest/internal/typeseval/typeseval.go`（PR-CFG-M 新建）— 未检查 package-level godoc 是否存在；新增公开类型 `EvaluateConstString` / `LoadPackages` / `Resolver` / `SharedResolver` 应有简要描述说明其为 archtest 内部工具包 | 补 `doc.go` 或在 `typeseval.go` 顶部加 package-level 注释 |

---

## Seat Digest

- 最高优先级漂移是 `docs/backlog.md` 两处死链（DRF-02：027/028 archive 路径未更新）和两处过期开放项（PR-CFG-4 已由 #331 关闭、V-A6/A7 已由 #340 关闭，但 P1 表和工时汇总未同步）；这直接导致 backlog 工时估算虚高。
- auth 签名漂移（DRF-01 / GOD-01）已有 backlog 条目跟踪（`PR338-FU-AUTH-FAIL-CLOSED-DOC-CLEANUP`），无需重复登记；`bootstrap/doc.go` 示例的 `AuthNone` 误导（DRF-03 / GOD-02）是新增 Low 级缺口，尚无登记。
- `tests/e2e/` 缺 README（DRF-04）是 batch2 新增 harness 的唯一空白文档点，补写成本低（<30min），建议纳入下一个接触该目录的 PR。
