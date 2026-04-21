# Auth Federated Whistle 计划完成度六席位审查报告

> 日期: 2026-04-21  
> 审查对象: `docs/plans/202604191515-auth-federated-whistle.md`  
> 审查方式: 六席位并行（架构/安全/测试/运维/可维护性/产品）+ 三主题开源对标（每主题 >=3 项目）  
> 代码基线: 当前仓库 HEAD（backlog 基线 `docs/backlog.md` 标注为 2026-04-21）

---

## 1. 审查范围与总体风险

### 审查范围
- 基石件 F1-F7 的实际落地状态与偏差
- Wave 0 / Wave 1 任务在当前代码中的完成情况
- 与 backlog（尤其 S18~S22、R4、X10、L10、S-nonce）的关系
- 对已完成与未完成项分别给出优化建议

### 总体风险判定
- **阻塞合并项（P0）: 1 项**
- **高优先级（P1）: 4 项**
- **改进项（P2）: 5 项**

结论:
- **已完成较好**: F1、F5
- **已落地但未收口**: F6、F7、F3
- **明显未完成**: F2（PG 主链接线）、F4（独立 listener + route group）
- 当前最大风险不是“完全没做”，而是**新旧链路并存导致语义断层**。

---

## 2. 完成度矩阵（合并六席位结论）

| 基石件 | 完成度 | 结论 | 证据（节选） |
|---|---|---|---|
| F1 JWT Registry | 90% | 已完成 | `runtime/auth/config/registry.go`, `cmd/core-bundle/main.go` |
| F2 Refresh Opaque + Store + PG | 45% | 部分完成（接口/内存/DDL 有，业务主链未切） | `runtime/auth/refresh/store.go`, `adapters/postgres/migrations/007_refresh_tokens.sql`, `cells/access-core/slices/sessionrefresh/service.go` |
| F3 Selector/声明式鉴权 | 75% | 变体完成（`auth.Declare + FinalizeAuth` 已落地） | `cells/access-core/cell.go`, `runtime/http/router/router.go`, `runtime/bootstrap/bootstrap_phases.go` |
| F4 独立 listener + RouteGroup | 20% | 未完成 | `cmd/core-bundle/bundle.go`, `runtime/bootstrap/bootstrap_phases.go` |
| F5 Errcode Classifier | 90% | 已完成 | `pkg/errcode/classify.go`, `cells/access-core/slices/sessionvalidate/service.go` |
| F6 Lifecycle + Sweep | 70% | 框架完成，应用接线未完全切 lifecycle | `runtime/bootstrap/lifecycle.go`, `runtime/bootstrap/bootstrap.go`, `cells/access-core/cell.go` |
| F7 Principal 统一契约 | 70% | 主体注入已完成，授权语义未统一收口 | `runtime/auth/principal.go`, `runtime/auth/authenticator.go`, `cells/access-core/slices/rbacassign/handler.go` |

Wave 状态:
- Wave 0: **部分完成**（F1/F5 完成；F3/F6/F7 部分；F2/F4 未完）
- Wave 1: **部分完成**（S19 有进展；S21/S22/S41/S42 未收口）

---

## 3. 合并后问题表（按严重级别）

