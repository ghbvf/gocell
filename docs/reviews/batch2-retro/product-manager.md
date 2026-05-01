# Batch2 Retrospective — 产品经理席位

> 视角：GoCell 框架消费者（Go 开发者 `go get` 集成方）
> 基线：develop @ `1958a5a8`，batch2 11 个 PR（D/E/F/H/J/K'/G1/G2/L/M/I）累计 diff
> 角色独立结论，未参考其他席位

---

## 错误码消费者体验

| 检查项 | 状态 | 影响 |
|---|---|---|
| `ErrAuthNonceStoreFull` → 503 已就位 | 绿 | `pkg/errcode/errcode.go:356-364` 已存在 `ErrNonceStoreFull = "ERR_AUTH_NONCE_STORE_FULL"`，HTTP 503 语义正确（瞬态容量信号 vs 安全信号），消费者收到 503 即可触发退避重试，不与重放 401 混淆 |
| `ERR_AUTH_INSUFFICIENT_ROLE` 残留扫描 | 绿 | 全仓 grep 无 `INSUFFICIENT_ROLE` 命中（含 `examples/ssobff` `examples/todoorder` `examples/iotdevice`、所有 contract.yaml、所有 *.go）；plan 段 PR-CFG-L 自身就是纠正计划——把原描述的 `INSUFFICIENT_ROLE` 改成 `ERR_AUTH_FORBIDDEN`，消费者代码无硬编码可断点 |
| 401/403 envelope schema 一致性 | 绿 | `cells/configcore/slices/configread/contract_test.go:34,59,134` 均显式调 `c.ValidateErrorResponse(t, status, body)`，跨 read/write/internal 三 listener 共享 `error-response-v1.schema.json`；`runAuthzCases` helper（同文件 :115-137）把校验固化为表驱动模式可复用 |
| 错误信息「有意义但不 leak」 | 绿 | `runtime/auth/authenticator.go:152-164` 三条构造期错误都明确说明「what is wrong + how to fix」（"ring must not be nil" / "requires a NonceStore via WithServiceTokenNonceStore" / "must not be NonceStoreKindNoop"），消费者 wiring 时能定位问题；payload 路径错误（`servicetoken.go:296-347`）正确分类为 fail-fast 不暴露内部 |

**消费者影响**：错误码体验已**显著改善**。PR-CFG-I 把 `runtime/auth` 提升为「第四层 fail-closed」后，消费者无论用 demo 还是 real 模式，misconfig 都在构造期 fail 而不是运行时 401，DX 提升明显。

---

## Journey / 验收一致性

| Journey | auto check | 跑通状态 | 备注 |
|---|---|---|---|
| J-ssologin | 1/3（2 manual） | experimental | OIDC 适配器未就绪，依赖 Phase 3；当前 manual 不阻断 |
| J-sessionrefresh | 3/3 | experimental | checkRef 全 auto |
| J-sessionlogout | 3/3 | experimental | checkRef 全 auto |
| J-useronboarding | 4/4 | experimental | checkRef 全 auto |
| J-accountlockout | 4/4 | experimental | C2 backlog 实现前 checkRef 桩 |
| J-auditlogintrail | 3/3 | experimental | checkRef 全 auto |
| J-confighotreload | 3/3 | experimental | batch2 G1/G2 已加固相关 contract |
| J-configrollback | 4/4 | experimental | checkRef 全 auto |
| J-ordercreate (status-board:41) | — | **drift** | `journeys/status-board.yaml:41` 引用 `J-ordercreate` 但 `journeys/J-ordercreate.yaml` 不存在；examples/todoorder 没有 journey |

**注**：所有 journey lifecycle = `experimental`，不触发 ADV-05 「active journey ≥1 auto check」强制。e2e harness（`tests/e2e/docker-compose.e2e.yaml:21-28`）声明 `network_mode: host` Linux-only，macOS 消费者无法本地复现，需在 README 显著提示。

---

## 初次接入 / DX 成本 Findings

