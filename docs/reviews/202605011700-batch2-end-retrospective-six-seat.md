# Batch2 终点 Retrospective — 6 角色累计 diff 审查

> 范围：`develop @ 5313793b..1958a5a8`（11 个 PR：PR-CFG-D #276 / E #278 / F #281 / H #280 / J #286 / K' #287 / G1 #292 / G2 #291 / L #331 / M #321 / I #338，累计 920 文件 +77K/-10K）
> Plan：`docs/plans/202604260058-l4-virtual-taco.md`
> 各席位独立报告：`docs/reviews/batch2-retro/{architect,kernel-guardian,reviewer,devops,doc-engineer,product-manager}.md`
> 评审日期：2026-05-01

## Preflight

- repo: `ghbvf/gocell`
- reviewTargetType: `cumulative-batch`
- base...head: `5313793b...1958a5a8`
- changedFiles: 920
- evidenceSource: 本仓库 `git log --first-parent` + `Read`/`Grep`/工具自检
- 排除：PR-A66 #333（独立 batch3，plan line 158 明确「不与治理批次混合」）

## 6 角色席位

| Seat | Agent | 主张兑现 | Findings 数 | 关键结论 |
|------|-------|---------|------------|---------|
| 架构 | architect | 12/14 ✅ | 2 (P1) | A66 plan 说 sub-struct 实际 flat + comment-group；G.9.1 archtest 未迁 typeseval |
| Kernel Guardian | kernel-guardian | 全治理硬约束兑现，validate --strict 0 errors | 3 (2×🔴 + 1×🟡) | archtest 未 SkipDir worktrees/；listener guard 把 bak/docs 当 active |
| 综合（安全/测试/DX） | reviewer | 4 层 fail-closed 不可绕过 | 11 (2×P1 + 6×P2 + 3×P3) | SEC-02 cfg.metrics nil panic 真崩溃；DX-02 ChangePassword TOCTOU |
| DevOps | devops | 4/5 CI 主张兑现，golangci-lint 0 issues | 3 (1×P2 + 2×P2/P3) | bootstrap-admin.sh 4 处 ${VAR:-default} 违 no-soft-fallback |
| 文档 | doc-engineer | plan 已修第 24 行；025 Wave 2.5 清零 | 8 (2×P1 + 4×P2 + 2×P3) | bootstrap/doc.go 示例 PrimaryListener 用 AuthNone{}；backlog.md 死链 |
| 产品 | product-manager | errcode/journey 终态正确，5×3 examples 无 INSUFFICIENT_ROLE | 5 (Low/Med) | J-ordercreate 在 status-board 但 yaml 缺；docker-compose Linux-only 未提示 |

## 跨席位重复 finding 合并

| 合并 ID | 来源席位 | 主题 | 处置 |
|---------|---------|------|------|
| BR-DUP-01 | Architect ARCH-A66 + Plan 自身 | A66 plan 声称 5 sub-struct 但代码是 flat + comment group | plan 文本回写（去 batch2 范围外，仅备注） |
| BR-DUP-02 | Kernel-Guardian KG-RETRO-03 + DevOps DO-F2 | `--boundary-only` flag plan 声称要做但代码反向（断言不存在），CI 用 `verify generated` | plan 文本回写为「verify generated metadata-driven」 |
| BR-DUP-03 | Doc DRF-01 + Doc GOD-01 | 旧 ServiceTokenMiddleware 签名 + NoopNonceStore docstring 漂移 | 已登记 backlog `PR338-FU-AUTH-FAIL-CLOSED-DOC-CLEANUP`，不重登 |
| BR-DUP-04 | Doc DRF-03 + Doc GOD-02 | bootstrap/doc.go 示例 PrimaryListener 用 AuthNone{} | 单条 fix |

## Consolidated Findings（去重后）

### 立即 follow-up（P1，建议 1 PR 打包）

| ID | Severity | Cx | Owner | Evidence | Fix direction |
|----|----------|----|-------|----------|---------------|
| BR-P1-01 | P1 | Cx1 | reviewer SEC-02 | `runtime/auth/servicetoken.go:257-266` `cfg.metrics.recordServiceVerify` nil panic when `WithServiceTokenMetrics` 未注入 | `ServiceTokenMiddleware` 初始化加 `if cfg.metrics == nil { cfg.metrics = noopAuthMetrics{} }`，照 logger 默认化范式 |
| BR-P1-02 | P1 | Cx1 | reviewer DX-02 | `cells/accesscore/slices/identitymanage/service.go:471-473` `ChangePassword` `GetByID` + `CompareHashAndPassword` 在 tx 外，并发 ChangePassword TOCTOU | `GetByID + Compare + Generate` 整体移入 `updatePasswordAndRevokeSessions` 的 tx 闭包；`SELECT ... FOR UPDATE` 或 UpdatedAt 乐观锁 |
| BR-P1-03 | P1 | Cx2 | kernel-guardian KG-RETRO-01 | `tools/archtest/*` `collectGoFiles` 未 `filepath.SkipDir` 排除 `worktrees/`，被 sibling worktree 的 deliberate-bad-syntax fixture 污染 | walker 加 `if d.Name() == "worktrees" { return filepath.SkipDir }` |
| BR-P1-04 | P1 | Cx2 | kernel-guardian KG-RETRO-02 | `TestListenerDXA52Guard` 把 `bak/docs/`、`docs/bak/` 当 active docs，13 处旧 listener API 字面量误判 | guard 加路径过滤 `if strings.Contains(p, "/bak/") \|\| strings.HasPrefix(p, "bak/")` |
| BR-P1-05 | P1 | Cx1 | doc-engineer DRF-03 / GOD-02 | `runtime/bootstrap/doc.go:10-11` package-level 示例 `PrimaryListener` 配 `[]cell.ListenerAuth{cell.AuthNone{}}` —— 与 PrimaryListener 应用 JWT 的安全规范矛盾，会误导消费者 | 示例改用 `cell.AuthJWT{...}` 或加注释「示例 only，生产必须 JWT」 |