| 严重级别 | 席位 | 问题 | 证据（节选） | 根因 | 修复方向 |
|---|---|---|---|---|---|
| P0 | 架构/安全/产品/运维 | `/internal/v1` delegated auth 链路“认证通过但授权失败”风险 | `runtime/auth/principal.go`, `runtime/auth/authenticator.go`, `cells/access-core/slices/rbacassign/handler.go`, `runtime/auth/authz.go` | Principal 角色语义（`role:internal-admin`）与策略语义（`admin`）未统一 | 收口 F7：统一 internal service principal 与 policy 角色映射，并补正向成功 e2e |
| P1 | 安全/运维 | ServiceToken 默认可重放（nonceStore 未强制） | `runtime/auth/authenticator.go`, `cmd/core-bundle/main.go` | real 模式未把 anti-replay 变成强约束 | 引入 Redis nonce store；real 模式缺失即 fail-fast（对应 backlog `S-nonce`） |
| P1 | 架构/安全/测试 | F2 主链未切：refresh 仍 JWT + sessionRepo previous token 逻辑 | `cells/access-core/slices/sessionlogin/service.go`, `cells/access-core/slices/sessionrefresh/service.go` | 新 Store 抽象落地但 wiring 未收口 | 先接线切主链，再补 PG store 并发集测，再退役旧字段 |
| P1 | 架构/运维 | F4 未落地：仍单 listener + prefix guard | `cmd/core-bundle/bundle.go`, `runtime/bootstrap/bootstrap_phases.go` | 运行时隔离停留在路径层 | 先双 listener 最小版，再 route-group 化，最后移除 `WithInternalEndpointGuard` |
| P1 | 测试/产品 | internal 写接口缺少“真实成功路径”集测门禁 | `cmd/core-bundle/auth_integration_test.go` | 测试只验证 guard 不验证业务成功 | 新增 BuildApp 真实装配 e2e，service token -> roles assign/revoke 成功断言 |
| P2 | 可维护性 | 认证链路存在重复实现（middleware 与 authenticator 部分逻辑重复） | `runtime/auth/middleware.go`, `runtime/auth/authenticator.go` | F7 迁移中间态未收敛 | 统一由 authenticator 产出 principal，middleware 专注编排 |
| P2 | 测试 | S21/S22 测试口径与计划验收口径不一致 | `runtime/auth/jwt_aud_test.go`, `cells/access-core/auth_integration_test.go` | 测试范围偏 verifier，不足以覆盖真实路由链路 | table-driven 重构 + 真实路由 refresh 测试 |
| P2 | 产品 | first-run DX 仍需手工解析 JWT（未补 userId） | `cells/access-core/internal/dto/token_pair.go`, `docs/operations/first-run-setup.md` | Wave1 DX 条目未落地 | login response 补 `userId` + 文档去手工解码步骤 |
| P2 | 可维护性/运维 | F6 应用接线仍依赖 worker sink 特例 | `cells/access-core/cell.go` | lifecycle 架构完成但应用路径仍保留旧模式 | 用 lifecycle hook 替代 bootstrap worker sink 特例 |
| P2 | 产品/API 一致性 | S42 未落地，列表响应未统一 nextCursor 语义 | `contracts/http/auth/role/list/v1/response.schema.json`, `cells/access-core/slices/rbaccheck/handler.go` | contract 与治理规则未同步收口 | 按统一列表响应补 `nextCursor` 并增加治理检查 |

---

## 4. 根因问题簇（含数据流/调用链/设计原因）

## 簇 A: 认证主体已统一，授权语义未统一

### 症状
- internal 路由 delegated 后可通过 service token 鉴权，但在 role policy 阶段被拒。

### 数据流
- `Authorization: ServiceToken ...` -> service token verifier -> Principal(Service, roles=[role:internal-admin]) -> handler policy `AnyRole(admin)` -> 403。

### 调用链
- router delegated matcher -> auth middleware skip jwt path -> service token auth -> policy guard -> handler。

### 架构/设计根因
- F7 完成了“结构统一”（Principal），但没有完成“语义统一”（role mapping 或 policy 对齐）。

### 影响范围
- `/internal/v1/access/roles/assign|revoke` 及后续所有 internal write 路由。

---

## 簇 B: F2 基础件存在但业务主链未切，形成双轨

### 症状
- 已有 `refresh.Store` 与 migration 007，但 login/refresh 仍发 JWT refresh 并依赖 sessionRepo previous token。

### 数据流
- login -> issuer.Issue(refresh JWT) -> refresh endpoint VerifyIntent(refresh) -> GetByRefreshToken / GetByPreviousRefreshToken -> 更新 session 字段。

