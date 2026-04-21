# Auth 域重排实施计划（先架构/分层，再问题，再功能）

> 日期: 2026-04-21  
> 依据: 六席位审查报告 `docs/reviews/202604211230-023-auth-federated-whistle-six-seat-review.md` + backlog `docs/backlog.md`  
> 目标: 在不引入新架构债的前提下，收口 F2/F4/F7 中间态，随后清理问题，最后实现新功能与 DX

---

## 0. 计划原则

1. 先做结构收口，再做点状修复，再做功能扩展。
2. 每个阶段设置“可验证门禁”，未通过不得进入下一阶段。
3. 所有 `/internal/v1` 相关改动必须走真实装配链路（BuildApp）验证。
4. 优先处理 P0/P1 风险项（可用性与安全）再处理 P2/P3。

---

## 1. 当前状态快照（用于重排）

### 已完成/高完成
- F1 JWT Registry（已完成）
- F5 Errcode Classifier（已完成）

### 部分完成（需要收口）
- F3 声明式鉴权（变体完成）
- F6 Lifecycle（框架完成，应用接线待统一）
- F7 Principal（注入完成，策略语义待统一）

### 未完成
- F2 Refresh opaque + PG store 主链切换
- F4 控制面独立 listener + route group

---

## 2. 阶段 A（架构 / 设计 / 分层优化升级）

> 目标: 把当前“可运行但中间态”的链路收敛为稳定架构，消除双轨。

## A1. PR-AUTH-A1-PRINCIPAL-POLICY-CLOSURE（P0，0.5d）

### 范围
- 统一 internal service principal 与 policy 语义（`role:internal-admin` 与 internal 路由策略一致）
- 清理 internal 路由中潜在语义歧义（对应 backlog `L10`）

### 文件
- `runtime/auth/principal.go`
- `runtime/auth/authz.go`
- `cells/access-core/slices/rbacassign/handler.go`
- `cells/audit-core/slices/auditquery/handler.go`（若有 internal policy）

### 验证门禁
- BuildApp 集测: `/internal/v1/access/roles/assign|revoke` 使用合法 ServiceToken 返回业务成功
- 负向用例: 非法 token 401，合法 token + 无权限 403（语义清晰）

---

## A2. PR-AUTH-A2-NONCE-FAILFAST（P1，0.5d）

### 范围
- real 模式强制 nonce store（对应 backlog `S-nonce`）
- 缺失 nonce store 启动即失败

### 文件
- `runtime/auth/authenticator.go`
- `cmd/core-bundle/main.go`
- `cmd/core-bundle/shared_deps.go`

### 验证门禁
- real 模式无 nonce store: 启动失败
- replay token 集测: 第二次请求被拒

---

## A3. PR-AUTH-A3-REFRESH-CHAIN-SWITCH（P1，1.5d）

### 范围
- 先切业务主链到 `refresh.Store`（Issue/Rotate/Revoke）
- 保留旧 repo 字段只用于迁移兼容，禁止新增依赖
- 完成 PG store 实现接线与最小并发 CAS 保证

### 文件
- `cells/access-core/slices/sessionlogin/service.go`
- `cells/access-core/slices/sessionrefresh/service.go`
- `runtime/auth/refresh/*`
- `adapters/postgres/*refresh*`（新增 PG store 实现）
- `cmd/core-bundle/*` wiring

### 验证门禁
- 并发 100 goroutine Rotate（PG testcontainers）
- reuseInterval 内幂等重试；超窗触发 `ErrTokenReused` + session revoke
- 仓库 grep 不再出现 `GetByPreviousRefreshToken` 主路径依赖

---

## A4. PR-AUTH-A4-INTERNAL-LISTENER-MIN（P1，1d）

### 范围
- 先落地最小双 listener（`primary` + `internal`）
- `/internal/v1` 仅 internal listener 承载
- 先保留 guard 作为迁移保护层，随后逐步移除

### 文件
- `runtime/bootstrap/*`
- `cmd/core-bundle/bundle.go`
- `runtime/http/router/*`（最小 route-group 注册）

### 验证门禁
- primary 端口访问 `/internal/v1/*` 不可达
- internal 端口可达且需 service token
- 健康检查与 metrics 不受影响

---

## A5. PR-AUTH-A5-LIFECYCLE-APP-CONSOLIDATE（P2，0.5d）

### 范围
- 将 initialadmin 清理从 worker sink 特例迁移到 lifecycle hook
- 保留兼容入口一个迭代后删除

### 文件
- `cells/access-core/cell.go`
- `cells/access-core/internal/initialadmin/*`