### 跨 PR 漂移（P2，可单独修或归 follow-up PR）

| ID | Severity | Cx | Owner | Evidence | Fix direction |
|----|----------|----|-------|----------|---------------|
| BR-P2-01 | P2 | Cx1 | devops DO-F1 | `tests/e2e/scripts/bootstrap-admin.sh` 4 处 `${VAR:-default}` (BASE_URL/USERNAME/EMAIL/PASSWORD)，违反 `feedback_no_soft_fallback.md`（CI 内部基建禁 default） | 改 `set -u` 强制必需，CI workflow step 显式 export |
| BR-P2-02 | P2 | Cx2 | devops DO-F3 | `runtime/auth/...` + `cells/accesscore/slices/sessionlogin/...` 不在 `_build-lint.yml:255` integration-test scope | scope 加 `runtime/auth/... cells/accesscore/...` |
| BR-P2-03 | P2 | Cx2 | architect ARCH-G91 | `tools/archtest/storage_backend_test.go` 仍用 `go/parser` + `buildLocalVarValues` hand-rolled，PR-CFG-M 旁边的 typeseval 未被复用 | 迁到 `tools/archtest/internal/typeseval`，与 outbox_topic_test 共享 resolver |
| BR-P2-04 | P2 | Cx1 | doc DRF-02 | `docs/backlog.md:3-5` 引用 `plans/202604301129-027-...md` + `plans/202604301204-028-...md`（已 git rename 到 `plans/archive/`） | 链接改 `archive/` 路径 |
| BR-P2-05 | P2 | Cx1 | doc DRF-02b | `docs/backlog.md` P1 表 + 工时汇总仍把 `PR-CFG-4` (#331 已合) + `V-A6/V-A7` (#340 已合) 列开放，工时虚高 | 状态改 ✅ + 工时汇总减 |
| BR-P2-06 | P2 | Cx1 | pm PM-F1 | `journeys/status-board.yaml:41` 引用 `J-ordercreate` 但 `journeys/J-ordercreate.yaml` 不存在 | 删除 entry 或补 yaml |
| BR-P2-07 | P2 | Cx1 | reviewer DX-03 | `cells/configcore/slices/configwrite/handler.go:44` `key := r.PathValue("key")` 无 pattern 校验（与 identitymanage UUID 路径强校验不一致） | contract.yaml `pathParams.key.pattern: "^[a-zA-Z0-9._-]{1,255}$"` + handler 用 ParsePathParam |
| BR-P2-08 | P2 | Cx1 | reviewer DX-01 | `runtime/auth/servicetoken.go:368-381` `classifyServiceTokenVerifyError` 用 `strings.Contains(msg, ...)` 决定 metric label，错误措辞改动会静默断告警链 | 改 `errors.As(err, &ec); switch ec.Code` |

### Plan 文本回写（不动代码）

| ID | Owner | 现实 | Plan 应改 |
|----|-------|------|-----------|
| BR-PLAN-01 | architect ARCH-A66 / kernel-guardian KG-RETRO-03 / devops DO-F2 | A66 实际是 flat + comment groups（不是 5 sub-struct）；M.6 实际是 `verify generated` metadata-driven（不是 --boundary-only flag） | plan §A66 / §M.6 文本回写当前实现策略与理由 |

### 已登记 backlog 不重登

- `PR338-FU-AUTH-FAIL-CLOSED-DOC-CLEANUP` (P3/Cx1) — 含 DRF-01 + GOD-01
- `PR333-BOOTSTRAP-OPTION-CROSS-CONCERN` (P2/Cx2) — A66 self-review 设计债
- `PR333-RMQ-CLOSE-DEADLINE-FLAKE` (P2/Cx1) — CI flake

### Low/info（不必单独修）

| ID | 来源 | 说明 |
|----|------|------|
| BR-LOW-01 | reviewer SEC-01 | `runtime/auth/keys.go` log key 名是 `kid` 不是 `key_id`，K' grep 断言字段名不匹配但 thumbprint 非密钥材料，仅澄清 |
| BR-LOW-02 | reviewer TEST-04 | `tests/e2e/docker-compose.e2e.yaml:128` healthcheck `wget` —— 需确认 Dockerfile.corebundle 是否含 wget；若缺需自定义 probe |
| BR-LOW-03 | reviewer TEST-01 + TEST-02 | analyzer Benchmark 边界 case + configread happy-path 经 mux 走 policy（小覆盖缺口） |
| BR-LOW-04 | doc DRF-04 | `tests/e2e/` 缺 README.md（说明 Linux-only + 本地运行 + CI 触发） |
| BR-LOW-05 | doc DRF-05 | archive 旧 plan 仍有 `ERR_AUTH_INSUFFICIENT_ROLE`（archive 历史快照，不修） |
| BR-LOW-06 | doc GOD-03 | `tools/archtest/internal/typeseval/` 缺 `doc.go` package-level godoc |
| BR-LOW-07 | pm PM-F2 | `tests/e2e/docker-compose.e2e.yaml:21-28` Linux-only 未在文档显著位提示 |
| BR-LOW-08 | pm PM-F3 | ssobff quickstart 未把 `GOCELL_STATE_DIR` 提到醒目位 |
| BR-LOW-09 | pm PM-F4 | `NewServiceTokenAuthenticator` 改 error-first 无 migration guide / CHANGELOG |
| BR-LOW-10 | pm PM-F5 | 缺架构层「contract_test vs e2e 覆盖分工」文档 |
| BR-LOW-11 | pm 范围漂移观察 | batch2 工时估算系统性高估 ~56%（实际 ~22h vs 原估 ~50h）；CFG-5 won't-do 未对齐到 029 master roadmap |
| BR-LOW-12 | pm | `J-ssologin.yaml:15` 引用未启动 Phase 3 OIDC，长期不可达 |

## Explicit Adjudication

- **P0**：none
- **P1 follow-up PR 必要性**：YES。BR-P1-01 (nil panic) + BR-P1-02 (TOCTOU) 是真实运行时风险；BR-P1-03 + BR-P1-04 是治理工具自身污染；BR-P1-05 是文档误导消费者用错 auth。建议打包成 `PR-BATCH2-RETRO-FU` 一并修复（~6-8h）。
- **plan 文本回写**：BR-PLAN-01 单独 docs PR 即可（~30min）。
- **Cx1 IN_SCOPE 自动派**：BR-P2-04/05/06/07/08 全部 Cx1 单文件改动，可派 `developer` agent `/fix`；但建议合并到上述 follow-up PR 里走单次 review。
- **跨席位 boundary-only flag** 三席位独立发现同一漂移（KG-RETRO-03 / DO-F2 / 隐含 ARCH-A66 plan-vs-impl）—— 高置信度，plan 必修。
- **未发现的好消息**：4 层 fail-closed 不可绕过 / no-dash id 全仓 0 命中 / 旧 metadata 字段名（cellId/sliceId 等）全死 / `gocell validate --strict` 0 errors / golangci-lint 0 issues / 5 个 examples 无 INSUFFICIENT_ROLE 残留 / 401/403 envelope schema 跨 listener 一致。

## 执行清单（plan §ExitPlanMode 步骤 7-8 闭环）

### 步骤 7（已完成）
- [x] 6 角色 retrospective 累计 diff —— 本文档

### 步骤 8 人工确认
- [x] 025 plan Wave 2.5 清零（CFG-1 ✅ / CFG-4 → L #331 ✅ / CFG-5 won't-do / CFG-6 → M #321 ✅）—— doc-engineer 席位证实
- [ ] backlog 仅剩 2 条 YAGNI（KG-F7 / RA-F5）—— **未达成**：retrospective 新增 5 条 P1 + 8 条 P2，需先关闭 follow-up PR 才能回到「仅 2 条 YAGNI」状态
- [ ] 025 plan 此后只剩 Wave 3 (A15/A16/A17/A36/A37/A38) + Wave 4 (A21/A22/A24/A33) —— 待人工确认

### 后续动作

1. **立即（本次会话或下一会话）**：
   - 把 BR-P1-01..05 + BR-P2-01..08 登记 `docs/backlog.md` 作为 `BATCH2-RETRO-FU-*` 条目
   - 起 PR `PR-BATCH2-RETRO-FU` 打包修复（~6-8h，1 reviewer）
   - 单独 docs PR 回写 BR-PLAN-01（plan §A66 + §M.6 段落）

2. **plan 文件回灌**：
   - `docs/plans/202604260058-l4-virtual-taco.md` 末尾追加 retrospective 完成记录 + 本文档链接

3. **不必立即修**（low/info 12 条）：随机会修，不阻塞批次收尾

---

**审查结论**: batch2 治理改造主目标全部达成（fail-closed / archtest 升级 / e2e harness / metadata 一致性 / contract envelope 统一）；累计 diff 引入 5 条 P1 + 8 条 P2 真实风险/漂移，集中在 (1) auth 配置默认化遗漏 (2) identitymanage 并发安全 (3) 治理工具自污染 (4) 文档误导。建议起 1 个 follow-up PR 打包关闭后再正式宣告 batch2 收尾。