### 调用链
- `sessionlogin.Service.Login` -> `issuer.Issue(TokenIntentRefresh)`  
- `sessionrefresh.Service.Refresh` -> `verifyRefreshToken` -> `lookupSession` -> `rotateAndIssue`。

### 架构/设计根因
- 采用“接口先落地、业务后切换”的策略，但缺少强制收口 gate，导致中间态长期化。

### 影响范围
- replay 防护、并发一致性、审计质量、跨实例行为一致性。

---

## 簇 C: 控制面隔离仍停留在路径守卫，不是监听边界

### 症状
- `/internal/v1` 与公网共用 listener，依赖 prefix guard 分流。

### 数据流
- 所有流量共享 `:8080` 入站 -> 路由层前缀匹配 -> internal guard/JWT 匹配分支。

### 调用链
- bootstrap option `WithInternalEndpointGuard` -> router `WithInternalPathPrefixGuard` -> request-time matcher。

### 架构/设计根因
- F4 的 listener/route-group 架构未启动，运行时仍是中间态方案。

### 影响范围
- 网络隔离策略、证书策略独立性、故障域隔离、运维观测边界。

---

## 簇 D: 测试门禁偏“中间件行为”，不足以证明“用户旅程成功”

### 症状
- 存在“guard 通过即可”的测试，而非“业务成功”断言。

### 数据流
- 测试请求 -> guard/auth layer -> 结束；未延伸到 handler/service 成功路径。

### 调用链
- integration test 只断言非特定错误，未断言 assign/revoke 完整成功。

### 架构/设计根因
- 验收口径与计划目标口径不一致（功能可达 != 功能可用）。

### 影响范围
- 关键安全路径回归漏检，计划完成度被高估。

---

## 5. 开源对比表（每主题 >=3 项目）

## 主题 1: 控制面隔离与鉴权链分层（对应 F4/F7）

| 项目 | 核心观察 | 迁移到 GoCell 的建议 |
|---|---|---|
| Kubernetes apiserver | 认证链与授权链是独立过滤器；默认拒绝；可按 secure serving 构建入口 | 拆分 Authn/Authz 组件，统一 default-deny；listener 与处理链解耦 |
| etcd | client/peer/metrics 分 listener，TLS 策略分域管理 | 先做 `primary/internal/ops` 三平面监听；internal 入口独立网络策略 |
| Vault | 多 listener + `api_addr/cluster_addr` 显式边界；安全默认需显式放开 | 多 listener 模式下强制显式 advertise 地址与 fail-fast 校验 |

结论:
- F4 采用“监听边界隔离 + 鉴权链分离”有充分上游证据。
- 不能照搬点: K8s 的 TokenReview/SAR 生态、Vault 的 HA 特定字段语义、etcd 的存储系统特性。

## 主题 2: Refresh Token rotation/reuse/replay（对应 F2）

| 项目 | 核心观察 | 迁移到 GoCell 的建议 |
|---|---|---|
| Dex | opaque refresh + obsolete token + reuseInterval + 存储层 CAS | 保留 current/obsolete 双代模型，CAS 下沉到 Store，暴露 reuse 错误语义 |
| Ory Hydra/Fosite | refresh 默认 opaque；strict/graceful rotation；reuse 命中链式撤销 | 引入 family/request_id，支持 strict 默认 + 可配 grace，replay 命中级联 revoke |
| Keycloak | rotation + max reuse + session 级撤销 | replay 命中升级为 session 级处置；在线/离线会话分层治理 |

结论:
- F2 的目标方向正确，核心缺口在“业务主链切换 + PG 实现 + 回归门禁”。
- 不能照搬点: Keycloak 会话模型重量级、Hydra 的部署参数上限策略。

## 主题 3: Principal 统一注入与策略声明（对应 F3/F7）