| ID | Severity | Evidence | 用户影响 | Fix direction |
|---|---|---|---|---|
| PM-F1 | Medium | `journeys/status-board.yaml:41` vs `journeys/J-ordercreate.yaml` 文件缺失 | 新消费者跑 `gocell validate --strict` 可能因 journey id 解引用失败而 confused；status-board 是治理面板，drift 直接降低可信度 | 删除 entry 或补 `J-ordercreate.yaml`（todoorder 已具备 ordercreate slice） |
| PM-F2 | Medium | `tests/e2e/docker-compose.e2e.yaml:21-28` `network_mode: host` Linux-only | macOS / Windows 消费者无法本地复现 PR-CFG-J 的 e2e；必须用 Linux runner 或 devcontainer，文档未在显著位置告知 | `tests/e2e/README.md`（若不存在则建）顶部加 platform 限制 banner；或提供 macOS-friendly TLS 路径 |
| PM-F3 | Low | `examples/ssobff/README.md:12-23` quick start 未提示 `GOCELL_STATE_DIR` 是首次跑必备（在 :61-65 才出现） | 新消费者按 :12-17 执行 `go run ./examples/ssobff` 后找不到 initial admin password 文件，体验中断 | quick start 段直接合并 STATE_DIR 导出；或把 default state dir 调整为 OS-aware |
| PM-F4 | Low | `runtime/auth/authenticator.go:146` 签名变 `(Authenticator, error)` | 消费者 wiring 必须从 `auth.NewServiceTokenAuthenticator(ring)` 改为 error-first；ssobff 已迁移（`examples/ssobff/internal_auth.go:31-35`）但**无 migration guide / CHANGELOG entry** | 在 `docs/operations/` 或 `docs/migrations/` 增加一节注明 PR-CFG-I 签名变更；当前唯一文档化在 `.claude/rules/gocell/runtime-api.md`，外部消费者看不到 |
| PM-F5 | Low | `cells/configcore/slices/configread/contract_test.go:191-200` 注释明确 internal listener 「token parsing / nonce replay」端到端覆盖应在 integration test 完成 | 消费者若复制 contract_test 范式，可能误以为 mux 测试已覆盖 listener 完整链路；实际 listener-level 路径要靠 ssobff walkthrough 或 e2e bundle | 在 `docs/architecture/` 加一段「contract_test 与 e2e 的覆盖分工」，避免范式被误用 |

---

## 范围/口径漂移

- **工时差观察**：plan 里多次标注「现状核实精简自 ~14h → 实际 ~5h」（PR-CFG-I `plan:111`、PR-CFG-L `plan:33-35`、PR-CFG-M `plan:78-80`）。**说明 batch2 计划阶段普遍存在「重复登记」**——大量条目在前序 PR（如 #267 PR-CFG-C / G1 / G2）已落地，但 plan 拷贝时未现状核实。建议下一轮 plan 起草前增加「state-of-develop 扫描」步骤（参考 `feedback_scan_before_plan.md`），把工时估算的失真降到 < 30%。当前批次实际兑现 ~22h vs 原估 ~50h，失真 56%。

- **won't-do 决策追踪**：CFG-5 won't-do 在 `plan:275` 提及但未在 `docs/plans/202605011500-029-master-roadmap.md` 「Won't-do」表（:164-178）中列出。同样 `KG-F7` / `RA-F5` 两条 backlog 保留项（plan:255-258）也未进 master roadmap。**消费者层面影响低**（这两条都是治理内部决策），但**项目治理一致性受损**——同一决策需要在两处对齐才不漂移。

- **「文档说支持但代码不支持」漂移扫描**：
  - `examples/ssobff/README.md:9-17` 仍声称「All dependencies are in-memory (no external services required)」，但 :29-37 又出现「Docker Infrastructure」章节，且 e2e 走的是 PG/Redis 真实路径。消费者初读会困惑「到底要不要 docker」。
  - `cmd/gocell scaffold` CLI 在 `.claude/rules/go-standards.md` 列出但未在 ssobff README 引用，新消费者需要从 backlog 才知道工具链入口。
  - `J-ssologin.yaml:15` "OIDC 重定向完成 # Phase 3 OIDC 适配器就绪后验证" — Phase 3 仍未启动（029 master roadmap 全无 OIDC 项），verify 路径持续不可达；建议改 `mode: manual` + `lifecycle: draft` 或直接移除。

---

## Seat Digest

1. **错误码 / wiring DX 在 batch2 显著改善**：`runtime/auth` 第四层 fail-closed + envelope schema 全链路校验 + `ERR_AUTH_FORBIDDEN` 收口，消费者 misconfig 在构造期就能定位，无硬编码 `INSUFFICIENT_ROLE` 残留风险。
2. **journey 治理面板有 1 条 drift（status-board J-ordercreate 文件缺失），e2e harness Linux-only 限制未在文档显著提示**——这是 macOS 消费者本地复现的最大门槛。
3. **plan 工时估算系统性失真 ~56%**（重复登记 + 缺现状核实），建议把「state-of-develop 扫描」纳入下一批 plan 起草的强制步骤；同时 won't-do 决策需要在 plan 与 master roadmap 对齐，避免治理面板分裂。