### 验证门禁
- 启动 sweep + 停机 sweep 都可观测
- 删除过期 credential 文件路径稳定

---

## 阶段 A 退出条件

- P0/P1 架构断点全部关闭（A1~A4 完成）
- 无“认证通过但授权失败”的 internal 链路
- refresh 主链不再走旧 JWT refresh 旋转逻辑

---

## 3. 阶段 B（问题修复与回归补齐）

> 目标: 处理 backlog 中与 auth 域直接相关的已知问题，建立稳定回归网。

## B1. 测试与门禁收口（1d）

### 条目
- `S19` JWT audience drift 集测改真实 login 路径
- `S21` jwt aud 测试改 table-driven
- `S22` refresh wrong/missing audience 真实路由集测
- `S24` middleware refresh path e2e（补链路，不只 verifier）

### 验证门禁
- `go test ./runtime/auth/...`
- `go test ./cells/access-core/... -tags integration`

---

## B2. 错误与契约一致性修复（0.5d）

### 条目
- `S41` 事件序列化错误不再吞掉
- `L7(FMT15)` 列表响应强制 `nextCursor + hasMore` 对齐治理

### 验证门禁
- 对应 handler/contract test 全绿
- `go run ./cmd/gocell validate`

---

## B3. 运行与安全治理补强（0.5d）

### 条目
- `R4` 在 A4 后续彻底收口（移除旧 prefix guard）
- internal listener 的配置 fail-fast 与文档化

### 验证门禁
- grep 不再出现 `WithInternalEndpointGuard` 的生产接线
- 架构文档更新

---

## 阶段 B 退出条件

- auth 域关键回归测试覆盖“成功链路 + 失败链路 + 并发链路”
- contract 与治理规则通过，且无关键 TODO 依赖

---

## 4. 阶段 C（新功能实现与 DX 提升）

> 目标: 在架构稳定后交付面向用户/开发者的新能力。

## C1. PR-AUTH-C1-ROLELIST-CURSOR（0.5d）

### 条目
- `S42` 角色列表响应补齐 `nextCursor`
- 对齐统一列表响应格式

---

## C2. PR-AUTH-C2-FIRSTRUN-DX（0.5d）

### 条目
- login response 补 `userId`
- first-run 文档去手工 JWT 解析步骤
- 403 hint 优化（带 resolved path）

---

## C3. PR-AUTH-C3-ROUTEGROUP-FULL（1d）

### 条目
- 在 A4 最小双 listener 基础上升级完整 route-group（primary/internal/health）
- 可选 mTLS 入口策略

---

## 阶段 C 退出条件

- 新功能已覆盖 contract + integration + 文档
- 无新增架构债条目进入 backlog

---

## 5. 推荐执行顺序（单人 / 双人）

### 单人顺序（约 6~7 工作日）
1. A1 -> A2 -> A3 -> A4 -> A5
2. B1 -> B2 -> B3
3. C1 -> C2 -> C3

### 双人并行（约 4~5 工作日）
- Track A（认证语义）: A1 -> A2 -> A3
- Track B（运行时边界）: A4 -> A5 -> B3
- 汇合后: B1/B2 并行，最后 C1/C2/C3

---

## 6. 每阶段统一验证清单

- `go build ./...`
- `go test ./...`
- 涉及并发: `go test -race ./runtime/auth/... ./cells/access-core/...`
- 变更契约后: `go run ./cmd/gocell validate`
- 提交前: `golangci-lint run ./修改的包/...`

---

## 7. 与原计划的关系（重排说明）

- 保留原计划“七基石”方向不变。
- 调整执行策略为:
  1. **先收口架构中间态**（A 阶段）
  2. **再清问题与回归**（B 阶段）
  3. **最后做功能/DX 增量**（C 阶段）
- 这样可避免“功能先行导致中间态继续扩散”的风险。

---

## 8. 本计划对应 backlog 关键条目

- A 阶段: `L10`, `S-nonce`, `X10`, `R4`（前置收口）
- B 阶段: `S19`, `S21`, `S22`, `S24`, `S41`, `L7(FMT15)`
- C 阶段: `S42` + first-run DX 相关审查项

---

## 9. 成功判据（Definition of Done）

1. `/internal/v1` 的成功/失败授权路径都有稳定集测，且语义一致。
2. refresh token 主链完全由 store 驱动，旧 previous-refresh 路径退役。
3. listener 边界完成隔离，不再依赖单纯前缀守卫。
4. wave1 遗留测试与契约项收口，新增功能不引入结构性回归。