| 项目 | 核心观察 | 迁移到 GoCell 的建议 |
|---|---|---|
| Kratos | claims 注入 context；selector 按 operation/path 组合匹配 | 保持 `auth.Declare` 治理，补 operation-template 级匹配与冲突 fail-fast |
| go-chi/jwtauth | Verifier 与 Authenticator 分层；token/err 先入 context 再决策 | 认证事实与响应策略解耦，统一错误映射与 principal 读写契约 |
| go-grpc-middleware/auth | AuthFunc 返回 `newCtx`; service-level override 模式成熟 | 提供 route/service 级 override 扩展点，但受治理规则约束 |

结论:
- F7 方向正确，应继续推进“认证结果统一注入、授权统一消费”。
- 不能照搬点: 框架默认错误模型（尤其文本/GRPC status）与 GoCell errcode 体系不同。

---

## 6. 已完成项可继续优化点

### F1（已完成）
- 增加启动期“effective issuer/audience”结构化日志与 `/readyz?verbose` 可见性。
- 增加多 key source 切换演练测试（rotation 迁移场景）。

### F5（已完成）
- 将 `IsExpected4xx` 与 handler 错误映射建立一致性测试，防止新增错误码导致日志级别漂移。
- 增加真实依赖故障注入集测（DB timeout/bad conn）验证 infra 分类落点。

### F3/F6/F7（已落地但未收口）
- F3: 把 delegated/public/exempt 的冲突规则统一在 compile/finalize 阶段 fail-fast。
- F6: 把 initialadmin 特殊 worker sink 接线迁移到 lifecycle hook。
- F7: 完成 role 语义收口后，补“internal 成功旅程”端到端验收测试。

---

## 7. 未完成项优化方案

### F2 优化（建议分三步，避免大爆炸）
1. **接线切换**: sessionlogin/sessionrefresh 改为仅消费 `refresh.Store`。
2. **存储闭环**: 实现 PG store + CAS + reuseInterval + session revoke。
3. **遗留清理**: 删除 `PreviousRefreshToken` 旧逻辑与 repo 接口。

### F4 优化（建议双阶段迁移）
1. 引入 `primary/internal` 双 listener 最小可用版，internal 仅承载 `/internal/v1`。
2. 上线 route-group registry，再移除 `WithInternalEndpointGuard`。

### 测试优化
- 先补 P0/P1 路径的 BuildApp 真装配用例，再扩展审计/分页等回归。
- journey/integration 中至少恢复 auth 关键路径非 stub 验证。

---

## 8. 修复优先级与最稳妥实现方向

### 阻塞合并项（必须先做）
1. P0: internal principal 与 policy 角色语义统一（含成功 e2e）。

### 高优先级（同一迭代）
1. real 模式强制 nonceStore（S-nonce）。
2. F2 主链切换到 refresh store（至少 in-memory + 接口稳定）。
3. F4 最小双 listener 落地（先隔离再重构 route group）。

### 改进项（下一迭代）
1. S21/S22/S41/S42 与 first-run DX 收尾。
2. lifecycle 应用侧统一化、authenticator/middleware 去重复。

---

## 9. 与 backlog 的对齐更新建议

建议将以下条目标记为“已部分吸收/需重排顺序”：
- `R4`, `X10`, `S18`, `S19`, `S20`, `S21`, `S22`, `L10`, `S-nonce`。

建议将 Wave 顺序从“按功能块”改为“按风险闭环”：
1. 先封安全与可用性断点（P0 + nonce replay）。
2. 再做运行时边界（listener）。
3. 最后做功能扩展与 DX。

---

## 10. 结论

- 该计划方向正确，且已有约 **60%~70% 架构骨架** 完成。
- 当前最关键不是继续“加新基石”，而是**消灭中间态双轨**：
  - F7 语义收口（可用性）
  - F2 主链收口（一致性/安全）
  - F4 边界收口（运行时隔离）
- 一旦以上三项收口，Wave 1 剩余条目可快速并行推进，计划收益会显著释放。
